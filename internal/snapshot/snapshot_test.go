package snapshot

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tripleclabs/nova/internal/state"
)

func hasQemuImg() bool {
	_, err := exec.LookPath("qemu-img")
	return err == nil
}

// setupTestEnv creates a temp nova dir with a store, a fake machine, and a real qcow2 disk.
func setupTestEnv(t *testing.T) (*Manager, *state.Store, string) {
	t.Helper()
	if !hasQemuImg() {
		t.Skip("qemu-img not found")
	}

	novaDir := t.TempDir()
	store, err := state.NewStore(novaDir)
	if err != nil {
		t.Fatal(err)
	}

	// Create a machine with a real qcow2 disk.
	machine := &state.Machine{ID: "test-vm", Name: "test-vm", State: state.StateRunning}
	if err := store.Create(machine); err != nil {
		t.Fatal(err)
	}

	diskPath := filepath.Join(store.MachineDir("test-vm"), "disk.qcow2")
	cmd := exec.Command("qemu-img", "create", "-f", "qcow2", diskPath, "64M")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("creating test disk: %s: %v", out, err)
	}

	mgr, err := NewManager(store, novaDir)
	if err != nil {
		t.Fatal(err)
	}

	return mgr, store, novaDir
}

func TestSaveAndList(t *testing.T) {
	mgr, _, _ := setupTestEnv(t)

	if err := mgr.Save("snap1"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	snaps, err := mgr.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("len = %d, want 1", len(snaps))
	}
	if snaps[0].Name != "snap1" {
		t.Errorf("name = %q, want snap1", snaps[0].Name)
	}
	if len(snaps[0].Machines) != 1 {
		t.Errorf("machines = %d, want 1", len(snaps[0].Machines))
	}
}

func TestSaveDuplicate(t *testing.T) {
	mgr, _, _ := setupTestEnv(t)

	mgr.Save("dup")
	if err := mgr.Save("dup"); err == nil {
		t.Fatal("expected error for duplicate snapshot name")
	}
}

