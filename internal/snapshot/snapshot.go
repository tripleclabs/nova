// Package snapshot implements cluster-level snapshot save, restore, list,
// delete, and OCI push/pull ("time travel").
package snapshot

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tripleclabs/nova/internal/state"
)

// Snapshot holds metadata about a saved cluster snapshot.
type Snapshot struct {
	Name      string             `json:"name"`
	Machines  []MachineMeta      `json:"machines"`
	CreatedAt time.Time          `json:"created_at"`
}

// MachineMeta captures the state of a single machine at snapshot time.
type MachineMeta struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	State      state.MachineState `json:"state"`
	ConfigHash string             `json:"config_hash"`
	DiskPath   string             `json:"disk_path"` // Relative to machine dir.
}

// Manager handles snapshot operations.
type Manager struct {
	store    *state.Store
	snapDir  string // e.g. ~/.nova/snapshots
}

// NewManager creates a snapshot Manager.
func NewManager(store *state.Store, novaDir string) (*Manager, error) {
	snapDir := filepath.Join(novaDir, "snapshots")
	if err := os.MkdirAll(snapDir, 0755); err != nil {
		return nil, fmt.Errorf("creating snapshots dir: %w", err)
	}
	return &Manager{store: store, snapDir: snapDir}, nil
}

// Save creates a named snapshot. If machineNames is empty, all machines are
// included. Otherwise only the named machines are snapshotted.
func (m *Manager) Save(name string, machineNames ...string) error {
	if err := validateName(name); err != nil {
		return err
	}

	// Check for existing snapshot with this name.
	if _, err := m.loadMeta(name); err == nil {
		return fmt.Errorf("snapshot %q already exists (delete it first)", name)
	}

	allMachines, err := m.store.List()
	if err != nil {
		return fmt.Errorf("listing machines: %w", err)
	}

	// Filter to requested machines if specified.
	var machines []*state.Machine
	if len(machineNames) > 0 {
		wanted := make(map[string]bool)
		for _, n := range machineNames {
			wanted[n] = true
		}
		for _, machine := range allMachines {
			if wanted[machine.ID] || wanted[machine.Name] {
				machines = append(machines, machine)
			}
		}
		if len(machines) == 0 {
			return fmt.Errorf("no matching machines found")
		}
	} else {
		machines = allMachines
	}

	if len(machines) == 0 {
		return fmt.Errorf("no machines to snapshot")
	}

	snap := Snapshot{
		Name:      name,
		CreatedAt: time.Now(),
	}

	for _, machine := range machines {
		diskPath := filepath.Join(m.store.MachineDir(machine.ID), "disk.qcow2")
		if _, err := os.Stat(diskPath); err != nil {
			return fmt.Errorf("disk not found for machine %q: %w", machine.ID, err)
		}

		// Create internal qcow2 snapshot.
		if err := qemuImgSnapshot(diskPath, name); err != nil {
			// Roll back any snapshots already created.
			m.rollbackSnapshot(snap.Machines, name)
			return fmt.Errorf("snapshotting %q: %w", machine.ID, err)
		}

		snap.Machines = append(snap.Machines, MachineMeta{
			ID:         machine.ID,
			Name:       machine.Name,
			State:      machine.State,
			ConfigHash: machine.ConfigHash,
			DiskPath:   "disk.qcow2",
		})
	}

	return m.saveMeta(snap)
}

// Restore reverts all machines in a snapshot to their saved state.
func (m *Manager) Restore(name string) error {
	snap, err := m.loadMeta(name)
	if err != nil {
		return fmt.Errorf("snapshot %q not found", name)
	}

	for _, mm := range snap.Machines {
		diskPath := filepath.Join(m.store.MachineDir(mm.ID), mm.DiskPath)
		if err := qemuImgApplySnapshot(diskPath, name); err != nil {
			return fmt.Errorf("restoring %q: %w", mm.ID, err)
		}
	}

	return nil
}

// List returns all saved snapshots.
func (m *Manager) List() ([]Snapshot, error) {
	entries, err := os.ReadDir(m.snapDir)
	if err != nil {
		return nil, err
	}
	var snaps []Snapshot
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.snapDir, e.Name()))
		if err != nil {
			continue
		}
		var s Snapshot
		if json.Unmarshal(data, &s) == nil {
			snaps = append(snaps, s)
		}
	}
	return snaps, nil
}

// Delete removes a snapshot and its internal qcow2 snapshots.
func (m *Manager) Delete(name string) error {
	snap, err := m.loadMeta(name)
	if err != nil {
		return fmt.Errorf("snapshot %q not found", name)
	}

	for _, mm := range snap.Machines {
		diskPath := filepath.Join(m.store.MachineDir(mm.ID), mm.DiskPath)
		// Best-effort: disk may have been nuked.
		qemuImgDeleteSnapshot(diskPath, name)
	}

	return os.Remove(m.metaPath(name))
}

// PackDir returns the directory where a snapshot is packed for push/pull.
func (m *Manager) PackDir(name string) string {
	return filepath.Join(m.snapDir, name+".pack")
}

