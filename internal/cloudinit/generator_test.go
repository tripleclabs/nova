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

func TestGenerate_WithSharedMounts(t *testing.T) {
	out, err := Generate(GeneratorConfig{
		Hostname:      "mount-vm",
		AuthorizedKey: "ssh-ed25519 AAAA...",
		Mounts: []SharedMount{
			{Tag: "workspace", GuestPath: "/workspace"},
			{Tag: "data", GuestPath: "/mnt/data"},
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	s := string(out)
	if !strings.Contains(s, "workspace") {
		t.Error("should contain workspace mount tag")
	}
	if !strings.Contains(s, "/mnt/data") {
		t.Error("should contain /mnt/data mount path")
	}
	if !strings.Contains(s, "virtiofs") {
		t.Error("should contain virtiofs filesystem type")
	}
	if !strings.Contains(s, "mkdir -p /workspace") {
		t.Error("should contain mkdir runcmd for mount point")
	}
}

func TestGenerate_MountsWithUserConfig(t *testing.T) {
	userCfg := `#cloud-config
runcmd:
  - echo user-cmd
mounts:
  - [tmpfs, /tmp/extra, tmpfs, "size=100m"]
`
	dir := t.TempDir()
	path := filepath.Join(dir, "cloud-config.yaml")
	os.WriteFile(path, []byte(userCfg), 0644)

	out, err := Generate(GeneratorConfig{
		Hostname:      "merge-mount-vm",
		AuthorizedKey: "ssh-ed25519 AAAA...",
		UserDataPath:  path,
		Mounts: []SharedMount{
			{Tag: "share0", GuestPath: "/share"},
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	s := string(out)
	// Both nova's mount and user's mount should be present.
	if !strings.Contains(s, "share0") {
		t.Error("should contain nova's virtiofs mount")
	}
	if !strings.Contains(s, "tmpfs") {
		t.Error("should contain user's tmpfs mount")
	}
	// Both runcmds should be present.
	if !strings.Contains(s, "mkdir -p /share") {
		t.Error("should contain nova's mkdir runcmd")
	}
	if !strings.Contains(s, "echo user-cmd") {
		t.Error("should contain user's runcmd")
	}
}

func TestGenerate_NinePMounts(t *testing.T) {
	out, err := Generate(GeneratorConfig{
		Hostname:      "linux-vm",
		AuthorizedKey: "ssh-ed25519 AAAA...",
		Mounts: []SharedMount{
			{Tag: "workspace", GuestPath: "/workspace", MountType: "9p"},
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	s := string(out)
	if !strings.Contains(s, "9p") {
		t.Error("should contain 9p filesystem type")
	}
	if !strings.Contains(s, "trans=virtio") {
		t.Error("should contain trans=virtio mount option")
	}
	if !strings.Contains(s, "version=9p2000.L") {
		t.Error("should contain version=9p2000.L mount option")
	}
	// Should NOT contain virtiofs for a 9p mount.
	if strings.Contains(s, "virtiofs") {
		t.Error("9p mount should not contain virtiofs")
	}
}

func TestGenerate_DefaultMountTypeIsVirtioFS(t *testing.T) {
	out, err := Generate(GeneratorConfig{
		Hostname:      "vm",
		AuthorizedKey: "ssh-ed25519 AAAA...",
		Mounts: []SharedMount{
			{Tag: "share", GuestPath: "/share"}, // MountType empty → virtiofs
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "virtiofs") {
		t.Error("empty MountType should default to virtiofs")
	}
}

func TestGenerate_Rosetta(t *testing.T) {
	out, err := Generate(GeneratorConfig{
		Hostname:      "rosetta-vm",
		AuthorizedKey: "ssh-ed25519 AAAA...",
		Rosetta:       true,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	s := string(out)
	if !strings.Contains(s, "rosetta") {
		t.Error("should contain rosetta mount command")
	}
	if !strings.Contains(s, "update-binfmts") {
		t.Error("should contain binfmt_misc registration")
	}
	if !strings.Contains(s, "/media/rosetta") {
		t.Error("should contain rosetta mount point")
	}
}

func TestGenerate_RosettaWithMounts(t *testing.T) {
	out, err := Generate(GeneratorConfig{
		Hostname:      "rosetta-mount-vm",
		AuthorizedKey: "ssh-ed25519 AAAA...",
		Rosetta:       true,
		Mounts: []SharedMount{
			{Tag: "workspace", GuestPath: "/workspace"},
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	s := string(out)
	// Both workspace mount and rosetta commands should be present.
	if !strings.Contains(s, "mkdir -p /workspace") {
		t.Error("should contain workspace mkdir")
	}
	if !strings.Contains(s, "update-binfmts") {
		t.Error("should contain rosetta binfmt registration")
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

func TestToSlice_StringSlice(t *testing.T) {
	input := []string{"a", "b", "c"}
	result, ok := toSlice(input)
	if !ok {
		t.Fatal("toSlice should return true for []string")
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(result))
	}
	for i, expected := range []string{"a", "b", "c"} {
		if result[i] != expected {
			t.Errorf("element %d = %v, want %v", i, result[i], expected)
		}
	}
}

func TestToSlice_NonSlice(t *testing.T) {
	_, ok := toSlice("not a slice")
	if ok {
		t.Error("toSlice should return false for a string")
	}
	_, ok = toSlice(42)
	if ok {
		t.Error("toSlice should return false for an int")
	}
}

func TestAppendLists_NonListBase(t *testing.T) {
	// When base is not a list but override is, override wins.
	result := appendLists("not-a-list", []any{"x", "y"})
	list, ok := result.([]any)
	if !ok {
		t.Fatalf("expected []any, got %T", result)
	}
	if len(list) != 2 || list[0] != "x" {
		t.Errorf("unexpected result: %v", list)
	}
}

func TestAppendLists_NonListOverride(t *testing.T) {
	// When override is not a list, base is returned.
	base := []any{"a", "b"}
	result := appendLists(base, "not-a-list")
	list, ok := result.([]any)
	if !ok {
		t.Fatalf("expected []any, got %T", result)
	}
	if len(list) != 2 || list[0] != "a" {
		t.Errorf("unexpected result: %v", list)
	}
}

func TestAppendLists_BothNonList(t *testing.T) {
	// When neither is a list, base is returned.
	result := appendLists("base-val", "override-val")
	s, ok := result.(string)
	if !ok {
		t.Fatalf("expected string, got %T", result)
	}
	if s != "base-val" {
		t.Errorf("expected base-val, got %s", s)
	}
}

func TestGenerate_HostsNoMounts(t *testing.T) {
	out, err := Generate(GeneratorConfig{
		Hostname:      "hosts-vm",
		AuthorizedKey: "ssh-ed25519 AAAA...",
		Hosts: []HostEntry{
			{IP: "10.0.0.1", Hostname: "node1"},
			{IP: "10.0.0.2", Hostname: "node2"},
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	s := string(out)
	if !strings.Contains(s, "10.0.0.1 node1") {
		t.Error("should contain first host entry")
	}
	if !strings.Contains(s, "10.0.0.2 node2") {
		t.Error("should contain second host entry")
	}
	if !strings.Contains(s, "/etc/hosts") {
		t.Error("should contain /etc/hosts path in write_files")
	}
	if !strings.Contains(s, "append: true") {
		t.Error("should set append: true for hosts file")
	}
	// No mounts means no runcmd should be generated (no mkdir).
	if strings.Contains(s, "mkdir") {
		t.Error("should not contain mkdir when there are no mounts")
	}
}

func TestGenerate_RosettaWithUserConfig(t *testing.T) {
	userCfg := `#cloud-config
runcmd:
  - echo user-setup
packages:
  - htop
`
	dir := t.TempDir()
	path := filepath.Join(dir, "cloud-config.yaml")
	os.WriteFile(path, []byte(userCfg), 0644)

	out, err := Generate(GeneratorConfig{
		Hostname:      "rosetta-user-vm",
		AuthorizedKey: "ssh-ed25519 AAAA...",
		Rosetta:       true,
		UserDataPath:  path,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	s := string(out)
	// Rosetta commands should be present.
	if !strings.Contains(s, "update-binfmts") {
		t.Error("should contain rosetta binfmt registration")
	}
	if !strings.Contains(s, "/media/rosetta") {
		t.Error("should contain rosetta mount point")
	}
	// User runcmd should be merged in.
	if !strings.Contains(s, "echo user-setup") {
		t.Error("should contain user's runcmd")
	}
	// User packages should be present.
	if !strings.Contains(s, "htop") {
		t.Error("should contain user's packages")
	}
}

func TestGenerate_EmptyAuthorizedKey(t *testing.T) {
	out, err := Generate(GeneratorConfig{
		Hostname:      "empty-key-vm",
		AuthorizedKey: "",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	s := string(out)
	if !strings.Contains(s, "hostname: empty-key-vm") {
		t.Error("should contain hostname")
	}
	if !strings.Contains(s, "ssh_authorized_keys") {
		t.Error("should still contain ssh_authorized_keys field")
	}
}

func TestGenerate_WithExtraUser(t *testing.T) {
	out, err := Generate(GeneratorConfig{
		Hostname:      "user-vm",
		AuthorizedKey: "ssh-ed25519 AAAA... nova@host",
		ExtraUser: &UserConfig{
			Name:   "deploy",
			SSHKey: "ssh-ed25519 BBBB... deploy@host",
			Groups: []string{"sudo", "docker"},
			Shell:  "/bin/zsh",
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	s := string(out)
	// Nova user should still be present.
	if !strings.Contains(s, "name: nova") {
		t.Error("should contain nova user")
	}
	// Extra user should be present.
	if !strings.Contains(s, "name: deploy") {
		t.Error("should contain deploy user")
	}
	if !strings.Contains(s, "ssh-ed25519 BBBB") {
		t.Error("should contain deploy user's SSH key")
	}
	if !strings.Contains(s, "/bin/zsh") {
		t.Error("should contain deploy user's shell")
	}
	if !strings.Contains(s, "docker") {
		t.Error("should contain deploy user's groups")
	}
}

func TestGenerate_ExtraUserWithPasswordHash(t *testing.T) {
	out, err := Generate(GeneratorConfig{
		Hostname:      "pass-vm",
		AuthorizedKey: "ssh-ed25519 AAAA...",
		ExtraUser: &UserConfig{
			Name:         "admin",
			PasswordHash: "$6$rounds=4096$salt$hash",
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	s := string(out)
	if !strings.Contains(s, "name: admin") {
		t.Error("should contain admin user")
	}
	if !strings.Contains(s, "$6$rounds") {
		t.Error("should contain password hash")
	}
}

func TestGenerate_ExtraUserDefaultShell(t *testing.T) {
	out, err := Generate(GeneratorConfig{
		Hostname:      "shell-vm",
		AuthorizedKey: "ssh-ed25519 AAAA...",
		ExtraUser: &UserConfig{
			Name:   "deploy",
			SSHKey: "ssh-ed25519 KEY...",
			// Shell not set — should use distro default.
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	s := string(out)
	// Should get the default shell (bash for generic profile).
	if !strings.Contains(s, "/bin/bash") {
		t.Error("extra user should get default shell when not specified")
	}
}

func TestGenerate_InvalidUserConfigYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	// Use truly invalid YAML: a tab character in an indentation context breaks the parser.
	os.WriteFile(path, []byte("key:\n\t- broken\n  - mixed"), 0644)

	_, err := Generate(GeneratorConfig{
		Hostname:      "vm",
		AuthorizedKey: "ssh-ed25519 AAAA...",
		UserDataPath:  path,
	})
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}
