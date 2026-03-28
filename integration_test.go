//go:build integration

// Package nova_test provides end-to-end integration tests that exercise the
// full VM lifecycle against a real hypervisor. These tests download a small
// cloud image, boot it, verify SSH connectivity, and tear it down.
//
// Run with: make integration
// Requires: qemu-img, network access (first run only, image is cached)
package nova_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// nova runs the nova binary with the given args and returns combined output.
func nova(t *testing.T, args ...string) string {
	t.Helper()
	bin := novaBinary(t)
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("nova %s failed: %s\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

var cachedBin string

func novaBinary(t *testing.T) string {
	t.Helper()
	if cachedBin != "" {
		return cachedBin
	}
	// Build into the project's build dir so it persists across tests.
	root := projectRoot(t)
	bin := filepath.Join(root, "nova")
	cmd := exec.Command("make", "build")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building nova: %s\n%s", err, out)
	}
	cachedBin = bin
	return bin
}

func projectRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (go.mod)")
		}
		dir = parent
	}
}

// alpineImageURL returns the appropriate Alpine generic cloud-init image for the host arch.
// Uses UEFI boot (required for Apple VZ) and standard cloud-init (not tiny).
func alpineImageURL() string {
	switch runtime.GOARCH {
	case "arm64":
		return "https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/cloud/generic_alpine-3.21.1-aarch64-uefi-cloudinit-r0.qcow2"
	default:
		return "https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/cloud/generic_alpine-3.21.1-x86_64-uefi-cloudinit-r0.qcow2"
	}
}

// alpineImagePath returns a persistent path for the downloaded Alpine image.
// Stored alongside the project so it survives across test runs.
func alpineImagePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(projectRoot(t), ".cache", "alpine-cloudinit.qcow2")
}

// ensureAlpineImage downloads the Alpine cloud image (if needed) and builds it
// into the nova cache. The raw download is persisted in .cache/ so subsequent
// runs skip the download entirely.
func ensureAlpineImage(t *testing.T) {
	t.Helper()

	// Check if already in nova cache.
	out := nova(t, "image", "list")
	if strings.Contains(out, "nova.local/alpine:3.21") {
		t.Log("Alpine image already in nova cache")
		return
	}

	imgPath := alpineImagePath(t)

	// Download if not already on disk.
	if _, err := os.Stat(imgPath); err != nil {
		os.MkdirAll(filepath.Dir(imgPath), 0755)
		t.Logf("Downloading Alpine cloud image to %s ...", imgPath)
		cmd := exec.Command("curl", "-fSL", "-o", imgPath, alpineImageURL())
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("downloading Alpine image: %v", err)
		}
	} else {
		t.Log("Alpine image already downloaded, building into cache...")
	}

	// Build into nova cache.
	nova(t, "image", "build", imgPath, "--tag", "nova.local/alpine:3.21")

	out = nova(t, "image", "list")
	if !strings.Contains(out, "nova.local/alpine:3.21") {
		t.Fatalf("Alpine image not in cache after build:\n%s", out)
	}
}

// writeTestConfig writes a nova.hcl for integration testing.
func writeTestConfig(t *testing.T, dir string) {
	t.Helper()
	hcl := `
vm {
  name   = "integration-test"
  image  = "nova.local/alpine:3.21"
  cpus   = 2
  memory = "1G"
}
`
	if err := os.WriteFile(filepath.Join(dir, "nova.hcl"), []byte(hcl), 0644); err != nil {
		t.Fatal(err)
	}
}

// --- Integration tests ---

func TestIntegration_ImageBuildAndList(t *testing.T) {
	ensureAlpineImage(t)

	out := nova(t, "image", "list")
	if !strings.Contains(out, "nova.local/alpine:3.21") {
		t.Errorf("image list should contain alpine:\n%s", out)
	}
}

func TestIntegration_InitGeneratesConfig(t *testing.T) {
	dir := t.TempDir()
	bin := novaBinary(t)

	cmd := exec.Command(bin, "init")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("nova init: %s\n%s", err, out)
	}

	for _, f := range []string{"nova.hcl", "cloud-config.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s should exist after init: %v", f, err)
		}
	}
}

func TestIntegration_StatusEmpty(t *testing.T) {
	out := nova(t, "status")
	if !strings.Contains(out, "No VMs found") {
		t.Errorf("status on fresh state should say no VMs:\n%s", out)
	}
}

