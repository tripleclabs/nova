// Package cloudinit handles SSH key generation, cloud-init config merging,
// and NoCloud ISO creation.
package cloudinit

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

// SSHKeyPair holds paths to a generated ED25519 keypair.
type SSHKeyPair struct {
	PrivateKeyPath string
	PublicKeyPath  string
	AuthorizedKey  string // The public key in authorized_keys format.
}

// GenerateSSHKeyPair creates an ED25519 keypair in the given directory.
func GenerateSSHKeyPair(dir string) (*SSHKeyPair, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("creating ssh key dir: %w", err)
	}

	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating ED25519 key: %w", err)
	}

	// Marshal private key to OpenSSH PEM format.
	privBytes, err := ssh.MarshalPrivateKey(privKey, "")
	if err != nil {
		return nil, fmt.Errorf("marshaling private key: %w", err)
	}

	privPath := filepath.Join(dir, "nova_ed25519")
	if err := os.WriteFile(privPath, pem.EncodeToMemory(privBytes), 0600); err != nil {
		return nil, fmt.Errorf("writing private key: %w", err)
	}

	// Marshal public key to authorized_keys format.
	sshPub, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		return nil, fmt.Errorf("creating SSH public key: %w", err)
	}
	authorizedKey := string(ssh.MarshalAuthorizedKey(sshPub))

	pubPath := filepath.Join(dir, "nova_ed25519.pub")
	if err := os.WriteFile(pubPath, []byte(authorizedKey), 0644); err != nil {
		return nil, fmt.Errorf("writing public key: %w", err)
	}

	return &SSHKeyPair{
		PrivateKeyPath: privPath,
		PublicKeyPath:  pubPath,
		AuthorizedKey:  authorizedKey,
	}, nil
}

// LoadSSHKeyPair reads an existing keypair from the given directory.
func LoadSSHKeyPair(dir string) (*SSHKeyPair, error) {
	privPath := filepath.Join(dir, "nova_ed25519")
	pubPath := filepath.Join(dir, "nova_ed25519.pub")

	if _, err := os.Stat(privPath); err != nil {
		return nil, fmt.Errorf("private key not found: %w", err)
	}

	pubBytes, err := os.ReadFile(pubPath)
	if err != nil {
		return nil, fmt.Errorf("reading public key: %w", err)
	}

	return &SSHKeyPair{
		PrivateKeyPath: privPath,
		PublicKeyPath:  pubPath,
		AuthorizedKey:  string(pubBytes),
	}, nil
}
