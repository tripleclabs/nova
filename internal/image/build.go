package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// rawDiskLayer implements v1.Layer for a local disk image file.
//
// Nova stores disk images as OCI layers without tar wrapping or gzip compression:
// the "compressed" form of the layer IS the raw disk bytes. This matches
// extractLayer() in manager.go, which calls layer.Compressed() and io.Copy's
// the result directly to disk.
//
// All methods that return bytes open the file on demand; nothing is loaded into
// memory, making this safe for GB-sized disk images.
type rawDiskLayer struct {
	diskPath string
	digest   v1.Hash // sha256 of disk file, pre-computed
	size     int64
}

func (l *rawDiskLayer) Digest() (v1.Hash, error)   { return l.digest, nil }
func (l *rawDiskLayer) DiffID() (v1.Hash, error)   { return l.digest, nil }
func (l *rawDiskLayer) Size() (int64, error)        { return l.size, nil }
func (l *rawDiskLayer) MediaType() (types.MediaType, error) { return types.OCILayer, nil }

// Compressed returns the raw disk bytes. In nova's OCI format these bytes are
// the actual disk image — no compression is applied.
func (l *rawDiskLayer) Compressed() (io.ReadCloser, error) {
	return os.Open(l.diskPath)
}

// Uncompressed returns the same raw disk bytes as Compressed.
func (l *rawDiskLayer) Uncompressed() (io.ReadCloser, error) {
	return os.Open(l.diskPath)
}

// Build wraps a local disk image file as an OCI image, seeds it into the
// Manager's cache, and optionally pushes it to a remote registry.
//
// After Build returns, the image is immediately available to nova up without
// any network access.
// Build packages a local disk image into the nova cache.
// osName is an optional OS identifier (e.g. "ubuntu", "alpine:3.21") stored as
// metadata so nova up can apply OS-specific cloud-init configuration.
func (m *Manager) Build(ctx context.Context, diskPath, ref, osName string, push bool) (*CachedImage, error) {
	if _, err := os.Stat(diskPath); err != nil {
		return nil, fmt.Errorf("disk image %q: %w", diskPath, err)
	}

	parsed, err := name.ParseReference(ref)
	if err != nil {
		return nil, fmt.Errorf("parsing image ref %q: %w", ref, err)
	}

	slog.Info("computing image digest", "path", diskPath)
	digest, size, err := fileSHA256(diskPath)
	if err != nil {
		return nil, fmt.Errorf("hashing disk image: %w", err)
	}

	layer := &rawDiskLayer{diskPath: diskPath, digest: digest, size: size}

	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		return nil, fmt.Errorf("building OCI image: %w", err)
	}

	// Seed the local cache.
	destPath := m.diskPath(digest.Hex)
	if err := linkOrCopy(diskPath, destPath); err != nil {
		return nil, fmt.Errorf("caching disk image: %w", err)
	}

	ci := &CachedImage{
		Ref:      ref,
		Digest:   digest.Hex,
		DiskPath: destPath,
		Size:     size,
		PulledAt: time.Now(),
		OS:       osName,
	}
	if err := m.writeMeta(ref, ci); err != nil {
		return nil, fmt.Errorf("writing image metadata: %w", err)
	}
	slog.Info("image cached", "ref", ref, "digest", digest.Hex[:12], "path", destPath)

	if push {
		slog.Info("pushing image to registry", "ref", ref)
		if err := remote.Write(parsed, img,
			remote.WithAuthFromKeychain(authn.DefaultKeychain),
			remote.WithContext(ctx),
		); err != nil {
			return nil, fmt.Errorf("pushing image %q: %w", ref, err)
		}
		slog.Info("image pushed", "ref", ref)
	}

	return ci, nil
}

// fileSHA256 computes the SHA-256 digest and byte size of a file in one pass.
func fileSHA256(path string) (v1.Hash, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return v1.Hash{}, 0, err
	}
	defer f.Close()

	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return v1.Hash{}, 0, err
	}
	return v1.Hash{Algorithm: "sha256", Hex: hex.EncodeToString(h.Sum(nil))}, n, nil
}

// linkOrCopy creates dst as a hard link to src. If they are on different
// filesystems (os.Link fails), it falls back to a full streaming copy.
// A no-op if src and dst are already the same path.
func linkOrCopy(src, dst string) error {
	if src == dst {
		return nil
	}
	// Remove any stale destination first so os.Link doesn't fail.
	os.Remove(dst)
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	// Cross-device link — fall back to streaming copy.
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
