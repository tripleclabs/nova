package hypervisor

import (
	"runtime"
	"testing"
)

func TestNew(t *testing.T) {
	h, err := New()
	switch runtime.GOOS {
	case "darwin", "linux":
		if err != nil {
			t.Fatalf("New() on %s should succeed: %v", runtime.GOOS, err)
		}
		if h == nil {
			t.Fatalf("hypervisor should not be nil on %s", runtime.GOOS)
		}
	default:
		if err == nil {
			t.Fatalf("New() on %s should return error", runtime.GOOS)
		}
	}
}

func TestStateConstants(t *testing.T) {
	states := []State{StateStarting, StateRunning, StateStopped, StateError}
	for _, s := range states {
		if s == "" {
			t.Error("state constant should not be empty")
		}
	}
}

func TestVMConfig(t *testing.T) {
	cfg := VMConfig{
		Name:     "test",
		CPUs:     2,
		MemoryMB: 2048,
		DiskPath: "/tmp/test.qcow2",
		Network: NetworkConfig{
			PortForwards: []PortForward{
				{HostPort: 8080, GuestPort: 80, Protocol: "tcp"},
			},
		},
		Shares: []ShareConfig{
			{Tag: "workspace", HostPath: "/tmp/share", ReadOnly: false},
		},
	}
	if cfg.CPUs != 2 {
		t.Errorf("CPUs = %d, want 2", cfg.CPUs)
	}
	if len(cfg.Network.PortForwards) != 1 {
		t.Errorf("PortForwards len = %d, want 1", len(cfg.Network.PortForwards))
	}
	if len(cfg.Shares) != 1 {
		t.Errorf("Shares len = %d, want 1", len(cfg.Shares))
	}
}
