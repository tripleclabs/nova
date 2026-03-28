package snapshot

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/3clabs/nova/internal/state"
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
