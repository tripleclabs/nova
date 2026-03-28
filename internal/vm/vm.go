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
	"strconv"
	"strings"

	"github.com/3clabs/nova/internal/cloudinit"
	"github.com/3clabs/nova/internal/config"
	"github.com/3clabs/nova/internal/hypervisor"
	"github.com/3clabs/nova/internal/image"
	"github.com/3clabs/nova/internal/state"
)

// Orchestrator wires together all subsystems for VM lifecycle management.
type Orchestrator struct {
	store    *state.Store
	images   *image.Manager
	stateDir string
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

// Up parses config, pulls the image, creates a CoW overlay, and boots the VM.
func (o *Orchestrator) Up(ctx context.Context, cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	vmName := cfg.VM.Name
	if vmName == "" {
		vmName = "default"
	}
	machineID := vmName

	// Check if already running.
	if existing, err := o.store.Get(machineID); err == nil {
		if existing.State == state.StateRunning {
			return fmt.Errorf("VM %q is already running (use 'nova down' first)", machineID)
		}
		// Clean up stale state from a previously stopped machine.
		o.store.Delete(machineID)
	}

	// Create machine record.
	machine := &state.Machine{
		ID:         machineID,
		Name:       vmName,
		State:      state.StateCreating,
		ConfigHash: hashConfig(cfgPath),
	}
	if err := o.store.Create(machine); err != nil {
		return err
	}

	// Lock the machine for this operation.
	unlock, err := o.store.Lock(machineID)
	if err != nil {
		return err
	}
	defer unlock()

	machineDir := o.store.MachineDir(machineID)

	// Pull image.
	slog.Info("pulling image", "ref", cfg.VM.Image)
	baseDisk, err := o.images.Pull(ctx, cfg.VM.Image, func(complete, total int64) {
		if total > 0 {
			pct := float64(complete) / float64(total) * 100
			fmt.Printf("\rPulling image... %.0f%%", pct)
		}
	})
	if err != nil {
		o.store.Delete(machineID)
		return fmt.Errorf("pulling image: %w", err)
	}
	fmt.Println()

	// Create CoW overlay.
	slog.Info("creating disk overlay")
	overlayPath, err := image.CreateOverlay(baseDisk, machineDir)
	if err != nil {
		o.store.Delete(machineID)
		return fmt.Errorf("creating disk overlay: %w", err)
	}

	// Generate SSH keypair.
	slog.Info("generating SSH keypair")
	sshDir := filepath.Join(machineDir, "ssh")
	keyPair, err := cloudinit.GenerateSSHKeyPair(sshDir)
	if err != nil {
		o.store.Delete(machineID)
		return fmt.Errorf("generating SSH keys: %w", err)
	}

	// Generate cloud-init config and CIDATA ISO.
	slog.Info("building cloud-init ISO")
	ciCfg := cloudinit.GeneratorConfig{
		Hostname:      vmName,
		AuthorizedKey: keyPair.AuthorizedKey,
	}
	// Look for user cloud-config alongside nova.hcl.
	userDataPath := filepath.Join(filepath.Dir(cfgPath), "cloud-config.yaml")
	if _, err := os.Stat(userDataPath); err == nil {
		ciCfg.UserDataPath = userDataPath
	}

	userData, err := cloudinit.Generate(ciCfg)
	if err != nil {
		o.store.Delete(machineID)
		return fmt.Errorf("generating cloud-init config: %w", err)
	}

	cidataPath := filepath.Join(machineDir, "cidata.iso")
	if err := cloudinit.BuildCIDATAISO(cidataPath, vmName, userData); err != nil {
		o.store.Delete(machineID)
		return fmt.Errorf("building CIDATA ISO: %w", err)
	}

	// Build hypervisor config.
	memMB, err := parseMemoryMB(cfg.VM.Memory)
	if err != nil {
		o.store.Delete(machineID)
		return err
	}

	vmCfg := hypervisor.VMConfig{
		Name:       vmName,
		CPUs:       uint(cfg.VM.CPUs),
		MemoryMB:   memMB,
		DiskPath:   overlayPath,
		CIDATAPath: cidataPath,
		LogPath:    filepath.Join(machineDir, "console.log"),
	}

	// Port forwards.
	for _, pf := range cfg.VM.PortForwards {
		vmCfg.Network.PortForwards = append(vmCfg.Network.PortForwards, hypervisor.PortForward{
			HostPort:  pf.Host,
			GuestPort: pf.Guest,
			Protocol:  pf.Protocol,
		})
	}

	// Shared folders.
	for _, sf := range cfg.VM.SharedFolders {
		vmCfg.Shares = append(vmCfg.Shares, hypervisor.ShareConfig{
			Tag:      sanitizeTag(sf.GuestPath),
			HostPath: sf.HostPath,
			ReadOnly: sf.ReadOnly,
		})
	}

	// Start hypervisor.
	hv, err := hypervisor.New()
	if err != nil {
		o.store.Delete(machineID)
		return err
	}

	fmt.Printf("Starting VM %q (%d CPUs, %dMB RAM)...\n", vmName, vmCfg.CPUs, vmCfg.MemoryMB)
	if err := hv.Start(ctx, vmCfg); err != nil {
		o.store.Delete(machineID)
		return fmt.Errorf("starting VM: %w", err)
	}

	// Update state to running.
	machine.State = state.StateRunning
	machine.PID = os.Getpid() // The host process managing the VM.
	if err := o.store.Update(machine); err != nil {
		hv.ForceKill()
		return err
	}

	fmt.Printf("VM %q is running.\n", vmName)
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

	// For now, send SIGTERM to the process managing the VM.
	// In a full implementation, we'd communicate with the running hypervisor instance.
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
