package state

import (
	"os"
	"testing"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestCreateAndGet(t *testing.T) {
	s := tempStore(t)
	m := &Machine{ID: "test-1", Name: "myvm", State: StateCreating, ConfigHash: "abc123"}
	if err := s.Create(m); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get("test-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "myvm" {
		t.Errorf("Name = %q, want myvm", got.Name)
	}
	if got.State != StateCreating {
		t.Errorf("State = %q, want creating", got.State)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
}

func TestCreateDuplicate(t *testing.T) {
	s := tempStore(t)
	m := &Machine{ID: "dup", Name: "vm"}
	if err := s.Create(m); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(m); err == nil {
		t.Fatal("expected error for duplicate create")
	}
}

func TestUpdate(t *testing.T) {
	s := tempStore(t)
	m := &Machine{ID: "u1", Name: "vm", State: StateCreating}
	s.Create(m)

	m.State = StateRunning
	m.PID = 12345
	if err := s.Update(m); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ := s.Get("u1")
	if got.State != StateRunning {
		t.Errorf("State = %q, want running", got.State)
	}
	if got.PID != 12345 {
		t.Errorf("PID = %d, want 12345", got.PID)
	}
}

func TestDelete(t *testing.T) {
	s := tempStore(t)
	m := &Machine{ID: "del1", Name: "vm"}
	s.Create(m)

	if err := s.Delete("del1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get("del1"); err == nil {
		t.Fatal("Get after delete should fail")
	}
}

func TestDeleteNonExistent(t *testing.T) {
	s := tempStore(t)
	if err := s.Delete("nope"); err == nil {
		t.Fatal("expected error deleting non-existent machine")
	}
}

func TestList(t *testing.T) {
	s := tempStore(t)
	for _, id := range []string{"a", "b", "c"} {
		s.Create(&Machine{ID: id, Name: id})
	}
	machines, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(machines) != 3 {
		t.Errorf("List len = %d, want 3", len(machines))
	}
}

func TestLock(t *testing.T) {
	s := tempStore(t)
	m := &Machine{ID: "lock1", Name: "vm"}
	s.Create(m)

	unlock, err := s.Lock("lock1")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	// Second lock should fail.
	_, err = s.Lock("lock1")
	if err == nil {
		t.Fatal("expected error for double lock")
	}

	unlock()

	// Should succeed after unlock.
	unlock2, err := s.Lock("lock1")
	if err != nil {
		t.Fatalf("Lock after unlock: %v", err)
	}
	unlock2()
}

func TestMachineDir(t *testing.T) {
	s := tempStore(t)
	m := &Machine{ID: "dirtest", Name: "vm"}
	s.Create(m)

	dir := s.MachineDir("dirtest")
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("MachineDir does not exist: %v", err)
	}
	if !info.IsDir() {
		t.Error("MachineDir should be a directory")
	}
}