// Pack exports a snapshot into a directory of standalone qcow2 files
// suitable for OCI push. Each machine's disk is exported as a full
// qcow2 image (not just the internal snapshot delta).
func (m *Manager) Pack(name string) (string, error) {
	snap, err := m.loadMeta(name)
	if err != nil {
		return "", fmt.Errorf("snapshot %q not found", name)
	}

	packDir := m.PackDir(name)
	if err := os.MkdirAll(packDir, 0755); err != nil {
		return "", err
	}

	// Copy metadata.
	metaData, _ := json.MarshalIndent(snap, "", "  ")
	os.WriteFile(filepath.Join(packDir, "snapshot.json"), metaData, 0644)

	for _, mm := range snap.Machines {
		srcDisk := filepath.Join(m.store.MachineDir(mm.ID), mm.DiskPath)
		dstDisk := filepath.Join(packDir, mm.ID+".qcow2")

		// Export the snapshot as a standalone qcow2.
		if err := qemuImgConvert(srcDisk, dstDisk, name); err != nil {
			os.RemoveAll(packDir)
			return "", fmt.Errorf("exporting %q: %w", mm.ID, err)
		}
	}

	return packDir, nil
}

// Unpack imports a packed snapshot directory into the local store.
func (m *Manager) Unpack(packDir string) error {
	data, err := os.ReadFile(filepath.Join(packDir, "snapshot.json"))
	if err != nil {
		return fmt.Errorf("reading snapshot metadata: %w", err)
	}

	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("parsing snapshot metadata: %w", err)
	}

	for _, mm := range snap.Machines {
		// Clear any existing machine state first.
		m.store.Delete(mm.ID)

		// Recreate the machine state record (this creates the dir).
		machine := &state.Machine{
			ID:         mm.ID,
			Name:       mm.Name,
			State:      state.StateStopped,
			ConfigHash: mm.ConfigHash,
		}
		if err := m.store.Create(machine); err != nil {
			return fmt.Errorf("creating machine record for %q: %w", mm.ID, err)
		}

		// Copy the disk into the machine dir.
		srcDisk := filepath.Join(packDir, mm.ID+".qcow2")
		dstDisk := filepath.Join(m.store.MachineDir(mm.ID), mm.DiskPath)
		if err := copyFile(srcDisk, dstDisk); err != nil {
			return fmt.Errorf("importing disk for %q: %w", mm.ID, err)
		}
	}

	return m.saveMeta(snap)
}

func (m *Manager) metaPath(name string) string {
	return filepath.Join(m.snapDir, name+".json")
}

func (m *Manager) saveMeta(snap Snapshot) error {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.metaPath(snap.Name), data, 0644)
}

func (m *Manager) loadMeta(name string) (*Snapshot, error) {
	data, err := os.ReadFile(m.metaPath(name))
	if err != nil {
		return nil, err
	}
	var s Snapshot
	return &s, json.Unmarshal(data, &s)
}

func (m *Manager) rollbackSnapshot(machines []MachineMeta, name string) {
	for _, mm := range machines {
		diskPath := filepath.Join(m.store.MachineDir(mm.ID), mm.DiskPath)
		qemuImgDeleteSnapshot(diskPath, name)
	}
}

func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("snapshot name is required")
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return fmt.Errorf("snapshot name must be alphanumeric with hyphens/underscores, got %q", name)
		}
	}
	return nil
}

// --- qemu-img wrappers ---

func qemuImgSnapshot(diskPath, snapName string) error {
	return runQemuImg("snapshot", "-c", snapName, diskPath)
}

func qemuImgApplySnapshot(diskPath, snapName string) error {
	return runQemuImg("snapshot", "-a", snapName, diskPath)
}

func qemuImgDeleteSnapshot(diskPath, snapName string) error {
	return runQemuImg("snapshot", "-d", snapName, diskPath)
}

func qemuImgConvert(srcDisk, dstDisk, snapName string) error {
	return runQemuImg("convert", "-f", "qcow2", "-O", "qcow2", "-l", "snapshot.name="+snapName, srcDisk, dstDisk)
}

func runQemuImg(args ...string) error {
	cmd := exec.Command("qemu-img", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("qemu-img %s: %s: %w", args[0], strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

// Export packs a snapshot and writes it as a gzipped tar archive to outputPath.
// The archive can be transferred to another machine and imported with Import().
func (m *Manager) Export(name, outputPath string) error {
	// Pack the snapshot into standalone files first.
	packDir, err := m.Pack(name)
	if err != nil {
		return fmt.Errorf("packing snapshot: %w", err)
	}
	defer os.RemoveAll(packDir)

	// Create the output tar.gz.
	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("creating output file: %w", err)
	}
	defer outFile.Close()

	gw := gzip.NewWriter(outFile)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	// Walk the pack directory and add all files.
	return filepath.Walk(packDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == packDir {
			return nil // skip root dir itself
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("creating tar header for %s: %w", path, err)
		}
		// Use relative path within the archive.
		relPath, _ := filepath.Rel(packDir, path)
		header.Name = relPath

		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("writing tar header: %w", err)
		}

		if info.IsDir() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		_, err = io.Copy(tw, f)
		return err
	})
}

// Import reads a gzipped tar archive produced by Export(), extracts it to a
// temporary directory, and unpacks the snapshot into the local store.
func (m *Manager) Import(archivePath string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("opening archive: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("reading gzip: %w", err)
	}
	defer gr.Close()

	// Extract to a temp directory.
	tmpDir, err := os.MkdirTemp("", "nova-snap-import-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tr := tar.NewReader(gr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}

		// Sanitize path to prevent directory traversal.
		target := filepath.Join(tmpDir, filepath.Clean(header.Name))
		if !strings.HasPrefix(target, tmpDir) {
			return fmt.Errorf("invalid tar entry path: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			out, err := os.Create(target)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}

	// Unpack into the store using the existing infrastructure.
	return m.Unpack(tmpDir)
}

func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}
	return dstFile.Close()
}
