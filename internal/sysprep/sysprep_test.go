package sysprep

import (
	"strings"
	"testing"
)

func TestParseOSRelease_Ubuntu(t *testing.T) {
	content := `PRETTY_NAME="Ubuntu 24.04.3 LTS"
NAME="Ubuntu"
VERSION_ID="24.04"
ID=ubuntu
ID_LIKE=debian
`
	got := ParseOSRelease(content)
	if got != OSUbuntu {
		t.Errorf("ParseOSRelease = %q, want %q", got, OSUbuntu)
	}
}

func TestParseOSRelease_Alpine(t *testing.T) {
	content := `NAME="Alpine Linux"
ID=alpine
VERSION_ID=3.21.0
PRETTY_NAME="Alpine Linux v3.21"
`
	got := ParseOSRelease(content)
	if got != OSAlpine {
		t.Errorf("ParseOSRelease = %q, want %q", got, OSAlpine)
	}
}

func TestParseOSRelease_Debian(t *testing.T) {
	content := `PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
NAME="Debian GNU/Linux"
VERSION_ID="12"
ID=debian
`
	got := ParseOSRelease(content)
	if got != OSDebian {
		t.Errorf("ParseOSRelease = %q, want %q", got, OSDebian)
	}
}

func TestParseOSRelease_Fedora(t *testing.T) {
	content := `NAME="Fedora Linux"
VERSION="39 (Server Edition)"
ID=fedora
`
	got := ParseOSRelease(content)
	if got != OSFedora {
		t.Errorf("ParseOSRelease = %q, want %q", got, OSFedora)
	}
}

func TestParseOSRelease_QuotedID(t *testing.T) {
	content := `ID="ubuntu"
`
	got := ParseOSRelease(content)
	if got != OSUbuntu {
		t.Errorf("ParseOSRelease = %q, want %q (quoted ID)", got, OSUbuntu)
	}
}

func TestParseOSRelease_Unknown(t *testing.T) {
	content := `ID=nixos
`
	got := ParseOSRelease(content)
	if got != OSGeneric {
		t.Errorf("ParseOSRelease = %q, want %q for unknown distro", got, OSGeneric)
	}
}

func TestParseOSRelease_Empty(t *testing.T) {
	got := ParseOSRelease("")
	if got != OSGeneric {
		t.Errorf("ParseOSRelease empty = %q, want %q", got, OSGeneric)
	}
}

func TestBuildProfile_Ubuntu(t *testing.T) {
	steps := buildProfile(OSUbuntu, Options{})
	assertHasStep(t, steps, "Truncate machine-id")
	assertHasStep(t, steps, "Reset cloud-init")
	assertHasStep(t, steps, "Clean apt cache")
	assertHasStep(t, steps, "Flush and vacuum journald")
	assertHasStep(t, steps, "Remove SSH host keys")
	assertHasStep(t, steps, "Remove Nova authorized keys")
	assertNoStep(t, steps, "Zero free space")
}

func TestBuildProfile_Alpine(t *testing.T) {
	steps := buildProfile(OSAlpine, Options{})
	assertHasStep(t, steps, "Reset machine-id")
	assertHasStep(t, steps, "Clean apk cache")
	assertHasStep(t, steps, "Clear ash history")
	assertNoStep(t, steps, "Flush and vacuum journald") // No journald on Alpine.
	assertNoStep(t, steps, "Clean apt cache")
}

func TestBuildProfile_AlpineDoasCleanup(t *testing.T) {
	steps := buildProfile(OSAlpine, Options{})
	var found bool
	for _, s := range steps {
		if s.Name == "Remove Nova sudoers/doas config" {
			if !strings.Contains(s.Command, "doas.d") {
				t.Errorf("Alpine sudoers cleanup should include doas.d, got: %s", s.Command)
			}
			found = true
		}
	}
	if !found {
		t.Error("expected Nova sudoers/doas cleanup step")
	}
}

