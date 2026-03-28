package vm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/3clabs/nova/internal/state"
)

// helper creates an Orchestrator backed by a temp dir.
func newTestOrchestrator(t *testing.T) *Orchestrator {
	t.Helper()
	dir := t.TempDir()
	o, err := NewOrchestratorWithDir(dir)
	if err != nil {
		t.Fatalf("NewOrchestratorWithDir: %v", err)
	}
	return o
}

// helper creates a machine in the store with the given state.
func createMachine(t *testing.T, o *Orchestrator, name string, s state.MachineState) {
	t.Helper()
	m := &state.Machine{
		ID:    name,
		Name:  name,
		State: s,
		PID:   12345,
	}
	if err := o.store.Create(m); err != nil {
		t.Fatalf("store.Create(%q): %v", name, err)
	}
}

// ---------- NewOrchestrator / NewOrchestratorWithDir ----------

func TestNewOrchestratorWithDir(t *testing.T) {
	dir := t.TempDir()
	o, err := NewOrchestratorWithDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o == nil {
		t.Fatal("orchestrator is nil")
	}
	// Verify directories were created.
	for _, sub := range []string{"machines", filepath.Join("cache", "images")} {
		p := filepath.Join(dir, sub)
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			t.Errorf("expected directory %s to exist", p)
		}
	}
}

func TestNewOrchestratorWithDir_BadPath(t *testing.T) {
	// /dev/null is not a directory, so MkdirAll should fail.
	_, err := NewOrchestratorWithDir("/dev/null/impossible")
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
}

// ---------- Status ----------

func TestStatus_Empty(t *testing.T) {
	o := newTestOrchestrator(t)
	machines, err := o.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(machines) != 0 {
		t.Errorf("expected 0 machines, got %d", len(machines))
	}
}

func TestStatus_WithMachines(t *testing.T) {
	o := newTestOrchestrator(t)
	createMachine(t, o, "web", state.StateRunning)
	createMachine(t, o, "db", state.StateStopped)

	machines, err := o.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(machines) != 2 {
		t.Errorf("expected 2 machines, got %d", len(machines))
	}
}

// ---------- Down ----------

func TestDown_RunningVM(t *testing.T) {
	o := newTestOrchestrator(t)
	createMachine(t, o, "myvm", state.StateRunning)

	if err := o.Down("myvm"); err != nil {
		t.Fatalf("Down: %v", err)
	}

	m, err := o.store.Get("myvm")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if m.State != state.StateStopped {
		t.Errorf("expected state %q, got %q", state.StateStopped, m.State)
	}
	if m.PID != 0 {
		t.Errorf("expected PID 0, got %d", m.PID)
	}
}

func TestDown_DefaultName(t *testing.T) {
	o := newTestOrchestrator(t)
	createMachine(t, o, "default", state.StateRunning)

	// Passing empty string should use "default".
	if err := o.Down(""); err != nil {
		t.Fatalf("Down with empty name: %v", err)
	}

	m, err := o.store.Get("default")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if m.State != state.StateStopped {
		t.Errorf("expected stopped, got %q", m.State)
	}
}

func TestDown_NotRunning(t *testing.T) {
	o := newTestOrchestrator(t)
	createMachine(t, o, "stopped-vm", state.StateStopped)

	err := o.Down("stopped-vm")
	if err == nil {
		t.Fatal("expected error for non-running VM")
	}
}

func TestDown_NotFound(t *testing.T) {
	o := newTestOrchestrator(t)
	err := o.Down("nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent VM")
	}
}

// ---------- Destroy ----------

