//go:build darwin

package hypervisor

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Code-Hex/vz/v3"
)

// vzEngine implements the Hypervisor interface using Apple's Virtualization.framework.
type vzEngine struct {
	mu      sync.Mutex
	vm      *vz.VirtualMachine
	state   State
	cfg     VMConfig
	macAddr string // Deterministic MAC for DHCP lease lookup.
}

func newVZEngine() (Hypervisor, error) {
	return &vzEngine{state: StateStopped}, nil
}

func (e *vzEngine) Start(ctx context.Context, cfg VMConfig) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.cfg = cfg
	e.state = StateStarting
	slog.Info("configuring VM", "name", cfg.Name, "cpus", cfg.CPUs, "memory_mb", cfg.MemoryMB)

	vzCfg, err := e.buildVZConfig(cfg)
	if err != nil {
		e.state = StateError
		return fmt.Errorf("building VM config: %w", err)
	}

	valid, err := vzCfg.Validate()
	if !valid || err != nil {
		e.state = StateError
		return fmt.Errorf("invalid VM config: %w", err)
	}

	vm, err := vz.NewVirtualMachine(vzCfg)
	if err != nil {
		e.state = StateError
		return fmt.Errorf("creating VM: %w", err)
	}
	e.vm = vm

	slog.Info("starting VM", "name", cfg.Name)
	if err := vm.Start(); err != nil {
		e.state = StateError
		return fmt.Errorf("starting VM: %w", err)
	}

	// Wait for the VM to reach the running state.
	e.state = StateRunning
	slog.Info("VM started", "name", cfg.Name)

	// Monitor state changes in background.
	go e.watchState()

	return nil
}

func (e *vzEngine) Stop(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.vm == nil {
		return fmt.Errorf("VM not started")
	}

	if !e.vm.CanRequestStop() {
		return e.forceKillLocked()
	}

	stopped, err := e.vm.RequestStop()
	if err != nil {
		return fmt.Errorf("requesting stop: %w", err)
	}
	if !stopped {
		slog.Warn("graceful stop request returned false, forcing kill")
		return e.forceKillLocked()
	}

	e.state = StateStopped
	slog.Info("VM stopped gracefully", "name", e.cfg.Name)
	return nil
}

func (e *vzEngine) ForceKill() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.forceKillLocked()
}

func (e *vzEngine) forceKillLocked() error {
	if e.vm == nil {
		return fmt.Errorf("VM not started")
	}
	if err := e.vm.Stop(); err != nil {
		return fmt.Errorf("force stopping VM: %w", err)
	}
	e.state = StateStopped
	slog.Info("VM force killed", "name", e.cfg.Name)
	return nil
}

func (e *vzEngine) GetState() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

func (e *vzEngine) GuestIP() (string, error) {
	e.mu.Lock()
	mac := e.macAddr
	state := e.state
	e.mu.Unlock()

	if state != StateRunning {
		return "", fmt.Errorf("VM is not running (state: %s)", state)
	}
	if mac == "" {
		return "", fmt.Errorf("no MAC address set; cannot discover guest IP")
	}

	// Try DHCP leases first (fastest, works when dhcp-identifier: mac is set),
	// then fall back to ARP table scanning (works for any DHCP client).
	// VZ NAT typically assigns DHCP within 3-5s of boot.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ip, err := LookupDHCPLease(mac); err == nil && ip != "" {
			return ip, nil
		}
		if ip, err := LookupARPByMAC(mac); err == nil && ip != "" {
			return ip, nil
		}
		time.Sleep(time.Second)
	}
	return "", fmt.Errorf("guest IP not found for MAC %s after 30s (checked DHCP leases and ARP)", mac)
}

// LookupARPByMAC scans the host ARP table for a given MAC address.
func LookupARPByMAC(mac string) (string, error) {
	out, err := exec.Command("arp", "-a").Output()
	if err != nil {
		return "", fmt.Errorf("running arp -a: %w", err)
	}
	return ParseARPOutput(string(out), mac)
}

