package image

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// --- helpers ---

// writeTempDisk creates a temp file with the given content and returns its path.
func writeTempDisk(t *testing.T, content []byte) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "test-disk-*.img")
	if err != nil {
		t.Fatalf("create temp disk: %v", err)
	}
	defer f.Close()
	if _, err := f.Write(content); err != nil {
		t.Fatalf("write temp disk: %v", err)
	}
	return f.Name()
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// --- fileSHA256 ---

func TestFileSHA256(t *testing.T) {
	content := []byte("hello nova disk image")
	path := writeTempDisk(t, content)

	got, size, err := fileSHA256(path)
	if err != nil {
		t.Fatalf("fileSHA256: %v", err)
	}
	if got.Algorithm != "sha256" {
		t.Errorf("algorithm = %q, want sha256", got.Algorithm)
	}
	want := sha256hex(content)
	if got.Hex != want {
		t.Errorf("digest = %q, want %q", got.Hex, want)
	}
	if size != int64(len(content)) {
		t.Errorf("size = %d, want %d", size, len(content))
	}
}

func TestFileSHA256_NotFound(t *testing.T) {
	_, _, err := fileSHA256("/nonexistent/file.img")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// --- linkOrCopy ---

func TestLinkOrCopy_SamePath(t *testing.T) {
	path := writeTempDisk(t, []byte("data"))
	// Same src and dst — should be a no-op, no error.
	if err := linkOrCopy(path, path); err != nil {
		t.Fatalf("linkOrCopy same path: %v", err)
	}
}

func TestLinkOrCopy_CreatesDestination(t *testing.T) {
	content := []byte("disk image bytes")
	src := writeTempDisk(t, content)
	dst := filepath.Join(t.TempDir(), "copy.img")

	if err := linkOrCopy(src, dst); err != nil {
		t.Fatalf("linkOrCopy: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading dst: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("dst content mismatch: got %q, want %q", got, content)
	}
}

func TestLinkOrCopy_OverwritesStale(t *testing.T) {
	src := writeTempDisk(t, []byte("new content"))
	dst := writeTempDisk(t, []byte("old content"))

	if err := linkOrCopy(src, dst); err != nil {
		t.Fatalf("linkOrCopy: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "new content" {
		t.Errorf("dst = %q, want new content", got)
	}
}

// --- rawDiskLayer ---

func TestRawDiskLayer_Digest(t *testing.T) {
	content := []byte("qcow2-like bytes")
	path := writeTempDisk(t, content)
	digest, size, _ := fileSHA256(path)

	layer := &rawDiskLayer{diskPath: path, digest: digest, size: size}

	got, err := layer.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if got != digest {
		t.Errorf("Digest = %v, want %v", got, digest)
	}
}

func TestRawDiskLayer_DiffIDEqualsDigest(t *testing.T) {
	content := []byte("raw disk")
	path := writeTempDisk(t, content)
	digest, size, _ := fileSHA256(path)

	layer := &rawDiskLayer{diskPath: path, digest: digest, size: size}

	d, _ := layer.Digest()
	id, err := layer.DiffID()
	if err != nil {
		t.Fatalf("DiffID: %v", err)
	}
	if id != d {
		t.Error("DiffID should equal Digest for uncompressed disk layers")
	}
}

func TestRawDiskLayer_Size(t *testing.T) {
	content := []byte("some disk data")
	path := writeTempDisk(t, content)
	digest, size, _ := fileSHA256(path)

	layer := &rawDiskLayer{diskPath: path, digest: digest, size: size}

	got, err := layer.Size()
	if err != nil {
		t.Fatalf("Size: %v", err)
	}
	if got != int64(len(content)) {
		t.Errorf("Size = %d, want %d", got, len(content))
	}
}

func TestRawDiskLayer_Compressed_StreamsFileBytes(t *testing.T) {
	content := []byte("raw disk image content for streaming test")
	path := writeTempDisk(t, content)
	digest, size, _ := fileSHA256(path)

	layer := &rawDiskLayer{diskPath: path, digest: digest, size: size}

	rc, err := layer.Compressed()
	if err != nil {
		t.Fatalf("Compressed: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading compressed: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("Compressed bytes mismatch")
	}
}

func TestRawDiskLayer_Uncompressed_StreamsFileBytes(t *testing.T) {
	content := []byte("raw disk image content for uncompressed test")
	path := writeTempDisk(t, content)
	digest, size, _ := fileSHA256(path)

	layer := &rawDiskLayer{diskPath: path, digest: digest, size: size}

	rc, err := layer.Uncompressed()
	if err != nil {
		t.Fatalf("Uncompressed: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading uncompressed: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("Uncompressed bytes mismatch")
	}
}

func TestRawDiskLayer_CanOpenMultipleTimes(t *testing.T) {
	// Each call to Compressed/Uncompressed should open an independent stream.
	content := []byte("multi-open test")
	path := writeTempDisk(t, content)
	digest, size, _ := fileSHA256(path)
	layer := &rawDiskLayer{diskPath: path, digest: digest, size: size}

	rc1, _ := layer.Compressed()
	defer rc1.Close()
	rc2, _ := layer.Compressed()
	defer rc2.Close()

	b1, _ := io.ReadAll(rc1)
	b2, _ := io.ReadAll(rc2)
	if !bytes.Equal(b1, content) || !bytes.Equal(b2, content) {
		t.Error("each Compressed() call should stream the full file independently")
	}
}

// --- Build ---

func TestBuild_SeedsCache(t *testing.T) {
	content := []byte("fake qcow2 disk image data")
	diskPath := writeTempDisk(t, content)
	ref := "ghcr.io/test/myimage:latest"

	cacheDir := t.TempDir()
	m, _ := NewManager(cacheDir)

	ci, err := m.Build(t.Context(), diskPath, ref, false)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Metadata should be correct.
	if ci.Ref != ref {
		t.Errorf("Ref = %q, want %q", ci.Ref, ref)
	}
	if ci.Size != int64(len(content)) {
		t.Errorf("Size = %d, want %d", ci.Size, len(content))
	}
	wantDigest := sha256hex(content)
	if ci.Digest != wantDigest {
		t.Errorf("Digest = %q, want %q", ci.Digest, wantDigest)
	}
	if ci.PulledAt.IsZero() {
		t.Error("PulledAt should not be zero")
	}

	// Cached disk file should exist and contain the original bytes.
	got, err := os.ReadFile(ci.DiskPath)
	if err != nil {
		t.Fatalf("reading cached disk: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Error("cached disk content does not match source")
	}
}

func TestBuild_ResolvableAfterBuild(t *testing.T) {
	diskPath := writeTempDisk(t, []byte("disk bytes"))
	ref := "ghcr.io/test/resolvable:v1"

	m, _ := NewManager(t.TempDir())
	if _, err := m.Build(t.Context(), diskPath, ref, false); err != nil {
		t.Fatalf("Build: %v", err)
	}

	path, err := m.Resolve(ref)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if path == "" {
		t.Error("Resolve returned empty path after Build")
	}
}

func TestBuild_ListableAfterBuild(t *testing.T) {
	diskPath := writeTempDisk(t, []byte("disk bytes"))
	ref := "ghcr.io/test/listable:v1"

	m, _ := NewManager(t.TempDir())
	if _, err := m.Build(t.Context(), diskPath, ref, false); err != nil {
		t.Fatalf("Build: %v", err)
	}

	images, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(images) != 1 {
		t.Fatalf("List len = %d, want 1", len(images))
	}
	if images[0].Ref != ref {
		t.Errorf("List[0].Ref = %q, want %q", images[0].Ref, ref)
	}
}

func TestBuild_DiskPathInCacheDir(t *testing.T) {
	cacheDir := t.TempDir()
	diskPath := writeTempDisk(t, []byte("data"))
	m, _ := NewManager(cacheDir)

	ci, err := m.Build(t.Context(), diskPath, "example.com/img:tag", false)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// The cached disk must live inside the cache dir.
	rel, err := filepath.Rel(cacheDir, ci.DiskPath)
	if err != nil || rel[:2] == ".." {
		t.Errorf("DiskPath %q should be inside cacheDir %q", ci.DiskPath, cacheDir)
	}
}

func TestBuild_InvalidDiskPath(t *testing.T) {
	m, _ := NewManager(t.TempDir())
	_, err := m.Build(t.Context(), "/nonexistent/disk.img", "example.com/img:tag", false)
	if err == nil {
		t.Fatal("expected error for missing disk file")
	}
}

func TestBuild_InvalidRef(t *testing.T) {
	diskPath := writeTempDisk(t, []byte("data"))
	m, _ := NewManager(t.TempDir())
	_, err := m.Build(t.Context(), diskPath, "not a valid ref !!!", false)
	if err == nil {
		t.Fatal("expected error for invalid ref")
	}
}

func TestBuild_Idempotent(t *testing.T) {
	content := []byte("idempotent disk")
	diskPath := writeTempDisk(t, content)
	ref := "example.com/img:v1"
	m, _ := NewManager(t.TempDir())

	ci1, err := m.Build(t.Context(), diskPath, ref, false)
	if err != nil {
		t.Fatalf("first Build: %v", err)
	}
	ci2, err := m.Build(t.Context(), diskPath, ref, false)
	if err != nil {
		t.Fatalf("second Build: %v", err)
	}

	if ci1.Digest != ci2.Digest {
		t.Error("digest should be stable across identical builds")
	}
	if ci1.DiskPath != ci2.DiskPath {
		t.Error("cached disk path should be stable across identical builds")
	}
}
