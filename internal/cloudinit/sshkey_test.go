package cloudinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateSSHKeyPair(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ssh")
	kp, err := GenerateSSHKeyPair(dir)
	if err != nil {
		t.Fatalf("GenerateSSHKeyPair: %v", err)
	}

	// Private key file should exist with restricted permissions.
	info, err := os.Stat(kp.PrivateKeyPath)
	if err != nil {
		t.Fatalf("private key not found: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("private key perms = %o, want 0600", info.Mode().Perm())
	}

	// Public key file should exist.
	if _, err := os.Stat(kp.PublicKeyPath); err != nil {
		t.Fatalf("public key not found: %v", err)
	}

	// AuthorizedKey should be in ssh format.
	if !strings.HasPrefix(kp.AuthorizedKey, "ssh-ed25519 ") {
		t.Errorf("AuthorizedKey should start with 'ssh-ed25519 ', got %q", kp.AuthorizedKey[:20])
	}
}

func TestLoadSSHKeyPair(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ssh")
	gen, err := GenerateSSHKeyPair(dir)
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadSSHKeyPair(dir)
	if err != nil {
		t.Fatalf("LoadSSHKeyPair: %v", err)
	}

	if loaded.AuthorizedKey != gen.AuthorizedKey {
		t.Error("loaded key doesn't match generated key")
	}
}

func TestLoadSSHKeyPair_NotFound(t *testing.T) {
	_, err := LoadSSHKeyPair(t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing keypair")
	}
}