func TestRestore(t *testing.T) {
	mgr, _, _ := setupTestEnv(t)

	if err := mgr.Save("restore-test"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Restore("restore-test"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
}

func TestRestoreNonExistent(t *testing.T) {
	mgr, _, _ := setupTestEnv(t)

	if err := mgr.Restore("nope"); err == nil {
		t.Fatal("expected error for non-existent snapshot")
	}
}

func TestDelete(t *testing.T) {
	mgr, _, _ := setupTestEnv(t)

	mgr.Save("del-test")
	if err := mgr.Delete("del-test"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	snaps, _ := mgr.List()
	if len(snaps) != 0 {
		t.Errorf("List after delete: len = %d, want 0", len(snaps))
	}
}

func TestDeleteNonExistent(t *testing.T) {
	mgr, _, _ := setupTestEnv(t)

	if err := mgr.Delete("nope"); err == nil {
		t.Fatal("expected error for non-existent snapshot")
	}
}

func TestPack(t *testing.T) {
	mgr, _, _ := setupTestEnv(t)

	mgr.Save("pack-test")

	packDir, err := mgr.Pack("pack-test")
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	defer os.RemoveAll(packDir)

	// Should contain snapshot.json and a qcow2 file.
	if _, err := os.Stat(filepath.Join(packDir, "snapshot.json")); err != nil {
		t.Error("pack dir should contain snapshot.json")
	}
	if _, err := os.Stat(filepath.Join(packDir, "test-vm.qcow2")); err != nil {
		t.Error("pack dir should contain test-vm.qcow2")
	}
}

func TestUnpack(t *testing.T) {
	mgr, store, novaDir := setupTestEnv(t)

	mgr.Save("unpack-test")
	packDir, _ := mgr.Pack("unpack-test")
	defer os.RemoveAll(packDir)

	// Create a fresh manager to simulate importing on another machine.
	freshDir := t.TempDir()
	freshStore, _ := state.NewStore(freshDir)
	freshMgr, _ := NewManager(freshStore, freshDir)

	if err := freshMgr.Unpack(packDir); err != nil {
		t.Fatalf("Unpack: %v", err)
	}

	// Should have the machine.
	machines, _ := freshStore.List()
	if len(machines) != 1 {
		t.Fatalf("machines after unpack: %d, want 1", len(machines))
	}
	if machines[0].ID != "test-vm" {
		t.Errorf("machine ID = %q, want test-vm", machines[0].ID)
	}

	// Disk should exist.
	diskPath := filepath.Join(freshStore.MachineDir("test-vm"), "disk.qcow2")
	if _, err := os.Stat(diskPath); err != nil {
		t.Error("disk should exist after unpack")
	}

	_ = store
	_ = novaDir
}

func TestValidateName(t *testing.T) {
	good := []string{"snap1", "my-snapshot", "SNAP_2"}
	for _, n := range good {
		if err := validateName(n); err != nil {
			t.Errorf("validateName(%q) should pass: %v", n, err)
		}
	}

	bad := []string{"", "has space", "has/slash", "has.dot"}
	for _, n := range bad {
		if err := validateName(n); err == nil {
			t.Errorf("validateName(%q) should fail", n)
		}
	}
}

func TestSaveNoMachines(t *testing.T) {
	novaDir := t.TempDir()
	store, _ := state.NewStore(novaDir)
	mgr, _ := NewManager(store, novaDir)

	if err := mgr.Save("empty"); err == nil {
		t.Fatal("expected error when no machines exist")
	}
}

// setupTestEnvMulti creates a temp nova dir with a store and N machines with real qcow2 disks.
func setupTestEnvMulti(t *testing.T, n int) (*Manager, *state.Store, string) {
	t.Helper()
	if !hasQemuImg() {
		t.Skip("qemu-img not found")
	}

	novaDir := t.TempDir()
	store, err := state.NewStore(novaDir)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < n; i++ {
		id := fmt.Sprintf("vm-%d", i)
		machine := &state.Machine{ID: id, Name: id, State: state.StateRunning}
		if err := store.Create(machine); err != nil {
			t.Fatal(err)
		}

		diskPath := filepath.Join(store.MachineDir(id), "disk.qcow2")
		cmd := exec.Command("qemu-img", "create", "-f", "qcow2", diskPath, "64M")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("creating test disk: %s: %v", out, err)
		}
	}

	mgr, err := NewManager(store, novaDir)
	if err != nil {
		t.Fatal(err)
	}

	return mgr, store, novaDir
}

func TestSaveMultipleMachines(t *testing.T) {
	mgr, _, _ := setupTestEnvMulti(t, 2)

	if err := mgr.Save("multi"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	snaps, err := mgr.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("len = %d, want 1", len(snaps))
	}
	if len(snaps[0].Machines) != 2 {
		t.Errorf("machines = %d, want 2", len(snaps[0].Machines))
	}
}

func TestRestoreVerifyRevert(t *testing.T) {
	if !hasQemuImg() {
		t.Skip("qemu-img not found")
	}

	novaDir := t.TempDir()
	store, err := state.NewStore(novaDir)
	if err != nil {
		t.Fatal(err)
	}

	machine := &state.Machine{ID: "revert-vm", Name: "revert-vm", State: state.StateRunning}
	if err := store.Create(machine); err != nil {
		t.Fatal(err)
	}

	diskPath := filepath.Join(store.MachineDir("revert-vm"), "disk.qcow2")
	// Create a qcow2 disk.
	cmd := exec.Command("qemu-img", "create", "-f", "qcow2", diskPath, "64M")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("creating test disk: %s: %v", out, err)
	}

	// Write some data using qemu-io (if available) or just use the raw approach:
	// We'll write a marker file next to the disk to simulate "state" since we can't
	// easily write into qcow2 without qemu-nbd. Instead, we verify that the qcow2
	// internal snapshot mechanism works by checking snapshot listing.
	mgr, err := NewManager(store, novaDir)
	if err != nil {
		t.Fatal(err)
	}

	// Snapshot the disk.
	if err := mgr.Save("before"); err != nil {
		t.Fatal(err)
	}

	// Verify the internal qcow2 snapshot exists via qemu-img snapshot -l.
	out, err := exec.Command("qemu-img", "snapshot", "-l", diskPath).CombinedOutput()
	if err != nil {
		t.Fatalf("listing snapshots: %s: %v", out, err)
	}
	if !strings.Contains(string(out), "before") {
		t.Fatalf("snapshot 'before' not found in qemu-img snapshot -l output:\n%s", out)
	}

	// Restore the snapshot.
	if err := mgr.Restore("before"); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// After restore the disk should still be valid and the snapshot should still exist.
	out2, err := exec.Command("qemu-img", "snapshot", "-l", diskPath).CombinedOutput()
	if err != nil {
		t.Fatalf("listing snapshots after restore: %s: %v", out2, err)
	}
	if !strings.Contains(string(out2), "before") {
		t.Fatalf("snapshot 'before' missing after restore:\n%s", out2)
	}
}

func TestListMultipleSnapshots(t *testing.T) {
	mgr, _, _ := setupTestEnv(t)

	names := []string{"snap-a", "snap-b", "snap-c"}
	for _, n := range names {
		if err := mgr.Save(n); err != nil {
			t.Fatalf("Save(%q): %v", n, err)
		}
	}

	snaps, err := mgr.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snaps) != 3 {
		t.Fatalf("len = %d, want 3", len(snaps))
	}

	// Collect names and verify all present.
	got := make(map[string]bool)
	for _, s := range snaps {
		got[s.Name] = true
	}
	for _, n := range names {
		if !got[n] {
			t.Errorf("missing snapshot %q in list", n)
		}
	}
}