// ParseARPOutput parses `arp -a` output to find the IP for a given MAC.
// Format: "? (192.168.64.3) at 52:54:0:a0:33:bd on bridge101 ifscope [bridge]"
func ParseARPOutput(output, mac string) (string, error) {
	normalizedMAC := strings.ToLower(mac)
	// Also try the no-leading-zeros format (arp uses "52:54:0:a0:33:bd" not "52:54:00:a0:33:bd")
	shortMAC := normalizeMACForDHCP(mac)
	for _, line := range strings.Split(output, "\n") {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, normalizedMAC) && !strings.Contains(lower, shortMAC) {
			continue
		}
		start := strings.Index(line, "(")
		end := strings.Index(line, ")")
		if start >= 0 && end > start {
			return line[start+1 : end], nil
		}
	}
	return "", fmt.Errorf("MAC %s not found in ARP table", mac)
}

// DHCPLeasesPath is the macOS bootpd leases file. Exported for testing.
const DHCPLeasesPath = "/var/db/dhcpd_leases"

// LookupDHCPLease reads the macOS bootpd DHCP leases file and returns the
// IP address for the given MAC. Exported for testing.
func LookupDHCPLease(mac string) (string, error) {
	data, err := os.ReadFile(DHCPLeasesPath)
	if err != nil {
		return "", fmt.Errorf("reading DHCP leases: %w", err)
	}
	return ParseDHCPLeases(string(data), mac)
}

// ParseDHCPLeases parses the macOS /var/db/dhcpd_leases file to find the IP
// for a given MAC address. The file contains brace-delimited blocks with
// key=value pairs. The hw_address field contains the MAC in a DHCP client ID
// format where the actual hardware address appears at the end.
//
// Example entry:
//
//	{
//	    name=my-vm
//	    ip_address=192.168.64.3
//	    hw_address=ff,0:0:0:2:0:1:0:1:31:5a:ff:52:52:54:0:0:0:2
//	    lease=0x69c850e4
//	}
//
// Exported for testing.
func ParseDHCPLeases(content, mac string) (string, error) {
	// Normalize MAC: strip leading zeros and lowercase for comparison.
	// "52:54:00:00:00:02" → "52:54:0:0:0:2"
	normalizedMAC := normalizeMACForDHCP(mac)

	// Parse all entries and return the one with the highest lease timestamp.
	// The file may contain multiple stale entries for the same MAC from previous boots.
	var bestIP string
	var bestLease uint64
	var currentIP string
	var currentLease uint64
	var macMatched bool

	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "ip_address=") {
			currentIP = strings.TrimPrefix(line, "ip_address=")
		}
		if strings.HasPrefix(line, "hw_address=") {
			hwAddr := strings.TrimPrefix(line, "hw_address=")
			macMatched = strings.HasSuffix(strings.ToLower(hwAddr), strings.ToLower(normalizedMAC))
		}
		if strings.HasPrefix(line, "lease=") {
			leaseHex := strings.TrimPrefix(line, "lease=")
			leaseHex = strings.TrimPrefix(leaseHex, "0x")
			val, _ := strconv.ParseUint(leaseHex, 16, 64)
			currentLease = val
		}
		if line == "}" {
			if macMatched && currentIP != "" && currentLease >= bestLease {
				bestIP = currentIP
				bestLease = currentLease
			}
			currentIP = ""
			currentLease = 0
			macMatched = false
		}
	}

	if bestIP != "" {
		return bestIP, nil
	}
	return "", fmt.Errorf("MAC %s not found in DHCP leases", mac)
}

