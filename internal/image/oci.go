package image

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// PushDir packs all files in a directory into an OCI image (one layer per file)
// and pushes it to the given registry reference. Used for snapshot push.
func (m *Manager) PushDir(ctx context.Context, dir, ref string) error {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return fmt.Errorf("parsing ref %q: %w", ref, err)
	}

	img := empty.Image

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading pack dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filePath := filepath.Join(dir, entry.Name())
		layer, err := tarball.LayerFromFile(filePath)
		if err != nil {
			return fmt.Errorf("creating layer from %s: %w", entry.Name(), err)
		}
		img, err = mutate.AppendLayers(img, layer)
		if err != nil {
			return fmt.Errorf("appending layer %s: %w", entry.Name(), err)
		}
	}

	// Set a custom media type to identify this as a Nova snapshot.
	img = mutate.MediaType(img, types.OCIManifestSchema1)

	slog.Info("pushing snapshot", "ref", ref, "files", len(entries))
	if err := remote.Write(parsed, img,
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
		remote.WithContext(ctx),
	); err != nil {
		return fmt.Errorf("pushing to %s: %w", ref, err)
	}

	return nil
}

// PullDir pulls an OCI image and extracts each layer as a file into a
// temporary directory. Returns the directory path. Caller must clean up.
func (m *Manager) PullDir(ctx context.Context, ref string) (string, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", fmt.Errorf("parsing ref %q: %w", ref, err)
	}

	desc, err := remote.Get(parsed,
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
		remote.WithContext(ctx),
	)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", ref, err)
	}

	img, err := desc.Image()
	if err != nil {
		return "", fmt.Errorf("resolving image: %w", err)
	}

	layers, err := img.Layers()
	if err != nil {
		return "", fmt.Errorf("reading layers: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "nova-snap-pull-*")
	if err != nil {
		return "", err
	}

	for i, layer := range layers {
		if err := extractLayerToFile(layer, tmpDir, i); err != nil {
			os.RemoveAll(tmpDir)
			return "", fmt.Errorf("extracting layer %d: %w", i, err)
		}
	}

	slog.Info("snapshot pulled", "ref", ref, "layers", len(layers), "dir", tmpDir)
	return tmpDir, nil
}

func extractLayerToFile(layer v1.Layer, dir string, index int) error {
	rc, err := layer.Uncompressed()
	if err != nil {
		return err
	}
	defer rc.Close()

	// Name the file by its digest for uniqueness.
	digest, err := layer.Digest()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("layer-%d-%s", index, digest.Hex[:12]))

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.ReadFrom(rc)
	return err
}