func TestDestroy_ExistingVM(t *testing.T) {
	o := newTestOrchestrator(t)
	createMachine(t, o, "nukevm", state.StateRunning)

	if err := o.Destroy("nukevm"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	// Machine should no longer exist.
	_, err := o.store.Get("nukevm")
	if err == nil {
		t.Error("expected machine to be deleted")
	}
}

func TestDestroy_StoppedVM(t *testing.T) {
	o := newTestOrchestrator(t)
	createMachine(t, o, "stopped", state.StateStopped)

	if err := o.Destroy("stopped"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
}

func TestDestroy_DefaultName(t *testing.T) {
	o := newTestOrchestrator(t)
	createMachine(t, o, "default", state.StateCreating)

	if err := o.Destroy(""); err != nil {
		t.Fatalf("Destroy with empty name: %v", err)
	}
}

func TestDestroy_NotFound(t *testing.T) {
	o := newTestOrchestrator(t)
	err := o.Destroy("ghost")
	if err == nil {
		t.Fatal("expected error for non-existent VM")
	}
}

// ---------- hashConfig ----------

func TestHashConfig_ValidFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "nova.hcl")
	if err := os.WriteFile(p, []byte("vm { cpus = 2 }"), 0644); err != nil {
		t.Fatal(err)
	}
	h := hashConfig(p)
	if h == "" {
		t.Error("expected non-empty hash for valid file")
	}
	if len(h) != 16 { // 8 bytes hex-encoded
		t.Errorf("expected 16-char hash, got %d chars: %q", len(h), h)
	}
}

func TestHashConfig_Deterministic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.hcl")
	if err := os.WriteFile(p, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	h1 := hashConfig(p)
	h2 := hashConfig(p)
	if h1 != h2 {
		t.Errorf("hash should be deterministic: %q != %q", h1, h2)
	}
}

func TestHashConfig_DifferentContent(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "a.hcl")
	p2 := filepath.Join(dir, "b.hcl")
	os.WriteFile(p1, []byte("content-a"), 0644)
	os.WriteFile(p2, []byte("content-b"), 0644)
	if hashConfig(p1) == hashConfig(p2) {
		t.Error("different files should produce different hashes")
	}
}

func TestHashConfig_Nonexistent(t *testing.T) {
	hash := hashConfig("/nonexistent/path")
	if hash != "" {
		t.Errorf("hashConfig for nonexistent file should be empty, got %q", hash)
	}
}

// ---------- sanitizeTag ----------

func TestSanitizeTag(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"/workspace", "workspace"},
		{"/mnt/data", "mnt_data"},
		{"share", "share"},
		{"/", "share"},
		{"", "share"},
		{"///multi///slashes", "multi___slashes"},
		{"no_slash", "no_slash"},
		{"//leading", "leading"},
		{"/a/b/c/d", "a_b_c_d"},
	}
	for _, tt := range tests {
		got := sanitizeTag(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeTag(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ---------- parseMemoryMB ----------

func TestParseMemoryMB(t *testing.T) {
	tests := []struct {
		input string
		want  uint64
	}{
		{"2G", 2048},
		{"512M", 512},
		{"1G", 1024},
		{"4G", 4096},
		{"256M", 256},
		{"0G", 0},
		{"0M", 0},
		{"1g", 1024},  // lowercase
		{"1m", 1},     // lowercase
		{"10G", 10240},
		{"1024M", 1024},
	}
	for _, tt := range tests {
		got, err := parseMemoryMB(tt.input)
		if err != nil {
			t.Errorf("parseMemoryMB(%q): %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseMemoryMB(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestParseMemoryMB_Invalid(t *testing.T) {
	cases := []string{
		"2T",    // unknown suffix
		"abc",   // non-numeric
		"",      // empty
		"G",     // no number
		"M",     // no number
		" ",     // whitespace only
		"1.5G",  // float
		"1K",    // unsupported suffix
		"-1G",   // negative (ParseUint will fail)
	}
	for _, c := range cases {
		_, err := parseMemoryMB(c)
		if err == nil {
			t.Errorf("parseMemoryMB(%q) should return error", c)
		}
	}
}

func TestParseMemoryMB_LeadingZeros(t *testing.T) {
	got, err := parseMemoryMB("01G")
	if err != nil {
		t.Fatalf("parseMemoryMB(\"01G\"): %v", err)
	}
	if got != 1024 {
		t.Errorf("parseMemoryMB(\"01G\") = %d, want 1024", got)
	}
}

func TestParseMemoryMB_Whitespace(t *testing.T) {
	got, err := parseMemoryMB("  2G  ")
	if err != nil {
		t.Fatalf("parseMemoryMB with whitespace: %v", err)
	}
	if got != 2048 {
		t.Errorf("got %d, want 2048", got)
	}
}
