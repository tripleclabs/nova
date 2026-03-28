package cloudinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerate_DefaultsOnly(t *testing.T) {
	out, err := Generate(GeneratorConfig{
		Hostname:      "test-vm",
		AuthorizedKey: "ssh-ed25519 AAAA... nova@host",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	s := string(out)
	if !strings.HasPrefix(s, "#cloud-config\n") {
		t.Error("output should start with #cloud-config")
	}
	if !strings.Contains(s, "hostname: test-vm") {
		t.Error("should contain hostname")
	}
	if !strings.Contains(s, "ssh_pwauth: false") {
		t.Error("should disable password auth")
	}
	if !strings.Contains(s, "ssh-ed25519 AAAA") {
		t.Error("should contain SSH key")
	}
	if !strings.Contains(s, "name: nova") {
		t.Error("should contain nova user")
	}
}

func TestGenerate_WithUserConfig(t *testing.T) {
	userCfg := `#cloud-config
package_update: true
packages:
  - curl
  - git
runcmd:
  - echo hello > /tmp/hello
`
	dir := t.TempDir()
	path := filepath.Join(dir, "cloud-config.yaml")
	os.WriteFile(path, []byte(userCfg), 0644)

	out, err := Generate(GeneratorConfig{
		Hostname:      "merged-vm",
		AuthorizedKey: "ssh-ed25519 AAAA... nova@host",
		UserDataPath:  path,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	s := string(out)
	// Nova defaults should be present.
	if !strings.Contains(s, "hostname: merged-vm") {
		t.Error("should contain hostname")
	}
	if !strings.Contains(s, "name: nova") {
		t.Error("should contain nova user")
	}

	// User config should be merged.
	if !strings.Contains(s, "package_update: true") {
		t.Error("should contain user's package_update")
	}
	if !strings.Contains(s, "curl") {
		t.Error("should contain user's packages")
	}
	if !strings.Contains(s, "echo hello") {
		t.Error("should contain user's runcmd")
	}
}

func TestGenerate_UserCannotOverrideUsers(t *testing.T) {
	userCfg := `#cloud-config
users:
  - name: hacker
    sudo: ALL=(ALL) ALL
`
	dir := t.TempDir()
	path := filepath.Join(dir, "cloud-config.yaml")
	os.WriteFile(path, []byte(userCfg), 0644)

	out, err := Generate(GeneratorConfig{
		Hostname:      "secure-vm",
		AuthorizedKey: "ssh-ed25519 AAAA... nova@host",
		UserDataPath:  path,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	s := string(out)
	if strings.Contains(s, "hacker") {
		t.Error("user should not be able to override the users block")
	}
	if !strings.Contains(s, "name: nova") {
		t.Error("nova user should be preserved")
	}
}

func TestGenerate_ListsMerge(t *testing.T) {
	userCfg := `#cloud-config
packages:
  - nginx
  - redis
`
	dir := t.TempDir()
	path := filepath.Join(dir, "cloud-config.yaml")
	os.WriteFile(path, []byte(userCfg), 0644)

	// Add packages to defaults by setting up a base that also has packages.
	out, err := Generate(GeneratorConfig{
		Hostname:      "list-vm",
		AuthorizedKey: "ssh-ed25519 AAAA...",
		UserDataPath:  path,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	s := string(out)
	if !strings.Contains(s, "nginx") || !strings.Contains(s, "redis") {
		t.Error("user packages should be present")
	}
}

func TestGenerate_MissingUserFile(t *testing.T) {
	_, err := Generate(GeneratorConfig{
		Hostname:      "vm",
		AuthorizedKey: "ssh-ed25519 AAAA...",
		UserDataPath:  "/nonexistent/cloud-config.yaml",
	})
	if err == nil {
		t.Fatal("expected error for missing user config file")
	}
}
