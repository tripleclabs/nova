//go:build integration

package nova_test

import (
	"strings"
	"testing"

	"github.com/tripleclabs/nova/pkg/novatest"
)

// TestIntegration_SingleVM_SSH boots a single Ubuntu VM and verifies SSH works.
func TestIntegration_SingleVM_SSH(t *testing.T) {
	cluster := novatest.NewCluster(t, novatest.WithHCL(`
		vm {
			name   = "ssh-test"
			image  = "ubuntu:24.04"
			cpus   = 2
			memory = "2G"
		}
	`))
	cluster.WaitReady()

	// Basic SSH connectivity.
	out := cluster.Node("ssh-test").Exec("echo hello-from-nova")
	if !strings.Contains(out, "hello-from-nova") {
		t.Errorf("expected 'hello-from-nova' in output, got: %q", out)
	}

	// Verify hostname was set by cloud-init.
	hostname := cluster.Node("ssh-test").Exec("hostname")
	if !strings.Contains(hostname, "ssh-test") {
		t.Errorf("hostname = %q, want 'ssh-test'", strings.TrimSpace(hostname))
	}

	// Verify the nova user exists.
	whoami := cluster.Node("ssh-test").Exec("whoami")
	if !strings.Contains(whoami, "nova") {
		t.Errorf("whoami = %q, want 'nova'", strings.TrimSpace(whoami))
	}

	// Verify sudo works.
	sudo := cluster.Node("ssh-test").Exec("sudo id")
	if !strings.Contains(sudo, "uid=0") {
		t.Errorf("sudo id should show root, got: %q", sudo)
	}
}

// TestIntegration_MultiNode_SSH boots a 2-node cluster and verifies
// both nodes are reachable via SSH with correct hostnames.
// Note: inter-VM networking requires Linux (QEMU socket multicast) or
// macOS vmnet (not yet implemented). This test verifies multi-node boot
// and host-to-guest SSH only.
func TestIntegration_MultiNode_SSH(t *testing.T) {
	cluster := novatest.NewCluster(t, novatest.WithHCL(`
		defaults {
			image  = "ubuntu:24.04"
			cpus   = 2
			memory = "2G"
		}

		network {
			subnet = "10.0.0.0/24"
		}

		node "node1" {}
		node "node2" {}
	`))
	cluster.WaitReady()

	// Verify each node is reachable via SSH.
	out1 := cluster.Node("node1").Exec("echo node1-ok")
	if !strings.Contains(out1, "node1-ok") {
		t.Errorf("node1 SSH failed: %q", out1)
	}

	out2 := cluster.Node("node2").Exec("echo node2-ok")
	if !strings.Contains(out2, "node2-ok") {
		t.Errorf("node2 SSH failed: %q", out2)
	}

	// Verify hostnames set by cloud-init.
	h1 := strings.TrimSpace(cluster.Node("node1").Exec("hostname"))
	h2 := strings.TrimSpace(cluster.Node("node2").Exec("hostname"))
	if h1 != "node1" {
		t.Errorf("node1 hostname = %q", h1)
	}
	if h2 != "node2" {
		t.Errorf("node2 hostname = %q", h2)
	}

	// Verify both have sudo.
	cluster.Node("node1").Exec("sudo id")
	cluster.Node("node2").Exec("sudo id")
}

// TestIntegration_Provisioner_Runs verifies that shell provisioners execute
// after VM boot.
func TestIntegration_Provisioner_Runs(t *testing.T) {
	cluster := novatest.NewCluster(t, novatest.WithHCL(`
		vm {
			name   = "prov-test"
			image  = "ubuntu:24.04"
			cpus   = 2
			memory = "2G"

			provisioner "shell" {
				inline = [
					"echo provisioner-was-here > /tmp/proof",
				]
			}
		}
	`))
	cluster.WaitReady()

	// Verify the provisioner ran.
	out := cluster.Node("prov-test").Exec("cat /tmp/proof")
	if !strings.Contains(out, "provisioner-was-here") {
		t.Errorf("provisioner proof not found, got: %q", out)
	}
}
