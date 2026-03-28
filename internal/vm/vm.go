// Package vm provides the high-level orchestration that ties together
// configuration, image management, hypervisor, and state.
package vm

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3clabs/nova/internal/cloudinit"
	"github.com/3clabs/nova/internal/config"
	"github.com/3clabs/nova/internal/hypervisor"
	"github.com/3clabs/nova/internal/image"
	"github.com/3clabs/nova/internal/network"
	"github.com/3clabs/nova/internal/provisioner"
	"github.com/3clabs/nova/internal/state"
)

// ExecResult holds the output of an SSH command execution.
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// Orchestrator wires together all subsystems for VM lifecycle management.
// It retains hypervisor handles for the lifetime of each VM so that
// stop/kill/exec operations can be performed without PID-based signalling.
type Orchestrator struct {
	mu          sync.RWMutex
	store       *state.Store
	images      *image.Manager
	stateDir    string
	hypervisors map[string]hypervisor.Hypervisor // machineID → live handle
}

// NewOrchestratorWithDir creates an Orchestrator with a custom state directory (for testing).
func NewOrchestratorWithDir(novaDir string) (*Orchestrator, error) {
	store, err := state.NewStore(novaDir)
	if err != nil {
		return nil, err
	}
	imgMgr, err := image.NewManager(filepath.Join(novaDir, "cache", "images"))
	if err != nil {
		return nil, err
	}
	return &Orchestrator{store: store, images: imgMgr, stateDir: novaDir, hypervisors: make(map[string]hypervisor.Hypervisor)}, nil
}

// NewOrchestrator creates a new Orchestrator using the default Nova state directory.
func NewOrchestrator() (*Orchestrator, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting home dir: %w", err)
	}
	novaDir := filepath.Join(home, ".nova")

	store, err := state.NewStore(novaDir)
	if err != nil {
		return nil, err
	}

	imgMgr, err := image.NewManager(filepath.Join(novaDir, "cache", "images"))
	if err != nil {
		return nil, err
	}

	return &Orchestrator{store: store, images: imgMgr, stateDir: novaDir, hypervisors: make(map[string]hypervisor.Hypervisor)}, nil
}

// Up parses config and boots all nodes defined in it.
func (o *Orchestrator) Up(ctx context.Context, cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	nodes := cfg.ResolveNodes()

	// Build /etc/hosts entries for multi-node clusters.
	var hostsEntries []cloudinit.HostEntry
	if len(nodes) > 1 {
		for _, n := range nodes {
			if n.IP != "" {
				hostsEntries = append(hostsEntries, cloudinit.HostEntry{
					IP:       n.IP,
					Hostname: n.Name,
				})
			}
		}
	}

	// Look for user cloud-config alongside nova.hcl.
	var userDataPath string
	candidate := filepath.Join(filepath.Dir(cfgPath), "cloud-config.yaml")
	if _, err := os.Stat(candidate); err == nil {
		userDataPath = candidate
	}

	for _, node := range nodes {
		if err := o.upNode(ctx, cfgPath, node, hostsEntries, userDataPath); err != nil {
			return fmt.Errorf("node %q: %w", node.Name, err)
		}
	}

	return nil
}