// normalizeMACForDHCP converts a MAC like "52:54:00:00:00:02" to "52:54:0:0:0:2"
// to match the format used in macOS DHCP lease files.
func normalizeMACForDHCP(mac string) string {
	parts := strings.Split(mac, ":")
	for i, p := range parts {
		// Strip leading zeros: "00" → "0", "02" → "2"
		val := 0
		for _, c := range p {
			val = val*16 + hexVal(c)
		}
		parts[i] = fmt.Sprintf("%x", val)
	}
	return strings.Join(parts, ":")
}

func hexVal(c rune) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	default:
		return 0
	}
}

func (e *vzEngine) buildVZConfig(cfg VMConfig) (*vz.VirtualMachineConfiguration, error) {
	// EFI variable store — required for UEFI boot (persists NVRAM).
	nvramPath := cfg.LogPath // fallback
	if cfg.MachineDir != "" {
		nvramPath = cfg.MachineDir + "/efi-variable-store"
	}
	efiVarStore, err := vz.NewEFIVariableStore(nvramPath, vz.WithCreatingEFIVariableStore())
	if err != nil {
		return nil, fmt.Errorf("creating EFI variable store: %w", err)
	}

	// Boot loader — EFI for full UEFI disk boot.
	efi, err := vz.NewEFIBootLoader(vz.WithEFIVariableStore(efiVarStore))
	if err != nil {
		return nil, fmt.Errorf("creating EFI boot loader: %w", err)
	}

	vzCfg, err := vz.NewVirtualMachineConfiguration(efi, cfg.CPUs, cfg.MemoryMB*1024*1024)
	if err != nil {
		return nil, fmt.Errorf("creating VM configuration: %w", err)
	}

	// Storage — main disk.
	diskAttachment, err := vz.NewDiskImageStorageDeviceAttachmentWithCacheAndSync(
		cfg.DiskPath, false, vz.DiskImageCachingModeAutomatic, vz.DiskImageSynchronizationModeFsync,
	)
	if err != nil {
		return nil, fmt.Errorf("attaching disk %s: %w", cfg.DiskPath, err)
	}
	blockDev, err := vz.NewVirtioBlockDeviceConfiguration(diskAttachment)
	if err != nil {
		return nil, fmt.Errorf("creating block device: %w", err)
	}
	storageDevices := []vz.StorageDeviceConfiguration{blockDev}

	// Cloud-init CIDATA ISO.
	if cfg.CIDATAPath != "" {
		cidataAttachment, err := vz.NewDiskImageStorageDeviceAttachment(cfg.CIDATAPath, true)
		if err != nil {
			return nil, fmt.Errorf("attaching CIDATA ISO %s: %w", cfg.CIDATAPath, err)
		}
		cidataDev, err := vz.NewVirtioBlockDeviceConfiguration(cidataAttachment)
		if err != nil {
			return nil, fmt.Errorf("creating CIDATA block device: %w", err)
		}
		storageDevices = append(storageDevices, cidataDev)
	}
	vzCfg.SetStorageDevicesVirtualMachineConfiguration(storageDevices)

	// Network — NAT.
	natAttachment, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, fmt.Errorf("creating NAT attachment: %w", err)
	}
	netDev, err := vz.NewVirtioNetworkDeviceConfiguration(natAttachment)
	if err != nil {
		return nil, fmt.Errorf("creating network device: %w", err)
	}

	// Set deterministic MAC address for ARP-based IP discovery.
	if cfg.Network.MACAddress != "" {
		hwAddr, err := net.ParseMAC(cfg.Network.MACAddress)
		if err != nil {
			return nil, fmt.Errorf("parsing MAC address %q: %w", cfg.Network.MACAddress, err)
		}
		macAddr, err := vz.NewMACAddress(hwAddr)
		if err != nil {
			return nil, fmt.Errorf("creating VZ MAC address: %w", err)
		}
		netDev.SetMACAddress(macAddr)
		e.macAddr = cfg.Network.MACAddress
	}

	vzCfg.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{netDev})

	// Entropy — for /dev/random in guest.
	entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("creating entropy device: %w", err)
	}
	vzCfg.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})

	// Serial console — log guest output to file.
	if cfg.LogPath != "" {
		serialAttachment, err := vz.NewFileSerialPortAttachment(cfg.LogPath, false)
		if err != nil {
			return nil, fmt.Errorf("creating serial port attachment: %w", err)
		}
		serialPort, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serialAttachment)
		if err != nil {
			return nil, fmt.Errorf("creating serial port: %w", err)
		}
		vzCfg.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{serialPort})
	}

	// Shared folders via VirtioFS and optional Rosetta translation.
	var fsDirs []vz.DirectorySharingDeviceConfiguration
	for _, share := range cfg.Shares {
		sharedDir, err := vz.NewSharedDirectory(share.HostPath, share.ReadOnly)
		if err != nil {
			return nil, fmt.Errorf("creating shared directory for %s: %w", share.HostPath, err)
		}
		singleShare, err := vz.NewSingleDirectoryShare(sharedDir)
		if err != nil {
			return nil, fmt.Errorf("creating directory share: %w", err)
		}
		fsConfig, err := vz.NewVirtioFileSystemDeviceConfiguration(share.Tag)
		if err != nil {
			return nil, fmt.Errorf("creating VirtioFS device for tag %s: %w", share.Tag, err)
		}
		fsConfig.SetDirectoryShare(singleShare)
		fsDirs = append(fsDirs, fsConfig)
	}

	// Rosetta 2 — enable x86_64 binary translation on ARM hosts.
	if NeedsEmulation(cfg.Arch) && normalizeArch(cfg.Arch) == "amd64" {
		avail := vz.LinuxRosettaDirectoryShareAvailability()
		switch avail {
		case vz.LinuxRosettaAvailabilityNotInstalled:
			slog.Info("installing Rosetta for Linux binary translation")
			if err := vz.LinuxRosettaDirectoryShareInstallRosetta(); err != nil {
				return nil, fmt.Errorf("installing Rosetta: %w", err)
			}
			fallthrough
		case vz.LinuxRosettaAvailabilityInstalled:
			rosettaShare, err := vz.NewLinuxRosettaDirectoryShare()
			if err != nil {
				return nil, fmt.Errorf("creating Rosetta directory share: %w", err)
			}
			rosettaFS, err := vz.NewVirtioFileSystemDeviceConfiguration("rosetta")
			if err != nil {
				return nil, fmt.Errorf("creating Rosetta VirtioFS device: %w", err)
			}
			rosettaFS.SetDirectoryShare(rosettaShare)
			fsDirs = append(fsDirs, rosettaFS)
			slog.Info("Rosetta 2 translation enabled for amd64 guest on arm64 host")
		default:
			return nil, fmt.Errorf("Rosetta is not supported on this system (availability: %v); cannot run amd64 guest on arm64 host", avail)
		}
	}

	if len(fsDirs) > 0 {
		vzCfg.SetDirectorySharingDevicesVirtualMachineConfiguration(fsDirs)
	}

	// Platform — use GenericPlatformConfiguration for Linux guests.
	platform, err := vz.NewGenericPlatformConfiguration()
	if err != nil {
		return nil, fmt.Errorf("creating generic platform config: %w", err)
	}
	vzCfg.SetPlatformVirtualMachineConfiguration(platform)

	return vzCfg, nil
}

func (e *vzEngine) watchState() {
	if e.vm == nil {
		return
	}
	ch := e.vm.StateChangedNotify()
	for state := range ch {
		e.mu.Lock()
		switch state {
		case vz.VirtualMachineStateRunning:
			e.state = StateRunning
		case vz.VirtualMachineStateStopped:
			e.state = StateStopped
		case vz.VirtualMachineStateError:
			e.state = StateError
			slog.Error("VM entered error state", "name", e.cfg.Name)
		}
		e.mu.Unlock()
	}
}
