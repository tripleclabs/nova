package image

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DiskFormat represents a VM disk image format.
type DiskFormat string

const (
	FormatQCOW2   DiskFormat = "qcow2"
	FormatRaw     DiskFormat = "raw"
	FormatUnknown DiskFormat = "unknown"
)

// qcow2 magic bytes: "QFI\xfb"
var qcow2Magic = []byte{0x51, 0x46, 0x49, 0xfb}

// DetectFormat inspects the first bytes of a disk image to determine its format.
func DetectFormat(path string) (DiskFormat, error) {
	f, err := os.Open(path)
	if err != nil {
		return FormatUnknown, err
	}
	defer f.Close()

	header := make([]byte, 4)
	if _, err := f.Read(header); err != nil {
		return FormatUnknown, err
	}

	if bytes.Equal(header, qcow2Magic) {
		return FormatQCOW2, nil
	}
	return FormatRaw, nil
}

// CreateOverlay creates a Copy-on-Write overlay disk backed by the given base image.
// For qcow2 base images, it uses qemu-img create -f qcow2 -b.
// For raw images, it creates a qcow2 overlay referencing the raw backing file.
// Returns the path to the new overlay disk.
func CreateOverlay(baseImage, machineDir string) (string, error) {
	overlayPath := filepath.Join(machineDir, "disk.qcow2")

	if err := os.MkdirAll(machineDir, 0755); err != nil {
		return "", fmt.Errorf("creating machine dir: %w", err)
	}

	// Resolve absolute path so the backing file reference is stable.
	absBase, err := filepath.Abs(baseImage)
	if err != nil {
		return "", fmt.Errorf("resolving base image path: %w", err)
	}

	format, err := DetectFormat(absBase)
	if err != nil {
		return "", fmt.Errorf("detecting base image format: %w", err)
	}

	backingFmt := "qcow2"
	if format == FormatRaw {
		backingFmt = "raw"
	}

	// qemu-img create -f qcow2 -b <base> -F <backing_fmt> <overlay>
	cmd := exec.Command("qemu-img", "create",
		"-f", "qcow2",
		"-b", absBase,
		"-F", backingFmt,
		overlayPath,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("qemu-img create: %s: %w", strings.TrimSpace(stderr.String()), err)
	}

	return overlayPath, nil
}

// ConvertToRaw converts a qcow2 disk image to raw format. Required for Apple
// Virtualization.framework which only supports raw disk images. The source
// qcow2 is removed after successful conversion.
func ConvertToRaw(qcow2Path string) (string, error) {
	rawPath := strings.TrimSuffix(qcow2Path, ".qcow2") + ".raw"

	cmd := exec.Command("qemu-img", "convert",
		"-f", "qcow2",
		"-O", "raw",
		qcow2Path,
		rawPath,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("qemu-img convert to raw: %s: %w", strings.TrimSpace(stderr.String()), err)
	}

	// Remove the qcow2 source to save space.
	os.Remove(qcow2Path)

	return rawPath, nil
}
