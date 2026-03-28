// Package image handles OCI image pulling, caching, and disk management.
package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Manager handles pulling, caching, and managing VM disk images from OCI registries.
type Manager struct {
	cacheDir string
}

// CachedImage holds metadata about a locally cached image.
type CachedImage struct {
	Ref       string    `json:"ref"`
	Digest    string    `json:"digest"`
	DiskPath  string    `json:"disk_path"`
	Size      int64     `json:"size"`
	PulledAt  time.Time `json:"pulled_at"`
}

// NewManager creates a Manager with the given cache directory.
func NewManager(cacheDir string) (*Manager, error) {
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("creating image cache dir: %w", err)
	}
	return &Manager{cacheDir: cacheDir}, nil
}

// Resolve parses an image reference and returns the local cache path if it exists,
// or an empty string if the image needs to be pulled.
func (m *Manager) Resolve(ref string) (string, error) {
	ci, err := m.readMeta(ref)
	if err != nil {
		return "", nil // not cached
	}
	if _, err := os.Stat(ci.DiskPath); err != nil {
		return "", nil // cache entry exists but file is missing
	}
	return ci.DiskPath, nil
}

// Pull downloads an OCI artifact from a remote registry and extracts the VM disk
// image to the local cache. It returns the path to the cached disk file.
//
// The artifact is expected to contain exactly one layer whose content is the VM
// disk image (raw or qcow2). Progress is reported via the optional callback.
func (m *Manager) Pull(ctx context.Context, ref string, progress func(complete, total int64)) (string, error) {
	// Check cache first.
	if path, _ := m.Resolve(ref); path != "" {
		slog.Info("image already cached", "ref", ref, "path", path)
		return path, nil
	}

	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", fmt.Errorf("parsing image ref %q: %w", ref, err)
	}

	slog.Info("pulling image", "ref", ref)

	desc, err := remote.Get(parsed, remote.WithAuthFromKeychain(authn.DefaultKeychain), remote.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("fetching image descriptor: %w", err)
	}

	img, err := desc.Image()
	if err != nil {
		return "", fmt.Errorf("resolving image: %w", err)
	}

	layers, err := img.Layers()
	if err != nil {
		return "", fmt.Errorf("reading layers: %w", err)
	}
	if len(layers) == 0 {
		return "", fmt.Errorf("image %q has no layers", ref)
	}

	// Use the first layer as the disk image.
	layer := layers[0]
	digest, err := img.Digest()
	if err != nil {
		return "", fmt.Errorf("reading digest: %w", err)
	}

	diskPath := m.diskPath(digest.Hex)

	if err := m.extractLayer(layer, diskPath, progress); err != nil {
		os.Remove(diskPath)
		return "", fmt.Errorf("extracting layer: %w", err)
	}

	info, err := os.Stat(diskPath)
	if err != nil {
		return "", err
	}

	ci := &CachedImage{
		Ref:      ref,
		Digest:   digest.Hex,
		DiskPath: diskPath,
		Size:     info.Size(),
		PulledAt: time.Now(),
	}
	if err := m.writeMeta(ref, ci); err != nil {
		return "", err
	}

	slog.Info("image cached", "ref", ref, "size", info.Size(), "path", diskPath)
	return diskPath, nil
}

// List returns all cached images.
func (m *Manager) List() ([]*CachedImage, error) {
	pattern := filepath.Join(m.cacheDir, "*.meta.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	var images []*CachedImage
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var ci CachedImage
		if json.Unmarshal(data, &ci) == nil {
			images = append(images, &ci)
		}
	}
	return images, nil
}

// Delete removes a cached image by reference.
func (m *Manager) Delete(ref string) error {
	ci, err := m.readMeta(ref)
	if err != nil {
		return fmt.Errorf("image %q not found in cache", ref)
	}
	os.Remove(ci.DiskPath)
	os.Remove(m.metaPath(ref))
	slog.Info("image deleted from cache", "ref", ref)
	return nil
}

func (m *Manager) extractLayer(layer v1.Layer, dst string, progress func(complete, total int64)) error {
	rc, err := layer.Compressed()
	if err != nil {
		// Fall back to uncompressed if compressed isn't available.
		rc, err = layer.Uncompressed()
		if err != nil {
			return fmt.Errorf("opening layer: %w", err)
		}
	}
	defer rc.Close()

	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()

	var w io.Writer = f
	if progress != nil {
		size, _ := layer.Size()
		w = &progressWriter{w: f, total: size, cb: progress}
	}

	_, err = io.Copy(w, rc)
	return err
}

func (m *Manager) diskPath(digestHex string) string {
	return filepath.Join(m.cacheDir, digestHex+".disk")
}

func (m *Manager) metaPath(ref string) string {
	h := sha256.Sum256([]byte(ref))
	return filepath.Join(m.cacheDir, hex.EncodeToString(h[:])+".meta.json")
}

func (m *Manager) readMeta(ref string) (*CachedImage, error) {
	data, err := os.ReadFile(m.metaPath(ref))
	if err != nil {
		return nil, err
	}
	var ci CachedImage
	return &ci, json.Unmarshal(data, &ci)
}

func (m *Manager) writeMeta(ref string, ci *CachedImage) error {
	data, err := json.MarshalIndent(ci, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.metaPath(ref), data, 0644)
}

// progressWriter wraps a writer and reports progress.
type progressWriter struct {
	w       io.Writer
	total   int64
	written int64
	cb      func(complete, total int64)
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.w.Write(p)
	pw.written += int64(n)
	pw.cb(pw.written, pw.total)
	return n, err
}
