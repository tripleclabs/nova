package hypervisor

import (
	"runtime"
	"testing"
)

func TestHostArch(t *testing.T) {
	arch := HostArch()
	if arch == "" {
		t.Fatal("HostArch should not be empty")
	}
	// On this machine it should match runtime.GOARCH normalized.
	switch runtime.GOARCH {
	case "amd64":
		if arch != "amd64" {
			t.Errorf("HostArch = %q, want amd64", arch)
		}
	case "arm64":
		if arch != "arm64" {
			t.Errorf("HostArch = %q, want arm64", arch)
		}
	}
}

func TestNormalizeArch(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"amd64", "amd64"},
		{"x86_64", "amd64"},
		{"arm64", "arm64"},
		{"aarch64", "arm64"},
		{"host", ""},
		{"", ""},
		{"riscv64", "riscv64"},
	}
	for _, tt := range tests {
		got := normalizeArch(tt.input)
		if got != tt.want {
			t.Errorf("normalizeArch(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNeedsEmulation(t *testing.T) {
	host := HostArch()

	// Same arch or "host" should not need emulation.
	if NeedsEmulation(host) {
		t.Errorf("NeedsEmulation(%q) should be false on matching host", host)
	}
	if NeedsEmulation("host") {
		t.Error("NeedsEmulation(host) should be false")
	}
	if NeedsEmulation("") {
		t.Error("NeedsEmulation(\"\") should be false")
	}

	// Different arch should need emulation.
	var foreign string
	if host == "arm64" {
		foreign = "amd64"
	} else {
		foreign = "arm64"
	}
	if !NeedsEmulation(foreign) {
		t.Errorf("NeedsEmulation(%q) should be true on %q host", foreign, host)
	}
}

func TestNeedsEmulation_Aliases(t *testing.T) {
	host := HostArch()
	// x86_64 is an alias for amd64.
	if host == "amd64" {
		if NeedsEmulation("x86_64") {
			t.Error("x86_64 should not need emulation on amd64 host")
		}
	}
	// aarch64 is an alias for arm64.
	if host == "arm64" {
		if NeedsEmulation("aarch64") {
			t.Error("aarch64 should not need emulation on arm64 host")
		}
	}
}
