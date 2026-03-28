package image

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestDiskPath(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	got := m.diskPath("abc123")
	want := filepath.Join(dir, "abc123.disk")
	if got != want {
		t.Errorf("diskPath = %q, want %q", got, want)
	}
}

func TestMetaPath(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	p := m.metaPath("ghcr.io/test/img:v1")
	// Should be deterministic and end with .meta.json
	if filepath.Ext(p) != ".json" {
		t.Errorf("metaPath should end in .json, got %q", p)
	}
	if p != m.metaPath("ghcr.io/test/img:v1") {
		t.Error("metaPath should be deterministic for the same ref")
	}
	// Different refs should produce different paths.
	if m.metaPath("ghcr.io/test/img:v1") == m.metaPath("ghcr.io/test/img:v2") {
		t.Error("different refs should produce different meta paths")
	}
}

func TestResolve_MetaExistsButDiskMissing(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	ref := "ghcr.io/test/ghost:latest"
	// Write meta pointing to a non-existent disk file.
	ci := &CachedImage{
		Ref:      ref,
		Digest:   "deadbeef",
		DiskPath: filepath.Join(dir, "nonexistent.disk"),
		Size:     1024,
		PulledAt: time.Now(),
	}
	if err := m.writeMeta(ref, ci); err != nil {
		t.Fatalf("writeMeta: %v", err)
	}

	path, err := m.Resolve(ref)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if path != "" {
		t.Errorf("Resolve = %q, want empty when disk file is missing", path)
	}
}

func TestPull_CacheHit(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	ref := "ghcr.io/test/cached:v1"
	diskPath := m.diskPath("somedigest")
	// Create the disk file manually.
	if err := os.WriteFile(diskPath, []byte("disk content here"), 0644); err != nil {
		t.Fatal(err)
	}
	// Create the meta file manually.
	ci := &CachedImage{
		Ref:      ref,
		Digest:   "somedigest",
		DiskPath: diskPath,
		Size:     17,
		PulledAt: time.Now(),
	}
	if err := m.writeMeta(ref, ci); err != nil {
		t.Fatal(err)
	}

	// Pull should return the cached path without hitting the network.
	got, err := m.Pull(t.Context(), ref, nil)
	if err != nil {
		t.Fatalf("Pull cache hit: %v", err)
	}
	if got != diskPath {
		t.Errorf("Pull = %q, want %q", got, diskPath)
	}
}

func TestDelete_ExistingImage(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	ref := "ghcr.io/test/todelete:v1"
	diskPath := m.diskPath("deleteme")
	os.WriteFile(diskPath, []byte("data"), 0644)

	ci := &CachedImage{
		Ref:      ref,
		Digest:   "deleteme",
		DiskPath: diskPath,
		Size:     4,
		PulledAt: time.Now(),
	}
	m.writeMeta(ref, ci)

	// Verify disk and meta exist before delete.
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatal("disk file should exist before delete")
	}
	if _, err := os.Stat(m.metaPath(ref)); err != nil {
		t.Fatal("meta file should exist before delete")
	}

	if err := m.Delete(ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Both files should be gone.
	if _, err := os.Stat(diskPath); !os.IsNotExist(err) {
		t.Error("disk file should be removed after delete")
	}
	if _, err := os.Stat(m.metaPath(ref)); !os.IsNotExist(err) {
		t.Error("meta file should be removed after delete")
	}
}

func TestList_MultipleImages(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	refs := []string{
		"ghcr.io/test/img1:v1",
		"ghcr.io/test/img2:v2",
		"ghcr.io/test/img3:v3",
	}
	for i, ref := range refs {
		ci := &CachedImage{
			Ref:      ref,
			Digest:   "digest" + string(rune('0'+i)),
			DiskPath: m.diskPath("digest" + string(rune('0'+i))),
			Size:     int64(i * 100),
			PulledAt: time.Now(),
		}
		if err := m.writeMeta(ref, ci); err != nil {
			t.Fatal(err)
		}
	}

	images, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(images) != 3 {
		t.Fatalf("List len = %d, want 3", len(images))
	}

	// All refs should be present.
	found := map[string]bool{}
	for _, img := range images {
		found[img.Ref] = true
	}
	for _, ref := range refs {
		if !found[ref] {
			t.Errorf("List missing ref %q", ref)
		}
	}
}

