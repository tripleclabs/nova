package cloudinit

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kdomanski/iso9660"
)

// BuildCIDATADir writes cloud-init NoCloud files to a directory for use with
// VirtioFS. This is used on macOS where Apple Virtualization.framework doesn't
// reliably expose ISO block devices to cloud-init. The directory should be
// shared with the guest via VirtioFS with the tag "cidata".
func BuildCIDATADir(dirPath string, hostname string, userData []byte, networkConfig []byte) error {
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		return fmt.Errorf("creating cidata dir: %w", err)
	}

	metaData := fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", hostname, hostname)
	if err := os.WriteFile(filepath.Join(dirPath, "meta-data"), []byte(metaData), 0644); err != nil {
		return fmt.Errorf("writing meta-data: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dirPath, "user-data"), userData, 0644); err != nil {
		return fmt.Errorf("writing user-data: %w", err)
	}

	if networkConfig != nil {
		if err := os.WriteFile(filepath.Join(dirPath, "network-config"), networkConfig, 0644); err != nil {
			return fmt.Errorf("writing network-config: %w", err)
		}
	}

	return nil
}

// BuildCIDATAISO creates a NoCloud-compatible ISO containing meta-data,
// user-data, and optional network-config files. The ISO uses the volume label
// "CIDATA" which cloud-init recognizes as a NoCloud data source.
// Pass nil for networkConfig to omit it (single-VM mode).
func BuildCIDATAISO(outputPath string, hostname string, userData []byte, networkConfig []byte) error {
	writer, err := iso9660.NewWriter()
	if err != nil {
		return fmt.Errorf("creating ISO writer: %w", err)
	}
	defer writer.Cleanup()

	// meta-data: minimal YAML with instance-id and local-hostname.
	metaData := fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", hostname, hostname)
	if err := writer.AddFile(bytes.NewReader([]byte(metaData)), "meta-data"); err != nil {
		return fmt.Errorf("adding meta-data to ISO: %w", err)
	}

	// user-data: the merged cloud-config.
	if err := writer.AddFile(bytes.NewReader(userData), "user-data"); err != nil {
		return fmt.Errorf("adding user-data to ISO: %w", err)
	}

	// network-config: cloud-init network-config v2 for static IP assignment.
	if networkConfig != nil {
		if err := writer.AddFile(bytes.NewReader(networkConfig), "network-config"); err != nil {
			return fmt.Errorf("adding network-config to ISO: %w", err)
		}
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("creating ISO file: %w", err)
	}
	defer f.Close()

	if err := writer.WriteTo(f, "CIDATA"); err != nil {
		return fmt.Errorf("writing ISO: %w", err)
	}

	return nil
}