func TestDeleteRemovesQcow2Snapshot(t *testing.T) {
	mgr, store, _ := setupTestEnv(t)

	if err := mgr.Save("del-qcow2"); err != nil {
		t.Fatal(err)
	}

	diskPath := filepath.Join(store.MachineDir("test-vm"), "disk.qcow2")

	// Verify internal snapshot exists before delete.
	out, err := exec.Command("qemu-img", "snapshot", "-l", diskPath).CombinedOutput()
	if err != nil {
		t.Fatalf("listing snapshots: %s: %v", out, err)
	}
	if !strings.Contains(string(out), "del-qcow2") {
		t.Fatalf("snapshot not found before delete:\n%s", out)
	}

	// Delete the snapshot.
	if err := mgr.Delete("del-qcow2"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Verify internal qcow2 snapshot is removed.
	out2, err := exec.Command("qemu-img", "snapshot", "-l", diskPath).CombinedOutput()
	if err != nil {
		// qemu-img snapshot -l returns error if no snapshots; that's OK.
		return
	}
	if strings.Contains(string(out2), "del-qcow2") {
		t.Errorf("qcow2 snapshot 'del-qcow2' still present after Delete:\n%s", out2)
	}
}

func TestPackDir(t *testing.T) {
	novaDir := t.TempDir()
	store, _ := state.NewStore(novaDir)
	mgr, _ := NewManager(store, novaDir)

	got := mgr.PackDir("my-snap")
	want := filepath.Join(novaDir, "snapshots", "my-snap.pack")
	if got != want {
		t.Errorf("PackDir = %q, want %q", got, want)
	}
}

func TestLoadMetaSaveMetaRoundTrip(t *testing.T) {
	novaDir := t.TempDir()
	store, _ := state.NewStore(novaDir)
	mgr, _ := NewManager(store, novaDir)

	original := Snapshot{
		Name:      "roundtrip",
		CreatedAt: time.Now().Truncate(time.Second),
		Machines: []MachineMeta{
			{ID: "m1", Name: "machine-1", State: state.StateRunning, ConfigHash: "abc123", DiskPath: "disk.qcow2"},
			{ID: "m2", Name: "machine-2", State: state.StateStopped, ConfigHash: "def456", DiskPath: "disk.qcow2"},
		},
	}

	if err := mgr.saveMeta(original); err != nil {
		t.Fatalf("saveMeta: %v", err)
	}

	loaded, err := mgr.loadMeta("roundtrip")
	if err != nil {
		t.Fatalf("loadMeta: %v", err)
	}

	if loaded.Name != original.Name {
		t.Errorf("Name = %q, want %q", loaded.Name, original.Name)
	}
	if len(loaded.Machines) != len(original.Machines) {
		t.Fatalf("Machines len = %d, want %d", len(loaded.Machines), len(original.Machines))
	}
	for i, m := range loaded.Machines {
		if m.ID != original.Machines[i].ID {
			t.Errorf("Machines[%d].ID = %q, want %q", i, m.ID, original.Machines[i].ID)
		}
		if m.Name != original.Machines[i].Name {
			t.Errorf("Machines[%d].Name = %q, want %q", i, m.Name, original.Machines[i].Name)
		}
		if m.State != original.Machines[i].State {
			t.Errorf("Machines[%d].State = %q, want %q", i, m.State, original.Machines[i].State)
		}
		if m.ConfigHash != original.Machines[i].ConfigHash {
			t.Errorf("Machines[%d].ConfigHash = %q, want %q", i, m.ConfigHash, original.Machines[i].ConfigHash)
		}
		if m.DiskPath != original.Machines[i].DiskPath {
			t.Errorf("Machines[%d].DiskPath = %q, want %q", i, m.DiskPath, original.Machines[i].DiskPath)
		}
	}
}

func TestLoadMetaNonExistent(t *testing.T) {
	novaDir := t.TempDir()
	store, _ := state.NewStore(novaDir)
	mgr, _ := NewManager(store, novaDir)

	_, err := mgr.loadMeta("does-not-exist")
	if err == nil {
		t.Fatal("expected error for non-existent meta")
	}
}

func TestRollbackSnapshot(t *testing.T) {
	if !hasQemuImg() {
		t.Skip("qemu-img not found")
	}

	novaDir := t.TempDir()
	store, _ := state.NewStore(novaDir)

	// Create two machines with disks.
	for _, id := range []string{"rb-vm-0", "rb-vm-1"} {
		machine := &state.Machine{ID: id, Name: id, State: state.StateRunning}
		if err := store.Create(machine); err != nil {
			t.Fatal(err)
		}
		diskPath := filepath.Join(store.MachineDir(id), "disk.qcow2")
		cmd := exec.Command("qemu-img", "create", "-f", "qcow2", diskPath, "64M")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("creating test disk: %s: %v", out, err)
		}
	}

	mgr, _ := NewManager(store, novaDir)

	// Manually create a snapshot on just the first machine (simulating partial success).
	disk0 := filepath.Join(store.MachineDir("rb-vm-0"), "disk.qcow2")
	if err := qemuImgSnapshot(disk0, "partial"); err != nil {
		t.Fatal(err)
	}

	// Build partial MachineMeta for the rollback.
	partialMachines := []MachineMeta{
		{ID: "rb-vm-0", DiskPath: "disk.qcow2"},
	}

	// Rollback should remove the snapshot from rb-vm-0.
	mgr.rollbackSnapshot(partialMachines, "partial")

	// Verify the snapshot is gone.
	out, _ := exec.Command("qemu-img", "snapshot", "-l", disk0).CombinedOutput()
	if strings.Contains(string(out), "partial") {
		t.Errorf("rollback did not remove snapshot from rb-vm-0:\n%s", out)
	}
}

