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

func TestGenerateSSHKeyPair_PrivateKeyPEMFormat(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ssh")
	kp, err := GenerateSSHKeyPair(dir)
	if err != nil {
		t.Fatalf("GenerateSSHKeyPair: %v", err)
	}

	// Read private key back and verify PEM format.
	privData, err := os.ReadFile(kp.PrivateKeyPath)
	if err != nil {
		t.Fatalf("reading private key: %v", err)
	}
	privStr := string(privData)
	if !strings.HasPrefix(privStr, "-----BEGIN OPENSSH PRIVATE KEY-----") {
		t.Error("private key should start with PEM header")
	}
	if !strings.Contains(privStr, "-----END OPENSSH PRIVATE KEY-----") {
		t.Error("private key should contain PEM footer")
	}

	// Read public key back and verify it matches AuthorizedKey.
	pubData, err := os.ReadFile(kp.PublicKeyPath)
	if err != nil {
		t.Fatalf("reading public key: %v", err)
	}
	if string(pubData) != kp.AuthorizedKey {
		t.Error("public key file content should match AuthorizedKey field")
	}
	if !strings.HasPrefix(string(pubData), "ssh-ed25519 ") {
		t.Error("public key file should start with ssh-ed25519")
	}
}

func TestLoadSSHKeyPair_CorruptPubKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ssh")
	// Generate a valid keypair first.
	_, err := GenerateSSHKeyPair(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Corrupt the public key file.
	pubPath := filepath.Join(dir, "nova_ed25519.pub")
	if err := os.WriteFile(pubPath, []byte("corrupted-key-data"), 0644); err != nil {
		t.Fatal(err)
	}

	// LoadSSHKeyPair should still succeed (it just reads the file, no validation).
	loaded, err := LoadSSHKeyPair(dir)
	if err != nil {
		t.Fatalf("LoadSSHKeyPair should not fail on corrupt pub key: %v", err)
	}
	// But the AuthorizedKey will be the corrupt data.
	if loaded.AuthorizedKey != "corrupted-key-data" {
		t.Errorf("expected corrupt data, got %q", loaded.AuthorizedKey)
	}
}

func TestLoadSSHKeyPair_MissingPubKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ssh")
	_, err := GenerateSSHKeyPair(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Remove the public key file.
	os.Remove(filepath.Join(dir, "nova_ed25519.pub"))

	_, err = LoadSSHKeyPair(dir)
	if err == nil {
		t.Fatal("expected error when public key file is missing")
	}
}

func TestGenerateSSHKeyPair_CreatesDirectory(t *testing.T) {
	// Nested directory that doesn't exist yet.
	dir := filepath.Join(t.TempDir(), "a", "b", "c", "ssh")
	kp, err := GenerateSSHKeyPair(dir)
	if err != nil {
		t.Fatalf("GenerateSSHKeyPair: %v", err)
	}
	if _, err := os.Stat(kp.PrivateKeyPath); err != nil {
		t.Errorf("private key should exist: %v", err)
	}
	if _, err := os.Stat(kp.PublicKeyPath); err != nil {
		t.Errorf("public key should exist: %v", err)
	}
}
