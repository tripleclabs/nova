// Package hypervisor defines the abstraction layer for VM lifecycle management.
package hypervisor

import (
	"context"
	"fmt"
	"os"
	"runtime"
)

// State represents the lifecycle state of a virtual machine.
type State string

const (
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopped  State = "stopped"
	StateError    State = "error"
)

// VMConfig holds all parameters needed to boot a VM.
type VMConfig struct {
	Name       string
	Arch       string        // Guest architecture: "amd64", "arm64", or "host" (default).
	CPUs       uint
	MemoryMB   uint64
	DiskPath   string        // Path to the overlay disk (raw on macOS, qcow2 on Linux).
	CIDATAPath string        // Path to cloud-init ISO (optional).
	LogPath    string        // Path for serial console log output.
	MachineDir string        // Path to the machine state directory (for EFI NVRAM, etc).
	PIDPath    string        // Path to write the hypervisor process PID (optional).
	Network    NetworkConfig
	Shares     []ShareConfig
}

// NetworkConfig describes the VM's network setup.
type NetworkConfig struct {
	PortForwards []PortForward
	MACAddress   string   // Deterministic MAC for IP discovery (e.g., "52:54:00:00:00:02").
	StaticIP     string   // Static IP to assign in guest for multi-node (e.g., "10.0.0.2").
	Subnet       string   // CIDR for the multi-node LAN (e.g., "10.0.0.0/24").
	MultiNode    bool     // When true, use multi-node networking (mcast on Linux, shared NAT on macOS).
	SwitchFile   *os.File // QEMU-side socketpair fd; nil means use legacy SLIRP/mcast.
}

// PortForward maps a host port to a guest port.
type PortForward struct {
	HostPort  int
	GuestPort int
	Protocol  string // "tcp" or "udp"
}

// ShareConfig describes a host directory shared with the guest.
type ShareConfig struct {
	Tag       string // Mount tag inside the guest.
	HostPath  string
	ReadOnly  bool
}

// Hypervisor is the interface that all VM backend engines must implement.
type Hypervisor interface {
	// Start boots a VM with the given configuration. It blocks until the VM
	// is running or an error occurs.
	Start(ctx context.Context, cfg VMConfig) error

	// Stop requests a graceful shutdown of the VM.
	Stop(ctx context.Context) error

	// ForceKill immediately terminates the VM.
	ForceKill() error

	// GetState returns the current lifecycle state.
	GetState() State

	// GuestIP returns the IP address of the guest, if known.
	GuestIP() (string, error)
}

// NewAttached reconnects to an already-running VM via its QMP socket.
// Only supported on Linux (QEMU). Returns an error on unsupported platforms.
func NewAttached(ctx context.Context, cfg VMConfig) (Hypervisor, error) {
	switch runtime.GOOS {
	case "linux":
		return attachQEMUEngine(ctx, cfg)
	default:
		return nil, fmt.Errorf("daemon reattach not supported on %s", runtime.GOOS)
	}
}

// New returns a Hypervisor implementation appropriate for the current platform.
func New() (Hypervisor, error) {
	switch runtime.GOOS {
	case "darwin":
		return newVZEngine()
	case "linux":
		return newQEMUEngine()
	case "windows":
		return newHyperVEngine()
	default:
		return nil, fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}