func (o *Orchestrator) upNode(
	ctx context.Context,
	cfgPath string,
	node config.ResolvedNode,
	hostsEntries []cloudinit.HostEntry,
	userDataPath string,
) error {
	machineID := node.Name

	// Check if already running.
	if existing, err := o.store.Get(machineID); err == nil {
		if existing.State == state.StateRunning {
			return fmt.Errorf("already running (use 'nova down' first)")
		}
		o.store.Delete(machineID)
	}

	machine := &state.Machine{
		ID:         machineID,
		Name:       node.Name,
		State:      state.StateCreating,
		ConfigHash: hashConfig(cfgPath),
	}
	if err := o.store.Create(machine); err != nil {
		return err
	}

	unlock, err := o.store.Lock(machineID)
	if err != nil {
		return err
	}
	defer unlock()

	machineDir := o.store.MachineDir(machineID)

	// Pull image.
	slog.Info("pulling image", "node", node.Name, "ref", node.Image)
	baseDisk, err := o.images.Pull(ctx, node.Image, func(complete, total int64) {
		if total > 0 {
			pct := float64(complete) / float64(total) * 100
			fmt.Printf("\r[%s] Pulling image... %.0f%%", node.Name, pct)
		}
	})
	if err != nil {
		o.store.Delete(machineID)
		return fmt.Errorf("pulling image: %w", err)
	}
	fmt.Println()

	// Look up OS metadata from the image cache for OS-aware cloud-init config.
	var imageOS string
	if ci := o.images.ResolveImage(node.Image); ci != nil {
		imageOS = ci.OS
	}

	// Create CoW overlay.
	overlayPath, err := image.CreateOverlay(baseDisk, machineDir)
	if err != nil {
		o.store.Delete(machineID)
		return fmt.Errorf("creating disk overlay: %w", err)
	}

	// Apple Virtualization.framework requires raw disk images.
	if runtime.GOOS == "darwin" {
		slog.Info("converting overlay to raw for VZ framework")
		rawPath, err := image.ConvertToRaw(overlayPath)
		if err != nil {
			o.store.Delete(machineID)
			return fmt.Errorf("converting to raw: %w", err)
		}
		overlayPath = rawPath
	}

	// Generate SSH keypair.
	sshDir := filepath.Join(machineDir, "ssh")
	keyPair, err := cloudinit.GenerateSSHKeyPair(sshDir)
	if err != nil {
		o.store.Delete(machineID)
		return fmt.Errorf("generating SSH keys: %w", err)
	}

	// Build cloud-init config.
	needsRosetta := runtime.GOOS == "darwin" && hypervisor.NeedsEmulation(node.Arch)
	ciCfg := cloudinit.GeneratorConfig{
		Hostname:      node.Name,
		AuthorizedKey: keyPair.AuthorizedKey,
		UserDataPath:  userDataPath,
		Hosts:         hostsEntries,
		Rosetta:       needsRosetta,
		OS:            imageOS,
	}
	// Inject extra user if configured.
	if node.User != nil {
		ciCfg.ExtraUser = &cloudinit.UserConfig{
			Name:         node.User.Name,
			SSHKey:       node.User.SSHKey,
			PasswordHash: node.User.PasswordHash,
			Groups:       node.User.Groups,
			Shell:        node.User.Shell,
		}
	}
	// Inject shared folder mounts into cloud-init.
	// Use 9p on Linux (QEMU) and virtiofs on macOS (Apple Virtualization.framework).
	mountType := "virtiofs"
	if runtime.GOOS == "linux" {
		mountType = "9p"
	}
	for _, sf := range node.SharedFolders {
		ciCfg.Mounts = append(ciCfg.Mounts, cloudinit.SharedMount{
			Tag:       sanitizeTag(sf.GuestPath),
			GuestPath: sf.GuestPath,
			MountType: mountType,
		})
	}

	userData, err := cloudinit.Generate(ciCfg)
	if err != nil {
		o.store.Delete(machineID)
		return fmt.Errorf("generating cloud-init: %w", err)
	}

	cidataPath := filepath.Join(machineDir, "cidata.iso")
	if err := cloudinit.BuildCIDATAISO(cidataPath, node.Name, userData); err != nil {
		o.store.Delete(machineID)
		return fmt.Errorf("building CIDATA ISO: %w", err)
	}

	// Build hypervisor config.
	memMB, err := parseMemoryMB(node.Memory)
	if err != nil {
		o.store.Delete(machineID)
		return err
	}

	vmCfg := hypervisor.VMConfig{
		Name:       node.Name,
		Arch:       node.Arch,
		CPUs:       uint(node.CPUs),
		MemoryMB:   memMB,
		DiskPath:   overlayPath,
		CIDATAPath: cidataPath,
		LogPath:    filepath.Join(machineDir, "console.log"),
		MachineDir: machineDir,
		PIDPath:    filepath.Join(machineDir, "hypervisor.pid"),
	}

	for _, pf := range node.PortForwards {
		vmCfg.Network.PortForwards = append(vmCfg.Network.PortForwards, hypervisor.PortForward{
			HostPort:  pf.Host,
			GuestPort: pf.Guest,
			Protocol:  pf.Protocol,
		})
	}

	for _, sf := range node.SharedFolders {
		vmCfg.Shares = append(vmCfg.Shares, hypervisor.ShareConfig{
			Tag:      sanitizeTag(sf.GuestPath),
			HostPath: sf.HostPath,
			ReadOnly: sf.ReadOnly,
		})
	}

	// Check host port availability.
	var hostPorts []int
	for _, pf := range vmCfg.Network.PortForwards {
		hostPorts = append(hostPorts, pf.HostPort)
	}
	if err := network.CheckPortsAvailable(hostPorts); err != nil {
		o.store.Delete(machineID)
		return fmt.Errorf("port conflict: %w", err)
	}

	// Start hypervisor.
	hv, err := hypervisor.New()
	if err != nil {
		o.store.Delete(machineID)
		return err
	}

	fmt.Printf("Starting %q (%d CPUs, %dMB RAM)...\n", node.Name, vmCfg.CPUs, vmCfg.MemoryMB)
	if err := hv.Start(ctx, vmCfg); err != nil {
		o.store.Delete(machineID)
		return fmt.Errorf("starting VM: %w", err)
	}

	// Retain the hypervisor handle for later stop/kill/exec operations.
	o.mu.Lock()
	o.hypervisors[machineID] = hv
	o.mu.Unlock()

	machine.State = state.StateRunning
	machine.PID = readPIDFile(filepath.Join(machineDir, "hypervisor.pid"))
	if err := o.store.Update(machine); err != nil {
		hv.ForceKill()
		return err
	}

	// Write the shell user (for nova shell) — defaults to "nova" unless a user block overrides.
	shellUser := "nova"
	if node.User != nil {
		shellUser = node.User.Name
	}
	os.WriteFile(filepath.Join(machineDir, "shell_user"), []byte(shellUser), 0644)

	fmt.Printf("%q is running.\n", node.Name)

	// Run provisioners if any are defined.
	if len(node.Provisioners) > 0 {
		fmt.Printf("[%s] Waiting for SSH before provisioning...\n", node.Name)
		if err := o.WaitReady(ctx, machineID); err != nil {
			return fmt.Errorf("waiting for SSH: %w", err)
		}

		// Read the SSH private key for the provisioner.
		keyData, err := os.ReadFile(filepath.Join(machineDir, "ssh", "nova_ed25519"))
		if err != nil {
			return fmt.Errorf("reading SSH key for provisioner: %w", err)
		}

		guestIP, err := hv.GuestIP()
		if err != nil {
			return fmt.Errorf("getting guest IP for provisioner: %w", err)
		}

		sshCfg := provisioner.SSHConfig{
			Host:       guestIP,
			Port:       "22",
			User:       "nova",
			PrivateKey: keyData,
		}

		output := &provisioner.OutputWriter{
			Prefix: node.Name,
			Writer: os.Stdout,
		}

		fmt.Printf("[%s] Running %d provisioner(s)...\n", node.Name, len(node.Provisioners))
		if err := provisioner.RunAll(ctx, sshCfg, node.Provisioners, output); err != nil {
			return fmt.Errorf("provisioning: %w", err)
		}
		fmt.Printf("[%s] Provisioning complete.\n", node.Name)
	}

	return nil
}

