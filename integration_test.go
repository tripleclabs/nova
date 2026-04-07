//go:build integration

// Package nova_test provides end-to-end integration tests that exercise the
// full VM lifecycle against a real hypervisor. These tests download a small
// cloud image, boot it, verify SSH connectivity, and tear it down.
//
// Run with: make integration
// Requires: qemu-img, network access (first run only, image is cached)
package nova_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tripleclabs/nova/pkg/novatest"
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
func alpineImageURL() string {
	switch runtime.GOARCH {
	case "arm64":
		return "https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/cloud/generic_alpine-3.21.1-aarch64-uefi-cloudinit-r0.qcow2"
	default:
		return "https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/cloud/generic_alpine-3.21.1-x86_64-uefi-cloudinit-r0.qcow2"
	}
}

// alpineImagePath returns a persistent path for the downloaded Alpine image.
func alpineImagePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(projectRoot(t), ".cache", "alpine-cloudinit.qcow2")
}

// ensureAlpineImage downloads the Alpine cloud image (if needed) and builds it
// into the nova cache.
func ensureAlpineImage(t *testing.T) {
	t.Helper()

	out := nova(t, "image", "list")
	if strings.Contains(out, "nova.local/alpine:3.23") {
		t.Log("Alpine image already in nova cache")
		return
	}

	imgPath := alpineImagePath(t)

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

	nova(t, "image", "build", imgPath, "--tag", "nova.local/alpine:3.23")

	out = nova(t, "image", "list")
	if !strings.Contains(out, "nova.local/alpine:3.23") {
		t.Fatalf("Alpine image not in cache after build:\n%s", out)
	}
}

// --- CLI tests (no VMs needed) ---

func TestIntegration_Version(t *testing.T) {
	out := nova(t, "version")
	if !strings.Contains(out, "nova") {
		t.Errorf("version output: %s", out)
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

func TestIntegration_ImageBuildAndList(t *testing.T) {
	ensureAlpineImage(t)

	out := nova(t, "image", "list")
	if !strings.Contains(out, "nova.local/alpine:3.23") {
		t.Errorf("image list should contain alpine:\n%s", out)
	}
}

// Note: TestIntegration_StatusEmpty was removed — it depended on global
// ~/.nova state and would fail when user VMs were running. Status is
// implicitly tested via novatest.NewCluster which calls Apply + Status.

func TestIntegration_DownNonExistent(t *testing.T) {
	bin := novaBinary(t)
	cmd := exec.Command(bin, "down", "does-not-exist")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("down non-existent should fail")
	}
	_ = fmt.Sprintf("%s", out)
}

// --- Snapshot tests (need qemu-img, not full VMs) ---

// Note: TestIntegration_SnapshotLifecycle was removed — it wrote directly to
// ~/.nova which conflicts with user state and other running VMs. Snapshot
// save/restore/delete/list/export/import are thoroughly covered by the isolated
// unit tests in internal/snapshot/snapshot_test.go (25 tests, all using temp dirs).

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

// --- VM lifecycle tests (using novatest SDK) ---

func TestIntegration_FullLifecycle(t *testing.T) {
	ensureAlpineImage(t)

	cluster := novatest.NewCluster(t, novatest.WithHCL(`
		vm {
			name   = "lifecycle-test"
			image  = "alpine:3.23"
			cpus   = 2
			memory = "1G"
		}
	`))
	cluster.WaitReady()

	// Verify the VM is running and reachable.
	out := cluster.Node("lifecycle-test").Exec("echo alive")
	if !strings.Contains(out, "alive") {
		t.Errorf("VM not reachable: %q", out)
	}

	// Stop and verify.
	cluster.Node("lifecycle-test").Stop()
	if cluster.Node("lifecycle-test").IsRunning() {
		t.Error("VM should be stopped after Stop()")
	}

	// Cleanup is automatic via t.Cleanup().
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