func TestList_SkipsCorruptMeta(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	// Write one valid meta.
	ref := "ghcr.io/test/good:v1"
	ci := &CachedImage{Ref: ref, Digest: "d1", DiskPath: "/tmp/d1.disk", Size: 10}
	m.writeMeta(ref, ci)

	// Write a corrupt meta file directly.
	corruptPath := filepath.Join(dir, "corrupt.meta.json")
	os.WriteFile(corruptPath, []byte("not json{{{"), 0644)

	images, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(images) != 1 {
		t.Errorf("List len = %d, want 1 (should skip corrupt)", len(images))
	}
}

func TestProgressWriter_MultipleWrites(t *testing.T) {
	var calls []struct{ complete, total int64 }
	cb := func(complete, total int64) {
		calls = append(calls, struct{ complete, total int64 }{complete, total})
	}

	var buf bytes.Buffer
	pw := &progressWriter{
		w:     &buf,
		total: 50,
		cb:    cb,
	}

	// First write
	n, err := pw.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write 1: %v", err)
	}
	if n != 5 {
		t.Errorf("Write 1 n = %d, want 5", n)
	}

	// Second write
	n, err = pw.Write([]byte(" world"))
	if err != nil {
		t.Fatalf("Write 2: %v", err)
	}
	if n != 6 {
		t.Errorf("Write 2 n = %d, want 6", n)
	}

	// Third write
	n, err = pw.Write([]byte("!!!"))
	if err != nil {
		t.Fatalf("Write 3: %v", err)
	}
	if n != 3 {
		t.Errorf("Write 3 n = %d, want 3", n)
	}

	if len(calls) != 3 {
		t.Fatalf("callback called %d times, want 3", len(calls))
	}
	if calls[0].complete != 5 {
		t.Errorf("call 0 complete = %d, want 5", calls[0].complete)
	}
	if calls[1].complete != 11 {
		t.Errorf("call 1 complete = %d, want 11", calls[1].complete)
	}
	if calls[2].complete != 14 {
		t.Errorf("call 2 complete = %d, want 14", calls[2].complete)
	}
	for i, c := range calls {
		if c.total != 50 {
			t.Errorf("call %d total = %d, want 50", i, c.total)
		}
	}

	// Verify data actually written.
	if buf.String() != "hello world!!!" {
		t.Errorf("buf = %q, want %q", buf.String(), "hello world!!!")
	}
}

func TestProgressWriter_ErrorPropagation(t *testing.T) {
	// errWriter always returns an error.
	ew := &errWriter{err: io.ErrShortWrite}
	var called bool
	pw := &progressWriter{
		w:     ew,
		total: 100,
		cb:    func(_, _ int64) { called = true },
	}

	_, err := pw.Write([]byte("data"))
	if err != io.ErrShortWrite {
		t.Errorf("error = %v, want ErrShortWrite", err)
	}
	if !called {
		t.Error("callback should still be called even on error")
	}
}

type errWriter struct {
	err error
}

func (e *errWriter) Write(p []byte) (int, error) {
	return 0, e.err
}

func TestCachedImageJSON(t *testing.T) {
	ci := &CachedImage{
		Ref:      "ghcr.io/test/img:v1",
		Digest:   "abc123",
		DiskPath: "/tmp/abc123.disk",
		Size:     1024,
		PulledAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(ci)
	if err != nil {
		t.Fatal(err)
	}
	var ci2 CachedImage
	if err := json.Unmarshal(data, &ci2); err != nil {
		t.Fatal(err)
	}
	if ci2.Ref != ci.Ref || ci2.Digest != ci.Digest || ci2.Size != ci.Size {
		t.Errorf("JSON round-trip mismatch: got %+v", ci2)
	}
}

func TestNewManager_InvalidPath(t *testing.T) {
	// On Unix, writing under /dev/null/foo should fail.
	_, err := NewManager("/dev/null/invalid/cache")
	if err == nil {
		t.Error("expected error for invalid cache directory path")
	}
}
