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
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/tripleclabs/nova/internal/distro"
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
	Ref      string    `json:"ref"`
	Digest   string    `json:"digest"`
	DiskPath string    `json:"disk_path"`
	Size     int64     `json:"size"`
	PulledAt time.Time `json:"pulled_at"`
	OS       string    `json:"os,omitempty"` // e.g. "ubuntu", "alpine"; empty for custom images
}

// NewManager creates a Manager with the given cache directory.
func NewManager(cacheDir string) (*Manager, error) {
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("creating image cache dir: %w", err)
	}
	return &Manager{cacheDir: cacheDir}, nil
}

// normalizeRef expands a distro shorthand to its canonical nova.local reference.
// "ubuntu:24.04" → "nova.local/ubuntu:24.04"; other refs are returned unchanged.
func normalizeRef(ref string) string {
	if distro.IsShorthand(ref) {
		return distro.CanonicalRef(ref)
	}
	return ref
}

// Resolve returns the local cache path for an image reference, or an empty
// string if the image has not been cached yet. Distro shorthands are accepted.
func (m *Manager) Resolve(ref string) (string, error) {
	ref = normalizeRef(ref)
	ci, err := m.readMeta(ref)
	if err != nil {
		return "", nil // not cached
	}
	if _, err := os.Stat(ci.DiskPath); err != nil {
		return "", nil // cache entry exists but file is missing
	}
	return ci.DiskPath, nil
}

// ResolveImage returns the full CachedImage for a reference, or nil if not cached.
func (m *Manager) ResolveImage(ref string) *CachedImage {
	ref = normalizeRef(ref)
	ci, err := m.readMeta(ref)
	if err != nil {
		return nil
	}
	if _, err := os.Stat(ci.DiskPath); err != nil {
		return nil
	}
	return ci
}

// Pull ensures a VM disk image is available in the local cache and returns its path.
//
// Resolution order:
//  1. Already cached → return immediately.
//  2. Known distro shorthand (e.g. "ubuntu:24.04") → download from official URL.
//  3. Full OCI reference → pull from the remote registry.
//
// Progress is reported via the optional callback (bytes complete, bytes total).
func (m *Manager) Pull(ctx context.Context, ref string, progress func(complete, total int64)) (string, error) {
	canonical := normalizeRef(ref)

	// 1. Check cache.
	if path, _ := m.Resolve(canonical); path != "" {
		slog.Info("image already cached", "ref", canonical, "path", path)
		return path, nil
	}

	// 2. Known distro — download directly from the official URL.
	if distro.IsShorthand(ref) {
		shorthand := ref
		spec, ok := distro.Lookup(shorthand)
		if !ok {
			return "", fmt.Errorf("unknown distro %q — run 'nova image build' to add a custom image", shorthand)
		}
		return m.pullDistro(ctx, shorthand, spec, progress)
	}

	// 3. OCI registry pull.
	return m.pullOCI(ctx, canonical, progress)
}

// pullDistro downloads a distro cloud image from its official URL and caches it.
func (m *Manager) pullDistro(ctx context.Context, shorthand string, spec *distro.Spec, progress func(int64, int64)) (string, error) {
	canonical := distro.CanonicalRef(shorthand)

	url, ok := spec.DownloadURL()
	if !ok {
		return "", fmt.Errorf("no download URL for distro %q on arch %s", shorthand, archName())
	}

	slog.Info("downloading distro image", "distro", shorthand, "url", url)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", shorthand, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: HTTP %d", shorthand, resp.StatusCode)
	}

	// Stream to a temp file in the cache dir, then compute digest.
	tmp, err := os.CreateTemp(m.cacheDir, "download-*.tmp")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { os.Remove(tmpPath) }()

	h := sha256.New()
	var written int64
	total := resp.ContentLength

	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				tmp.Close()
				return "", werr
			}
			h.Write(buf[:n])
			written += int64(n)
			if progress != nil {
				progress(written, total)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			tmp.Close()
			return "", fmt.Errorf("downloading %s: %w", shorthand, err)
		}
	}
	tmp.Close()

	digestHex := hex.EncodeToString(h.Sum(nil))
	diskPath := m.diskPath(digestHex)
	if err := linkOrCopy(tmpPath, diskPath); err != nil {
		return "", fmt.Errorf("caching disk: %w", err)
	}

	// Extract OS name from shorthand ("ubuntu:24.04" → "ubuntu").
	osName := shorthand
	if i := len(shorthand); i > 0 {
		for j, c := range shorthand {
			if c == ':' {
				osName = shorthand[:j]
				break
			}
		}
	}

	ci := &CachedImage{
		Ref:      canonical,
		Digest:   digestHex,
		DiskPath: diskPath,
		Size:     written,
		PulledAt: time.Now(),
		OS:       osName,
	}
	if err := m.writeMeta(canonical, ci); err != nil {
		return "", err
	}

	slog.Info("distro image cached", "ref", canonical, "size", written, "path", diskPath)
	return diskPath, nil
}

// pullOCI pulls an image from an OCI registry and caches the first layer as the disk.
func (m *Manager) pullOCI(ctx context.Context, ref string, progress func(complete, total int64)) (string, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", fmt.Errorf("parsing image ref %q: %w", ref, err)
	}

	slog.Info("pulling OCI image", "ref", ref)

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

	slog.Info("OCI image cached", "ref", ref, "size", info.Size(), "path", diskPath)
	return diskPath, nil
}

func archName() string {
	return runtime.GOARCH
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
