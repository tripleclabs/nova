// Package state manages Nova's local state directory (~/.nova/).
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// MachineState represents the lifecycle state of a VM.
type MachineState string

const (
	StateCreating MachineState = "creating"
	StateRunning  MachineState = "running"
	StateStopped  MachineState = "stopped"
	StateError    MachineState = "error"
)

// Machine holds the persisted metadata for a managed VM.
type Machine struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	State      MachineState `json:"state"`
	PID        int          `json:"pid,omitempty"`
	ConfigHash string       `json:"config_hash"`
	CreatedAt  time.Time    `json:"created_at"`
	UpdatedAt  time.Time    `json:"updated_at"`
}

// Store provides CRUD operations on Nova's local state.
type Store struct {
	root string // e.g. ~/.nova
}

// NewStore creates a Store rooted at the given directory, creating it if needed.
func NewStore(root string) (*Store, error) {
	dirs := []string{
		root,
		filepath.Join(root, "machines"),
		filepath.Join(root, "cache", "images"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return nil, fmt.Errorf("creating state dir %s: %w", d, err)
		}
	}
	return &Store{root: root}, nil
}

// Root returns the root state directory path.
func (s *Store) Root() string {
	return s.root
}

// MachineDir returns the directory for a specific machine.
func (s *Store) MachineDir(id string) string {
	return filepath.Join(s.root, "machines", id)
}

// Create persists a new machine record.
func (s *Store) Create(m *Machine) error {
	dir := s.MachineDir(m.ID)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("machine %q already exists", m.ID)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	m.CreatedAt = time.Now()
	m.UpdatedAt = m.CreatedAt
	return s.writeMeta(m)
}

// Get reads a machine record by ID.
func (s *Store) Get(id string) (*Machine, error) {
	data, err := os.ReadFile(s.metaPath(id))
	if err != nil {
		return nil, fmt.Errorf("machine %q not found: %w", id, err)
	}
	var m Machine
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("corrupt state for %q: %w", id, err)
	}
	return &m, nil
}

// Update overwrites a machine record.
func (s *Store) Update(m *Machine) error {
	if _, err := os.Stat(s.MachineDir(m.ID)); os.IsNotExist(err) {
		return fmt.Errorf("machine %q does not exist", m.ID)
	}
	m.UpdatedAt = time.Now()
	return s.writeMeta(m)
}

// Delete removes a machine's state directory entirely.
func (s *Store) Delete(id string) error {
	dir := s.MachineDir(id)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("machine %q does not exist", id)
	}
	return os.RemoveAll(dir)
}

// List returns all machine records.
func (s *Store) List() ([]*Machine, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "machines"))
	if err != nil {
		return nil, err
	}
	var machines []*Machine
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := s.Get(e.Name())
		if err != nil {
			continue // skip corrupt entries
		}
		machines = append(machines, m)
	}
	return machines, nil
}

// Lock acquires an advisory lock for a machine (simple file-based).
func (s *Store) Lock(id string) (func(), error) {
	lockPath := filepath.Join(s.MachineDir(id), ".lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("machine %q is locked by another process", id)
	}
	f.Close()
	return func() { os.Remove(lockPath) }, nil
}

func (s *Store) metaPath(id string) string {
	return filepath.Join(s.MachineDir(id), "machine.json")
}

func (s *Store) writeMeta(m *Machine) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.metaPath(m.ID), data, 0644)
}