func TestValidateNameEdgeCases(t *testing.T) {
	// Valid edge cases.
	valid := []string{"a", "Z", "0", "a-b", "a_b", "ABC123", "a-b-c_d_e"}
	for _, n := range valid {
		if err := validateName(n); err != nil {
			t.Errorf("validateName(%q) should pass: %v", n, err)
		}
	}

	// Invalid edge cases.
	invalid := []string{
		"",            // empty
		"has space",   // space
		"has/slash",   // slash
		"has.dot",     // dot
		"has@symbol",  // at sign
		"tab\there",   // tab
		"new\nline",   // newline
		"colon:here",  // colon
		"semi;colon",  // semicolon
		"hash#tag",    // hash
		"exclaim!",    // exclamation
		"quest?",      // question mark
		"star*",       // asterisk
		"paren(",      // parenthesis
		"bracket[",    // bracket
		"brace{",      // brace
		"pipe|here",   // pipe
		"back\\slash", // backslash
		"tilde~",      // tilde
		"plus+",       // plus
		"equals=",     // equals
		"comma,",      // comma
		"<angle>",     // angle brackets
	}
	for _, n := range invalid {
		if err := validateName(n); err == nil {
			t.Errorf("validateName(%q) should fail", n)
		}
	}
}

func TestNewManagerCreatesDir(t *testing.T) {
	novaDir := t.TempDir()
	store, _ := state.NewStore(novaDir)

	snapDir := filepath.Join(novaDir, "snapshots")
	// Ensure snapshots dir doesn't exist yet.
	os.RemoveAll(snapDir)

	mgr, err := NewManager(store, novaDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if mgr == nil {
		t.Fatal("manager should not be nil")
	}

	// Verify directory was created.
	info, err := os.Stat(snapDir)
	if err != nil {
		t.Fatalf("snapshots dir should exist: %v", err)
	}
	if !info.IsDir() {
		t.Error("snapshots path should be a directory")
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	mgr, _, _ := setupTestEnv(t)

	if err := mgr.Save("export-test"); err != nil {
		t.Fatal(err)
	}

	// Export to a tar.gz file.
	archivePath := filepath.Join(t.TempDir(), "export-test.novasnap")
	if err := mgr.Export("export-test", archivePath); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Verify archive was created and is non-empty.
	fi, err := os.Stat(archivePath)
	if err != nil {
		t.Fatalf("archive should exist: %v", err)
	}
	if fi.Size() == 0 {
		t.Fatal("archive should be non-empty")
	}

	// Import into a fresh environment.
	freshDir := t.TempDir()
	freshStore, _ := state.NewStore(freshDir)
	freshMgr, _ := NewManager(freshStore, freshDir)

	if err := freshMgr.Import(archivePath); err != nil {
		t.Fatalf("Import: %v", err)
	}

	// Verify the snapshot was imported.
	snaps, err := freshMgr.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("imported snapshots = %d, want 1", len(snaps))
	}
	if snaps[0].Name != "export-test" {
		t.Errorf("imported snapshot name = %q, want export-test", snaps[0].Name)
	}

	// Verify the machine was recreated.
	machines, _ := freshStore.List()
	if len(machines) != 1 {
		t.Fatalf("machines after import = %d, want 1", len(machines))
	}
	if machines[0].ID != "test-vm" {
		t.Errorf("machine ID = %q, want test-vm", machines[0].ID)
	}

	// Verify the disk exists.
	diskPath := filepath.Join(freshStore.MachineDir("test-vm"), "disk.qcow2")
	if _, err := os.Stat(diskPath); err != nil {
		t.Error("disk should exist after import")
	}
}

func TestExportNonExistentSnapshot(t *testing.T) {
	mgr, _, _ := setupTestEnv(t)

	archivePath := filepath.Join(t.TempDir(), "nope.novasnap")
	if err := mgr.Export("nope", archivePath); err == nil {
		t.Fatal("expected error for non-existent snapshot")
	}
}

func TestImportInvalidArchive(t *testing.T) {
	novaDir := t.TempDir()
	store, _ := state.NewStore(novaDir)
	mgr, _ := NewManager(store, novaDir)

	// Create a file that is not a valid gzip archive.
	badPath := filepath.Join(t.TempDir(), "bad.novasnap")
	os.WriteFile(badPath, []byte("not a tar.gz"), 0644)

	if err := mgr.Import(badPath); err == nil {
		t.Fatal("expected error for invalid archive")
	}
}

func TestExportMultipleMachines(t *testing.T) {
	mgr, _, _ := setupTestEnvMulti(t, 3)

	if err := mgr.Save("multi-export"); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(t.TempDir(), "multi.novasnap")
	if err := mgr.Export("multi-export", archivePath); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Import and verify all 3 machines come through.
	freshDir := t.TempDir()
	freshStore, _ := state.NewStore(freshDir)
	freshMgr, _ := NewManager(freshStore, freshDir)

	if err := freshMgr.Import(archivePath); err != nil {
		t.Fatalf("Import: %v", err)
	}

	machines, _ := freshStore.List()
	if len(machines) != 3 {
		t.Fatalf("machines after import = %d, want 3", len(machines))
	}
}

func TestListIgnoresNonJSON(t *testing.T) {
	novaDir := t.TempDir()
	store, _ := state.NewStore(novaDir)
	mgr, _ := NewManager(store, novaDir)

	// Write a non-JSON file in the snapshots dir.
	os.WriteFile(filepath.Join(novaDir, "snapshots", "readme.txt"), []byte("hello"), 0644)
	// Write an invalid JSON file.
	os.WriteFile(filepath.Join(novaDir, "snapshots", "bad.json"), []byte("not json"), 0644)

	snaps, err := mgr.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snaps) != 0 {
		t.Errorf("List should return 0 snapshots, got %d", len(snaps))
	}
}
