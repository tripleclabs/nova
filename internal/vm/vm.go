// Package vm provides the high-level orchestration that ties together
// configuration, image management, hypervisor, and state.
package vm

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/3clabs/nova/internal/cloudinit"
	"github.com/3clabs/nova/internal/config"
	"github.com/3clabs/nova/internal/hypervisor"
	"github.com/3clabs/nova/internal/image"
	"github.com/3clabs/nova/internal/network"
	"github.com/3clabs/nova/internal/state"
)

// Orchestrator wires together all subsystems for VM lifecycle management.
type Orchestrator struct {
	store    *state.Store
	images   *image.Manager
	stateDir string
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
	return &Orchestrator{store: store, images: imgMgr, stateDir: novaDir}, nil
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

	return &Orchestrator{store: store, images: imgMgr, stateDir: novaDir}, nil
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

	// Create CoW overlay.
	overlayPath, err := image.CreateOverlay(baseDisk, machineDir)
	if err != nil {
		o.store.Delete(machineID)
		return fmt.Errorf("creating disk overlay: %w", err)
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

	machine.State = state.StateRunning
	machine.PID = os.Getpid()
	if err := o.store.Update(machine); err != nil {
		hv.ForceKill()
		return err
	}

	fmt.Printf("%q is running.\n", node.Name)
	return nil
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

	machine.State = state.StateStopped
	machine.PID = 0
	if err := o.store.Update(machine); err != nil {
		return err
	}

	fmt.Printf("VM %q stopped.\n", name)
	return nil
}

// Nuke force-kills a VM and deletes all its data.
func (o *Orchestrator) Nuke(name string) error {
	if name == "" {
		name = "default"
	}

	if _, err := o.store.Get(name); err != nil {
		return fmt.Errorf("VM %q not found", name)
	}

	if err := o.store.Delete(name); err != nil {
		return err
	}

	fmt.Printf("VM %q nuked.\n", name)
	return nil
}

// Status returns all known machines.
func (o *Orchestrator) Status() ([]*state.Machine, error) {
	return o.store.List()
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