func TestBuildProfile_Generic(t *testing.T) {
	steps := buildProfile(OSGeneric, Options{})
	assertHasStep(t, steps, "Truncate machine-id")
	assertHasStep(t, steps, "Reset cloud-init (if present)")
	assertHasStep(t, steps, "Clear shell history") // Generic clears both bash and ash.
	assertNoStep(t, steps, "Clean apt cache")       // Generic doesn't know the package manager.
}

func TestBuildProfile_ZeroFreeSpace(t *testing.T) {
	steps := buildProfile(OSUbuntu, Options{ZeroFreeSpace: true})
	assertHasStep(t, steps, "Zero free space (this may take a while)")
}

func TestBuildProfile_RemoveNovaUser(t *testing.T) {
	steps := buildProfile(OSUbuntu, Options{RemoveNovaUser: true})
	assertHasStep(t, steps, "Remove internal nova user")
}

func TestBuildProfile_NoRemoveNovaUserByDefault(t *testing.T) {
	steps := buildProfile(OSUbuntu, Options{})
	assertNoStep(t, steps, "Remove internal nova user")
}

func TestBuildProfile_NoZeroByDefault(t *testing.T) {
	steps := buildProfile(OSUbuntu, Options{})
	assertNoStep(t, steps, "Zero free space")
}

func TestBuildProfile_NovaNetworkCleanup(t *testing.T) {
	steps := buildProfile(OSUbuntu, Options{})
	assertHasStep(t, steps, "Remove Nova network config")
	assertHasStep(t, steps, "Remove cloud-init NoCloud seed")
	assertHasStep(t, steps, "Flush ARP cache")
}

func TestBuildProfile_AlpineNetworkCleanup(t *testing.T) {
	steps := buildProfile(OSAlpine, Options{})
	// Alpine should clean /etc/network/interfaces.d, not netplan.
	var found bool
	for _, s := range steps {
		if s.Name == "Remove Nova network config" {
			if !strings.Contains(s.Command, "interfaces.d") {
				t.Errorf("Alpine network cleanup should target interfaces.d, got: %s", s.Command)
			}
			found = true
		}
	}
	if !found {
		t.Error("expected Nova network config cleanup step for Alpine")
	}
}

func TestBuildProfile_UbuntuNetworkCleanup(t *testing.T) {
	steps := buildProfile(OSUbuntu, Options{})
	var found bool
	for _, s := range steps {
		if s.Name == "Remove Nova network config" {
			if !strings.Contains(s.Command, "netplan") {
				t.Errorf("Ubuntu network cleanup should target netplan, got: %s", s.Command)
			}
			found = true
		}
	}
	if !found {
		t.Error("expected Nova network config cleanup step for Ubuntu")
	}
}

func TestBuildProfile_UniversalStepsFirst(t *testing.T) {
	steps := buildProfile(OSUbuntu, Options{})
	// Universal steps (SSH keys, Nova keys, sudoers, tmp) should come before OS-specific.
	if len(steps) < 5 {
		t.Fatalf("expected at least 5 steps, got %d", len(steps))
	}
	if steps[0].Name != "Remove SSH host keys" {
		t.Errorf("first step = %q, want 'Remove SSH host keys'", steps[0].Name)
	}
}

func TestBuildProfile_Fedora(t *testing.T) {
	steps := buildProfile(OSFedora, Options{})
	assertHasStep(t, steps, "Clean dnf cache")
	assertHasStep(t, steps, "Flush and vacuum journald")
	assertNoStep(t, steps, "Clean apt cache")
}

func assertHasStep(t *testing.T, steps []Step, name string) {
	t.Helper()
	for _, s := range steps {
		if strings.Contains(s.Name, name) {
			return
		}
	}
	t.Errorf("expected step containing %q", name)
}

func assertNoStep(t *testing.T, steps []Step, substr string) {
	t.Helper()
	for _, s := range steps {
		if strings.Contains(s.Name, substr) {
			t.Errorf("unexpected step containing %q: %q", substr, s.Name)
		}
	}
}
