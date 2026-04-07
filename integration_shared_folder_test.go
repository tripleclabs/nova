//go:build integration

package nova_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tripleclabs/nova/pkg/novatest"
)

// TestIntegration_SharedFolder_HostWriteGuestRead writes a file on the host
// before boot and verifies it is visible inside the guest.
func TestIntegration_SharedFolder_HostWriteGuestRead(t *testing.T) {
	ensureAlpineImage(t)

	hostDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostDir, "hello.txt"), []byte("hello-from-host\n"), 0644); err != nil {
		t.Fatalf("writing host file: %v", err)
	}

	cluster := novatest.NewCluster(t, novatest.WithHCL(fmt.Sprintf(`
		vm {
			name   = "sf-host-write"
			image  = "alpine:3.23"
			cpus   = 2
			memory = "1G"

			shared_folder {
				host_path  = %q
				guest_path = "/mnt/share"
			}
		}
	`, hostDir)))
	cluster.WaitReady()

	out := cluster.Node("sf-host-write").Exec("cat /mnt/share/hello.txt")
	if !strings.Contains(out, "hello-from-host") {
		t.Errorf("guest did not see host file: %q", out)
	}
}

// TestIntegration_SharedFolder_GuestWriteHostRead writes a file from inside
// the guest and verifies it appears on the host filesystem.
func TestIntegration_SharedFolder_GuestWriteHostRead(t *testing.T) {
	ensureAlpineImage(t)

	hostDir := t.TempDir()

	cluster := novatest.NewCluster(t, novatest.WithHCL(fmt.Sprintf(`
		vm {
			name   = "sf-guest-write"
			image  = "alpine:3.23"
			cpus   = 2
			memory = "1G"

			shared_folder {
				host_path  = %q
				guest_path = "/mnt/share"
			}
		}
	`, hostDir)))
	cluster.WaitReady()

	cluster.Node("sf-guest-write").Exec("echo hello-from-guest > /mnt/share/guest.txt")

	novatest.Eventually(t, 10*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(hostDir, "guest.txt"))
		return err == nil
	})

	data, err := os.ReadFile(filepath.Join(hostDir, "guest.txt"))
	if err != nil {
		t.Fatalf("reading guest-written file on host: %v", err)
	}
	if !strings.Contains(string(data), "hello-from-guest") {
		t.Errorf("host did not see guest file contents: %q", string(data))
	}
}

// TestIntegration_SharedFolder_ReadOnly mounts a folder read-only and verifies
// the guest can read existing files but cannot write new ones.
func TestIntegration_SharedFolder_ReadOnly(t *testing.T) {
	ensureAlpineImage(t)

	hostDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostDir, "existing.txt"), []byte("read-only content\n"), 0644); err != nil {
		t.Fatalf("writing host file: %v", err)
	}

	cluster := novatest.NewCluster(t, novatest.WithHCL(fmt.Sprintf(`
		vm {
			name   = "sf-readonly"
			image  = "alpine:3.23"
			cpus   = 2
			memory = "1G"

			shared_folder {
				host_path  = %q
				guest_path = "/mnt/ro"
				read_only  = true
			}
		}
	`, hostDir)))
	cluster.WaitReady()

	// Existing file must be readable.
	out := cluster.Node("sf-readonly").Exec("cat /mnt/ro/existing.txt")
	if !strings.Contains(out, "read-only content") {
		t.Errorf("guest could not read existing file in ro mount: %q", out)
	}

	// Write attempt must fail.
	cluster.Node("sf-readonly").ExecExpectFailure("touch /mnt/ro/should-fail")

	// Confirm the file was not created on the host.
	if _, err := os.Stat(filepath.Join(hostDir, "should-fail")); err == nil {
		t.Error("write to read-only mount unexpectedly succeeded: file exists on host")
	}
}

// TestIntegration_SharedFolder_Multiple configures two independent shared
// folders and verifies each mounts to a distinct guest path.
func TestIntegration_SharedFolder_Multiple(t *testing.T) {
	ensureAlpineImage(t)

	dirA := t.TempDir()
	dirB := t.TempDir()

	if err := os.WriteFile(filepath.Join(dirA, "file-a.txt"), []byte("content-a\n"), 0644); err != nil {
		t.Fatalf("writing dirA file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "file-b.txt"), []byte("content-b\n"), 0644); err != nil {
		t.Fatalf("writing dirB file: %v", err)
	}

	cluster := novatest.NewCluster(t, novatest.WithHCL(fmt.Sprintf(`
		vm {
			name   = "sf-multi"
			image  = "alpine:3.23"
			cpus   = 2
			memory = "1G"

			shared_folder {
				host_path  = %q
				guest_path = "/mnt/folder-a"
			}

			shared_folder {
				host_path  = %q
				guest_path = "/mnt/folder-b"
			}
		}
	`, dirA, dirB)))
	cluster.WaitReady()

	outA := cluster.Node("sf-multi").Exec("cat /mnt/folder-a/file-a.txt")
	if !strings.Contains(outA, "content-a") {
		t.Errorf("folder-a: unexpected content: %q", outA)
	}

	outB := cluster.Node("sf-multi").Exec("cat /mnt/folder-b/file-b.txt")
	if !strings.Contains(outB, "content-b") {
		t.Errorf("folder-b: unexpected content: %q", outB)
	}

	// Each folder should only contain its own files.
	if r := cluster.Node("sf-multi").ExecResult("ls /mnt/folder-a/file-b.txt"); r.ExitCode == 0 {
		t.Error("file-b.txt unexpectedly visible in folder-a")
	}
	if r := cluster.Node("sf-multi").ExecResult("ls /mnt/folder-b/file-a.txt"); r.ExitCode == 0 {
		t.Error("file-a.txt unexpectedly visible in folder-b")
	}

	// Guest writes to both; confirm both appear on host.
	cluster.Node("sf-multi").Exec("echo guest-wrote-a > /mnt/folder-a/from-guest.txt")
	cluster.Node("sf-multi").Exec("echo guest-wrote-b > /mnt/folder-b/from-guest.txt")

	novatest.Eventually(t, 10*time.Second, func() bool {
		_, errA := os.Stat(filepath.Join(dirA, "from-guest.txt"))
		_, errB := os.Stat(filepath.Join(dirB, "from-guest.txt"))
		return errA == nil && errB == nil
	})
}