// readPIDFile reads an integer PID from a file, returning 0 on any error.
func readPIDFile(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return pid
}

// Down gracefully stops a running VM.
func (o *Orchestrator) Down(name string) error {
	if name == "" {
		name = "default"
	}

	machine, err := o.store.Get(name)
	if err != nil {
		return fmt.Errorf("VM %q not found", name)
	}
	if machine.State != state.StateRunning {
		return fmt.Errorf("VM %q is not running (state: %s)", name, machine.State)
	}

	// Use retained hypervisor handle if available, otherwise best-effort.
	o.mu.Lock()
	hv := o.hypervisors[name]
	o.mu.Unlock()

	if hv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := hv.Stop(ctx); err != nil {
			slog.Warn("graceful stop failed", "vm", name, "error", err)
		}
		o.mu.Lock()
		delete(o.hypervisors, name)
		o.mu.Unlock()
	}

	machine.State = state.StateStopped
	machine.PID = 0
	if err := o.store.Update(machine); err != nil {
		return err
	}

	fmt.Printf("VM %q stopped.\n", name)
	return nil
}

// Destroy force-kills a VM and deletes all its data.
func (o *Orchestrator) Destroy(name string) error {
	if name == "" {
		name = "default"
	}

	if _, err := o.store.Get(name); err != nil {
		return fmt.Errorf("VM %q not found", name)
	}

	// Use retained hypervisor handle if available.
	o.mu.Lock()
	hv := o.hypervisors[name]
	delete(o.hypervisors, name)
	o.mu.Unlock()

	if hv != nil {
		hv.ForceKill()
	}

	if err := o.store.Delete(name); err != nil {
		return err
	}

	fmt.Printf("VM %q destroyed.\n", name)
	return nil
}

