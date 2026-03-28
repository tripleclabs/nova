package cloudinit

import (
	"bytes"
	"fmt"
	"os"

	"github.com/kdomanski/iso9660"
)

// BuildCIDATAISO creates a NoCloud-compatible ISO containing meta-data and
// user-data files. The ISO uses the volume label "CIDATA" which cloud-init
// recognizes as a NoCloud data source.
func BuildCIDATAISO(outputPath string, hostname string, userData []byte) error {
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