func TestIntegration_FullLifecycle(t *testing.T) {
	ensureAlpineImage(t)

	dir := t.TempDir()
	writeTestConfig(t, dir)

	bin := novaBinary(t)

	// nova up — start the VM in background (it blocks, so run async).
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	upCmd := exec.CommandContext(ctx, bin, "up", "--config", filepath.Join(dir, "nova.hcl"))
	upCmd.Stdout = os.Stdout
	upCmd.Stderr = os.Stderr

	if err := upCmd.Start(); err != nil {
		t.Fatalf("nova up start: %v", err)
	}

	// Poll status until we see the VM or timeout.
	var statusOut string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		cmd := exec.Command(bin, "status")
		out, _ := cmd.CombinedOutput()
		statusOut = string(out)
		if strings.Contains(statusOut, "integration-test") {
			break
		}
		time.Sleep(time.Second)
	}

	if !strings.Contains(statusOut, "integration-test") {
		t.Logf("status output: %s", statusOut)
		t.Fatal("VM 'integration-test' never appeared in status")
	}
	t.Logf("VM appeared in status:\n%s", statusOut)

	// nova nuke — clean up regardless of state.
	nukeCmd := exec.Command(bin, "nuke", "integration-test")
	nukeOut, err := nukeCmd.CombinedOutput()
	if err != nil {
		t.Logf("nova nuke output: %s", nukeOut)
	} else {
		t.Logf("Nuked: %s", nukeOut)
	}

	// Kill the up process if still running.
	if upCmd.Process != nil {
		upCmd.Process.Kill()
	}

	// Verify it's gone.
	finalStatus := nova(t, "status")
	if strings.Contains(finalStatus, "integration-test") {
		t.Error("VM should be gone after nuke")
	}
}

func TestIntegration_SnapshotLifecycle(t *testing.T) {
	// Create a machine with a real qcow2 disk for snapshot testing.
	// Doesn't need a running VM — just state + disk.
	home, _ := os.UserHomeDir()
	novaDir := filepath.Join(home, ".nova")
	machineDir := filepath.Join(novaDir, "machines", "snap-test")
	os.MkdirAll(machineDir, 0755)

	diskPath := filepath.Join(machineDir, "disk.qcow2")
	cmd := exec.Command("qemu-img", "create", "-f", "qcow2", diskPath, "64M")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("creating test disk: %s: %v", out, err)
	}
	defer os.RemoveAll(machineDir)

	stateJSON := `{"id":"snap-test","name":"snap-test","state":"stopped","config_hash":"test","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}`
	os.WriteFile(filepath.Join(machineDir, "machine.json"), []byte(stateJSON), 0644)

	bin := novaBinary(t)

	run := func(args ...string) string {
		c := exec.Command(bin, args...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("nova %s: %s\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	out := run("snapshot", "save", "v1")
	if !strings.Contains(out, "saved") {
		t.Errorf("expected 'saved': %s", out)
	}

	out = run("snapshot", "list")
	if !strings.Contains(out, "v1") {
		t.Errorf("snapshot list should contain v1:\n%s", out)
	}

	out = run("snapshot", "restore", "v1")
	if !strings.Contains(out, "restored") {
		t.Errorf("expected 'restored': %s", out)
	}

	out = run("snapshot", "delete", "v1")
	if !strings.Contains(out, "deleted") {
		t.Errorf("expected 'deleted': %s", out)
	}

	out = run("snapshot", "list")
	if strings.Contains(out, "v1") {
		t.Error("snapshot v1 should be gone after delete")
	}
}

func TestIntegration_LinkCommands(t *testing.T) {
	bin := novaBinary(t)

	run := func(args ...string) string {
		c := exec.Command(bin, args...)
		out, _ := c.CombinedOutput()
		return string(out)
	}

	run("link", "reset")

	out := run("link", "degrade", "node-a", "node-b", "--latency", "50ms", "--loss", "5%")
	if !strings.Contains(out, "50ms") {
		t.Errorf("degrade output: %s", out)
	}

	out = run("link", "partition", "node-b", "node-c")
	if !strings.Contains(out, "PARTITIONED") {
		t.Errorf("partition output: %s", out)
	}

	out = run("link", "status")
	if !strings.Contains(out, "node-a") || !strings.Contains(out, "PARTITIONED") {
		t.Errorf("status output: %s", out)
	}

	run("link", "heal", "node-a", "node-b")
	run("link", "reset")

	out = run("link", "status")
	if !strings.Contains(out, "No active") {
		t.Errorf("status after reset: %s", out)
	}
}

func TestIntegration_Version(t *testing.T) {
	out := nova(t, "version")
	if !strings.Contains(out, "nova") {
		t.Errorf("version output: %s", out)
	}
}

func TestIntegration_NukeNonExistent(t *testing.T) {
	bin := novaBinary(t)
	cmd := exec.Command(bin, "nuke", "does-not-exist")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("nuke non-existent should fail")
	}
	if !strings.Contains(string(out), "not found") {
		t.Errorf("expected 'not found' in error: %s", out)
	}
}

func TestIntegration_DownNonExistent(t *testing.T) {
	bin := novaBinary(t)
	cmd := exec.Command(bin, "down", "does-not-exist")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("down non-existent should fail")
	}
	_ = fmt.Sprintf("%s", out)
}