// ForceKill immediately terminates a VM without cleanup — simulates a power failure.
// The machine state is set to "error" but disk/state are preserved.
func (o *Orchestrator) ForceKill(name string) error {
	if name == "" {
		name = "default"
	}

	machine, err := o.store.Get(name)
	if err != nil {
		return fmt.Errorf("VM %q not found", name)
	}

	o.mu.Lock()
	hv := o.hypervisors[name]
	delete(o.hypervisors, name)
	o.mu.Unlock()

	if hv != nil {
		hv.ForceKill()
	}

	machine.State = state.StateError
	machine.PID = 0
	o.store.Update(machine)

	slog.Info("VM force killed", "vm", name)
	return nil
}

// GuestIP returns the IP address of a running VM.
func (o *Orchestrator) GuestIP(name string) (string, error) {
	o.mu.RLock()
	hv := o.hypervisors[name]
	o.mu.RUnlock()

	if hv == nil {
		return "", fmt.Errorf("VM %q has no active hypervisor handle", name)
	}
	return hv.GuestIP()
}

// ExecSSH runs a command on a VM via SSH and returns the result.
func (o *Orchestrator) ExecSSH(name, command string, timeout time.Duration) (*ExecResult, error) {
	if name == "" {
		name = "default"
	}

	machine, err := o.store.Get(name)
	if err != nil {
		return nil, fmt.Errorf("VM %q not found", name)
	}
	if machine.State != state.StateRunning {
		return nil, fmt.Errorf("VM %q is not running", name)
	}

	// Read the private key.
	machineDir := o.store.MachineDir(name)
	keyPath := filepath.Join(machineDir, "ssh", "nova_ed25519")
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("reading SSH key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return nil, fmt.Errorf("parsing SSH key: %w", err)
	}

	// Get guest IP.
	guestIP, err := o.GuestIP(name)
	if err != nil {
		return nil, fmt.Errorf("getting guest IP: %w", err)
	}

	// Connect with timeout.
	config := &ssh.ClientConfig{
		User:            "nova",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	addr := net.JoinHostPort(guestIP, "22")
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("SSH dial %s: %w", addr, err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("creating SSH session: %w", err)
	}
	defer session.Close()

	var stdout, stderr strings.Builder
	session.Stdout = &stdout
	session.Stderr = &stderr

	// Run with timeout.
	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()

	select {
	case err := <-done:
		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*ssh.ExitError); ok {
				exitCode = exitErr.ExitStatus()
			} else {
				return nil, fmt.Errorf("SSH exec: %w", err)
			}
		}
		return &ExecResult{
			ExitCode: exitCode,
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
		}, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("SSH exec timed out after %v", timeout)
	}
}

// WaitReady blocks until a VM is reachable via SSH, or the timeout expires.
func (o *Orchestrator) WaitReady(ctx context.Context, name string) error {
	if name == "" {
		name = "default"
	}

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("VM %q not ready: %w", name, ctx.Err())
		default:
		}

		result, err := o.ExecSSH(name, "true", 5*time.Second)
		if err == nil && result.ExitCode == 0 {
			return nil
		}

		slog.Debug("waiting for SSH", "vm", name, "error", err)
		time.Sleep(2 * time.Second)
	}
}

// Status returns all known machines.
func (o *Orchestrator) Status() ([]*state.Machine, error) {
	return o.store.List()
}

// DestroyAll force-kills all VMs and cleans up all state. Used for test teardown.
func (o *Orchestrator) DestroyAll() error {
	machines, err := o.store.List()
	if err != nil {
		return err
	}
	for _, m := range machines {
		o.Destroy(m.ID)
	}
	return nil
}

func parseMemoryMB(mem string) (uint64, error) {
	mem = strings.TrimSpace(mem)
	if len(mem) < 2 {
		return 0, fmt.Errorf("invalid memory value: %q", mem)
	}
	suffix := strings.ToUpper(mem[len(mem)-1:])
	numStr := mem[:len(mem)-1]
	val, err := strconv.ParseUint(numStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing memory %q: %w", mem, err)
	}
	switch suffix {
	case "G":
		return val * 1024, nil
	case "M":
		return val, nil
	default:
		return 0, fmt.Errorf("unknown memory suffix: %s", suffix)
	}
}

func hashConfig(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h[:8])
}

func sanitizeTag(guestPath string) string {
	tag := strings.ReplaceAll(guestPath, "/", "_")
	tag = strings.TrimLeft(tag, "_")
	if tag == "" {
		tag = "share"
	}
	return tag
}
