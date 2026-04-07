package cloudinit

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildCIDATAISO(t *testing.T) {
	dir := t.TempDir()
	isoPath := filepath.Join(dir, "cidata.iso")

	userData := []byte("#cloud-config\nhostname: test-vm\n")
	if err := BuildCIDATAISO(isoPath, "test-vm", userData, nil); err != nil {
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
	if err := BuildCIDATAISO(isoPath, "verify-vm", userData, nil); err != nil {
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

	if !containsString(data, "instance-id: verify-vm-") {
		t.Error("ISO should contain meta-data with instance-id prefixed by hostname")
	}
	if !containsString(data, "local-hostname: verify-vm") {
		t.Error("ISO should contain meta-data with local-hostname")
	}
	if !containsString(data, "#cloud-config") {
		t.Error("ISO should contain user-data with #cloud-config")
	}
}

func TestBuildCIDATAISO_EmptyHostname(t *testing.T) {
	dir := t.TempDir()
	isoPath := filepath.Join(dir, "cidata.iso")

	userData := []byte("#cloud-config\nhostname: \"\"\n")
	if err := BuildCIDATAISO(isoPath, "", userData, nil); err != nil {
		t.Fatalf("BuildCIDATAISO with empty hostname: %v", err)
	}

	info, err := os.Stat(isoPath)
	if err != nil {
		t.Fatalf("ISO not created: %v", err)
	}
	if info.Size() == 0 {
		t.Error("ISO file is empty")
	}

	data, err := os.ReadFile(isoPath)
	if err != nil {
		t.Fatal(err)
	}
	// meta-data should still be present with empty hostname fields.
	if !containsString(data, "instance-id:") {
		t.Error("ISO should contain meta-data with instance-id field")
	}
}

func TestBuildCIDATAISO_LargeUserData(t *testing.T) {
	dir := t.TempDir()
	isoPath := filepath.Join(dir, "cidata.iso")

	// Build user data with many packages and runcmd entries.
	var large []byte
	large = append(large, []byte("#cloud-config\npackages:\n")...)
	for i := 0; i < 500; i++ {
		line := fmt.Sprintf("  - package-%d\n", i)
		large = append(large, []byte(line)...)
	}
	large = append(large, []byte("runcmd:\n")...)
	for i := 0; i < 500; i++ {
		line := fmt.Sprintf("  - echo command-%d\n", i)
		large = append(large, []byte(line)...)
	}

	if err := BuildCIDATAISO(isoPath, "large-vm", large, nil); err != nil {
		t.Fatalf("BuildCIDATAISO with large user data: %v", err)
	}

	info, err := os.Stat(isoPath)
	if err != nil {
		t.Fatalf("ISO not created: %v", err)
	}
	if info.Size() < 32*1024 {
		t.Errorf("ISO too small (%d bytes)", info.Size())
	}

	data, err := os.ReadFile(isoPath)
	if err != nil {
		t.Fatal(err)
	}
	// Verify some content from the large data set is present.
	if !containsString(data, "package-0") {
		t.Error("ISO should contain first package")
	}
	if !containsString(data, "package-499") {
		t.Error("ISO should contain last package")
	}
	if !containsString(data, "command-499") {
		t.Error("ISO should contain last runcmd")
	}
}

func TestBuildCIDATAISO_InvalidPath(t *testing.T) {
	userData := []byte("#cloud-config\nhostname: test\n")
	err := BuildCIDATAISO("/nonexistent/dir/cidata.iso", "test", userData, nil)
	if err == nil {
		t.Fatal("expected error for invalid output path")
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
