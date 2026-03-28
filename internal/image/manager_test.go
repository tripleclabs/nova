package image

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestNewManager(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache", "images")

	m, err := NewManager(cacheDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if m == nil {
		t.Fatal("manager is nil")
	}

	info, err := os.Stat(cacheDir)
	if err != nil {
		t.Fatalf("cache dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("cache dir should be a directory")
	}
}

func TestResolve_NotCached(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	path, err := m.Resolve("ghcr.io/test/nonexistent:latest")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want empty for uncached image", path)
	}
}

func TestListEmpty(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	images, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(images) != 0 {
		t.Errorf("List len = %d, want 0", len(images))
	}
}

func TestDeleteNonExistent(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	err := m.Delete("ghcr.io/test/nonexistent:latest")
	if err == nil {
		t.Error("expected error deleting non-existent image")
	}
}

func TestWriteAndReadMeta(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	ref := "ghcr.io/test/ubuntu:24.04"

	// Write a fake cached image entry.
	diskPath := filepath.Join(dir, "fakedigest.disk")
	os.WriteFile(diskPath, []byte("fake disk data"), 0644)

	ci := &CachedImage{
		Ref:      ref,
		Digest:   "fakedigest",
		DiskPath: diskPath,
		Size:     14,
	}
	if err := m.writeMeta(ref, ci); err != nil {
		t.Fatalf("writeMeta: %v", err)
	}

	// Resolve should find it.
	path, err := m.Resolve(ref)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if path != diskPath {
		t.Errorf("Resolve = %q, want %q", path, diskPath)
	}

	// List should include it.
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

	// Delete should clean up.
	if err := m.Delete(ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	path, _ = m.Resolve(ref)
	if path != "" {
		t.Errorf("Resolve after delete = %q, want empty", path)
	}
}

func TestProgressWriter(t *testing.T) {
	var lastComplete, lastTotal int64
	cb := func(complete, total int64) {
		lastComplete = complete
		lastTotal = total
	}

	pw := &progressWriter{
		w:     io.Discard,
		total: 100,
		cb:    cb,
	}

	pw.Write([]byte("hello"))
	if lastComplete != 5 {
		t.Errorf("complete = %d, want 5", lastComplete)
	}
	if lastTotal != 100 {
		t.Errorf("total = %d, want 100", lastTotal)
	}

	pw.Write(make([]byte, 95))
	if lastComplete != 100 {
		t.Errorf("complete = %d, want 100", lastComplete)
	}
}
