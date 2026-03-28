package image

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDetectFormat_QCOW2(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.qcow2")

	// Create a minimal qcow2 file with the magic header.
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// QFI\xfb magic followed by zeroes.
	header := append([]byte{0x51, 0x46, 0x49, 0xfb}, make([]byte, 508)...)
	f.Write(header)
	f.Close()

	format, err := DetectFormat(path)
	if err != nil {
		t.Fatalf("DetectFormat: %v", err)
	}
	if format != FormatQCOW2 {
		t.Errorf("format = %q, want qcow2", format)
	}
}

func TestDetectFormat_Raw(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.raw")

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// Raw images don't have qcow2 magic.
	f.Write(make([]byte, 512))
	f.Close()

	format, err := DetectFormat(path)
	if err != nil {
		t.Fatalf("DetectFormat: %v", err)
	}
	if format != FormatRaw {
		t.Errorf("format = %q, want raw", format)
	}
}

func TestCreateOverlay(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not found, skipping overlay test")
	}

	dir := t.TempDir()

	// Create a real qcow2 base image using qemu-img.
	basePath := filepath.Join(dir, "base.qcow2")
	cmd := exec.Command("qemu-img", "create", "-f", "qcow2", basePath, "64M")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("creating base image: %s: %v", out, err)
	}

	machineDir := filepath.Join(dir, "machines", "test-vm")
	overlayPath, err := CreateOverlay(basePath, machineDir)
	if err != nil {
		t.Fatalf("CreateOverlay: %v", err)
	}

	// Verify the overlay exists.
	info, err := os.Stat(overlayPath)
	if err != nil {
		t.Fatalf("overlay not found: %v", err)
	}
	if info.Size() == 0 {
		t.Error("overlay file is empty")
	}

	// Verify the overlay is qcow2.
	format, err := DetectFormat(overlayPath)
	if err != nil {
		t.Fatalf("DetectFormat overlay: %v", err)
	}
	if format != FormatQCOW2 {
		t.Errorf("overlay format = %q, want qcow2", format)
	}

	// Verify backing file with qemu-img info.
	infoCmd := exec.Command("qemu-img", "info", overlayPath)
	out, err := infoCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("qemu-img info: %s: %v", out, err)
	}
	if !containsBytes(out, []byte("backing file")) {
		t.Error("overlay should reference a backing file")
	}
}

func TestCreateOverlay_RawBase(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not found, skipping overlay test")
	}

	dir := t.TempDir()

	// Create a raw base image.
	basePath := filepath.Join(dir, "base.raw")
	cmd := exec.Command("qemu-img", "create", "-f", "raw", basePath, "64M")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("creating raw base: %s: %v", out, err)
	}

	machineDir := filepath.Join(dir, "machines", "test-vm-raw")
	overlayPath, err := CreateOverlay(basePath, machineDir)
	if err != nil {
		t.Fatalf("CreateOverlay with raw base: %v", err)
	}

	info, err := os.Stat(overlayPath)
	if err != nil {
		t.Fatalf("overlay not found: %v", err)
	}
	if info.Size() == 0 {
		t.Error("overlay file is empty")
	}
}

func containsBytes(haystack, needle []byte) bool {
	for i := 0; i <= len(haystack)-len(needle); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestDetectFormat_FileTooSmall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tiny.img")

	// Create a file with fewer than 4 bytes — should be detected as raw
	// (partial read fills buffer with zeros, which don't match qcow2 magic).
	os.WriteFile(path, []byte{0x01, 0x02}, 0644)

	format, err := DetectFormat(path)
	if err != nil {
		t.Fatalf("DetectFormat: %v", err)
	}
	if format != FormatRaw {
		t.Errorf("format = %q, want raw for tiny file", format)
	}
}

func TestDetectFormat_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.img")

	os.WriteFile(path, []byte{}, 0644)

	_, err := DetectFormat(path)
	if err == nil {
		t.Error("expected error for empty file")
	}
}

func TestDetectFormat_FileNotExist(t *testing.T) {
	_, err := DetectFormat("/nonexistent/path/disk.img")
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestDetectFormat_ExactlyFourBytes(t *testing.T) {
	dir := t.TempDir()

	// Exactly 4 bytes, not qcow2 magic -- should be raw.
	path := filepath.Join(dir, "four.img")
	os.WriteFile(path, []byte{0x00, 0x00, 0x00, 0x00}, 0644)

	format, err := DetectFormat(path)
	if err != nil {
		t.Fatalf("DetectFormat: %v", err)
	}
	if format != FormatRaw {
		t.Errorf("format = %q, want raw", format)
	}
}

func TestDetectFormat_PartialQCOW2Magic(t *testing.T) {
	dir := t.TempDir()

	// First 3 bytes match but 4th differs.
	path := filepath.Join(dir, "partial.img")
	os.WriteFile(path, []byte{0x51, 0x46, 0x49, 0x00, 0x00}, 0644)

	format, err := DetectFormat(path)
	if err != nil {
		t.Fatalf("DetectFormat: %v", err)
	}
	if format != FormatRaw {
		t.Errorf("format = %q, want raw for partial qcow2 magic", format)
	}
}

func TestCreateOverlay_NonExistentBaseImage(t *testing.T) {
	dir := t.TempDir()
	machineDir := filepath.Join(dir, "machines", "test-vm")

	_, err := CreateOverlay("/nonexistent/base.qcow2", machineDir)
	if err == nil {
		t.Error("expected error for non-existent base image")
	}
}

func TestDiskFormatConstants(t *testing.T) {
	if FormatQCOW2 != "qcow2" {
		t.Errorf("FormatQCOW2 = %q, want %q", FormatQCOW2, "qcow2")
	}
	if FormatRaw != "raw" {
		t.Errorf("FormatRaw = %q, want %q", FormatRaw, "raw")
	}
	if FormatUnknown != "unknown" {
		t.Errorf("FormatUnknown = %q, want %q", FormatUnknown, "unknown")
	}
}
