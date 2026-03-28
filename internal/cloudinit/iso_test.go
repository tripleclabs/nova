package cloudinit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildCIDATAISO(t *testing.T) {
	dir := t.TempDir()
	isoPath := filepath.Join(dir, "cidata.iso")

	userData := []byte("#cloud-config\nhostname: test-vm\n")
	if err := BuildCIDATAISO(isoPath, "test-vm", userData); err != nil {
		t.Fatalf("BuildCIDATAISO: %v", err)
	}

	info, err := os.Stat(isoPath)
	if err != nil {
		t.Fatalf("ISO not created: %v", err)
	}
	if info.Size() == 0 {
		t.Error("ISO file is empty")
	}
	// A valid ISO9660 image should be at least 32KB (16 system sectors + volume descriptor).
	if info.Size() < 32*1024 {
		t.Errorf("ISO too small (%d bytes), likely invalid", info.Size())
	}
}

func TestBuildCIDATAISO_ContainsFiles(t *testing.T) {
	dir := t.TempDir()
	isoPath := filepath.Join(dir, "cidata.iso")

	userData := []byte("#cloud-config\nhostname: verify-vm\n")
	if err := BuildCIDATAISO(isoPath, "verify-vm", userData); err != nil {
		t.Fatal(err)
	}

	// Read the ISO back and verify it contains meta-data and user-data.
	f, err := os.Open(isoPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Read raw bytes to check for our content strings.
	data, err := os.ReadFile(isoPath)
	if err != nil {
		t.Fatal(err)
	}

	if !containsString(data, "instance-id: verify-vm") {
		t.Error("ISO should contain meta-data with instance-id")
	}
	if !containsString(data, "#cloud-config") {
		t.Error("ISO should contain user-data with #cloud-config")
	}
}

func containsString(data []byte, s string) bool {
	target := []byte(s)
	for i := 0; i <= len(data)-len(target); i++ {
		match := true
		for j := range target {
			if data[i+j] != target[j] {
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
