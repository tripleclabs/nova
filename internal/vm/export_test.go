package vm

import (
	"os"
	"strings"
	"testing"

	"github.com/tripleclabs/nova/internal/state"
)

// --- ParseExportFormat ---

func TestParseExportFormat_Valid(t *testing.T) {
	tests := []struct {
		input string
		want  ExportFormat
	}{
		{"qcow2", FormatQCOW2},
		{"raw", FormatRaw},
		{"vmdk", FormatVMDK},
		{"vhdx", FormatVHDX},
		{"QCOW2", FormatQCOW2}, // case insensitive
		{"Raw", FormatRaw},
		{"VMDK", FormatVMDK},
	}
	for _, tt := range tests {
		got, err := ParseExportFormat(tt.input)
		if err != nil {
			t.Errorf("ParseExportFormat(%q): unexpected error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseExportFormat(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseExportFormat_OVA(t *testing.T) {
	got, err := ParseExportFormat("ova")
	if err != nil {
		t.Fatalf("ParseExportFormat(ova): %v", err)
	}
	if got != FormatOVA {
		t.Errorf("got %q, want ova", got)
	}
}

func TestExportExtension_OVA(t *testing.T) {
	if FormatOVA.ExportExtension() != ".ova" {
		t.Errorf("OVA extension = %q, want .ova", FormatOVA.ExportExtension())
	}
}

func TestGenerateOVF(t *testing.T) {
	ovf, err := generateOVF("test-vm", "test-vm.vmdk", 4, 8192, 1073741824)
	if err != nil {
		t.Fatalf("generateOVF: %v", err)
	}
	s := string(ovf)
	if !strings.Contains(s, "test-vm") {
		t.Error("OVF should contain VM name")
	}
	if !strings.Contains(s, "test-vm.vmdk") {
		t.Error("OVF should reference VMDK filename")
	}
	if !strings.Contains(s, "<rasd:VirtualQuantity>4</rasd:VirtualQuantity>") {
		t.Error("OVF should contain CPU count")
	}
	if !strings.Contains(s, "<rasd:VirtualQuantity>8192</rasd:VirtualQuantity>") {
		t.Error("OVF should contain memory")
	}
	if !strings.Contains(s, "vmx-13") {
		t.Error("OVF should specify virtual hardware version")
	}
	if !strings.Contains(s, "VmxNet3") {
		t.Error("OVF should specify VmxNet3 network adapter")
	}
}

func TestParseExportFormat_Invalid(t *testing.T) {
	invalids := []string{"vdi", "iso", "", "tar.gz"}
	for _, s := range invalids {
		_, err := ParseExportFormat(s)
		if err == nil {
			t.Errorf("ParseExportFormat(%q) should return error", s)
		}
	}
}

// --- ExportExtension ---

func TestExportExtension(t *testing.T) {
	tests := []struct {
		format ExportFormat
		want   string
	}{
		{FormatQCOW2, ".qcow2"},
		{FormatRaw, ".img"},
		{FormatVMDK, ".vmdk"},
		{FormatVHDX, ".vhdx"},
	}
	for _, tt := range tests {
		got := tt.format.ExportExtension()
		if got != tt.want {
			t.Errorf("%s.ExportExtension() = %q, want %q", tt.format, got, tt.want)
		}
	}
}

// --- Export error cases ---

func TestExport_VMNotFound(t *testing.T) {
	o := newTestOrchestrator(t)

	_, err := o.Export(t.Context(), "nonexistent", ExportOptions{Format: FormatQCOW2})
	if err == nil {
		t.Fatal("expected error for non-existent VM")
	}
}

func TestExport_VMNotRunning(t *testing.T) {
	o := newTestOrchestrator(t)
	createMachine(t, o, "stopped", state.StateStopped)

	_, err := o.Export(t.Context(), "stopped", ExportOptions{Format: FormatQCOW2})
	if err == nil {
		t.Fatal("expected error for stopped VM")
	}
}

func TestExport_DefaultName(t *testing.T) {
	o := newTestOrchestrator(t)

	// Empty name should resolve to "default" and return not-found.
	_, err := o.Export(t.Context(), "", ExportOptions{Format: FormatQCOW2, HasUser: true})
	if err == nil {
		t.Fatal("expected error")
	}
	// Verify it tried "default" not "".
	if got := err.Error(); got != `VM "default" not found` {
		t.Errorf("error = %q, want VM \"default\" not found", got)
	}
}

func TestExport_NoUserBlockAllowed(t *testing.T) {
	o := newTestOrchestrator(t)
	createMachine(t, o, "myvm", state.StateRunning)

	// Without a user block, export is allowed (with a warning). Sysprep still
	// removes the internal nova user, so nothing leaks — the image is valid
	// for reuse by nova, which recreates users via cloud-init on next boot.
	_, err := o.Export(t.Context(), "myvm", ExportOptions{
		Format:  FormatQCOW2,
		HasUser: false,
	})
	// Should fail for a reason OTHER than user block.
	if err != nil && strings.Contains(err.Error(), "user block") {
		t.Errorf("export without user block should proceed, got: %v", err)
	}
}

func TestExport_NoUserBlockAllowedWithNoClean(t *testing.T) {
	o := newTestOrchestrator(t)
	createMachine(t, o, "myvm", state.StateRunning)

	// With --no-clean, export should not require a user block.
	// It will still fail (no disk etc) but not because of the user gate.
	_, err := o.Export(t.Context(), "myvm", ExportOptions{
		Format:  FormatQCOW2,
		NoClean: true,
		HasUser: false,
	})
	// Should fail for a reason OTHER than user block.
	if err != nil && strings.Contains(err.Error(), "user block") {
		t.Errorf("--no-clean should bypass user block gate, got: %v", err)
	}
}

func TestExport_WithUserBlockProceeds(t *testing.T) {
	o := newTestOrchestrator(t)
	createMachine(t, o, "myvm", state.StateRunning)

	// With HasUser=true, it should pass the gate and fail for a different reason.
	_, err := o.Export(t.Context(), "myvm", ExportOptions{
		Format:  FormatQCOW2,
		HasUser: true,
	})
	// Should fail for a reason OTHER than user block (e.g., no hypervisor handle).
	if err != nil && strings.Contains(err.Error(), "user block") {
		t.Errorf("HasUser=true should pass user gate, got: %v", err)
	}
}

func TestExport_OutputAlreadyExists(t *testing.T) {
	o := newTestOrchestrator(t)
	createMachine(t, o, "myvm", state.StateRunning)

	// Create a file at the output path.
	tmpFile := t.TempDir() + "/existing.qcow2"
	if err := writeFile(tmpFile, []byte("existing")); err != nil {
		t.Fatal(err)
	}

	_, err := o.Export(t.Context(), "myvm", ExportOptions{
		Format:     FormatQCOW2,
		OutputPath: tmpFile,
		HasUser:    true,
	})
	if err == nil {
		t.Fatal("expected error for existing output file")
	}
}

// --- humanSize ---

func TestHumanSize(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1.0 KiB"},
		{1048576, "1.0 MiB"},
		{1073741824, "1.0 GiB"},
		{2 * 1073741824, "2.0 GiB"},
	}
	for _, tt := range tests {
		got := humanSize(tt.input)
		if got != tt.want {
			t.Errorf("humanSize(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0644)
}
