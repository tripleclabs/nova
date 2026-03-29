//go:build integration

package nova_test

import (
	"strings"
	"testing"
	"time"

	"github.com/3clabs/nova/pkg/novatest"
)

// TestIntegration_SingleVM_SSH boots a single Alpine VM and verifies SSH works.
func TestIntegration_SingleVM_SSH(t *testing.T) {
	ensureAlpineImage(t)

	cluster := novatest.NewCluster(t, novatest.WithHCL(`
		vm {
			name   = "ssh-test"
			image  = "alpine:3.21"
			cpus   = 2
			memory = "1G"
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

	// Verify the nova user exists and has sudo.
	whoami := cluster.Node("ssh-test").Exec("whoami")
	if !strings.Contains(whoami, "nova") {
		t.Errorf("whoami = %q, want 'nova'", strings.TrimSpace(whoami))
	}

	sudo := cluster.Node("ssh-test").Exec("sudo id")
	if !strings.Contains(sudo, "uid=0") {
		t.Errorf("sudo id should show root, got: %q", sudo)
	}
}

// TestIntegration_MultiNode_Networking boots a 2-node cluster and verifies
// inter-node connectivity via the configured static IPs.
func TestIntegration_MultiNode_Networking(t *testing.T) {
	ensureAlpineImage(t)

	cluster := novatest.NewCluster(t, novatest.WithHCL(`
		defaults {
			image  = "alpine:3.21"
			cpus   = 2
			memory = "1G"
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

	// Verify hostnames.
	h1 := strings.TrimSpace(cluster.Node("node1").Exec("hostname"))
	h2 := strings.TrimSpace(cluster.Node("node2").Exec("hostname"))
	if h1 != "node1" {
		t.Errorf("node1 hostname = %q", h1)
	}
	if h2 != "node2" {
		t.Errorf("node2 hostname = %q", h2)
	}

	// Verify each node has its static IP configured.
	ip1 := cluster.Node("node1").Exec("ip -4 addr show")
	if !strings.Contains(ip1, "10.0.0.2") {
		t.Errorf("node1 should have IP 10.0.0.2, got:\n%s", ip1)
	}

	ip2 := cluster.Node("node2").Exec("ip -4 addr show")
	if !strings.Contains(ip2, "10.0.0.3") {
		t.Errorf("node2 should have IP 10.0.0.3, got:\n%s", ip2)
	}

	// Verify inter-node connectivity: node1 pings node2 and vice versa.
	// Allow a few seconds for ARP to settle.
	novatest.Eventually(t, 30*time.Second, func() bool {
		result := cluster.Node("node1").ExecResult("ping -c1 -W2 10.0.0.3")
		return result.ExitCode == 0
	})

	novatest.Eventually(t, 30*time.Second, func() bool {
		result := cluster.Node("node2").ExecResult("ping -c1 -W2 10.0.0.2")
		return result.ExitCode == 0
	})

	// Verify /etc/hosts has the cluster entries.
	hosts1 := cluster.Node("node1").Exec("cat /etc/hosts")
	if !strings.Contains(hosts1, "node2") {
		t.Errorf("node1 /etc/hosts should contain node2:\n%s", hosts1)
	}

	hosts2 := cluster.Node("node2").Exec("cat /etc/hosts")
	if !strings.Contains(hosts2, "node1") {
		t.Errorf("node2 /etc/hosts should contain node1:\n%s", hosts2)
	}

	// Verify name resolution works: node1 can ping node2 by hostname.
	novatest.Eventually(t, 30*time.Second, func() bool {
		result := cluster.Node("node1").ExecResult("ping -c1 -W2 node2")
		return result.ExitCode == 0
	})
}

// TestIntegration_MultiNode_ThreeNodes boots a 3-node cluster to verify
// networking scales beyond two nodes.
func TestIntegration_MultiNode_ThreeNodes(t *testing.T) {
	ensureAlpineImage(t)

	cluster := novatest.NewCluster(t, novatest.WithHCL(`
		defaults {
			image  = "alpine:3.21"
			cpus   = 1
			memory = "512M"
		}

		network {
			subnet = "10.0.0.0/24"
		}

		node "web" {}
		node "app" {}
		node "db"  {}
	`))
	cluster.WaitReady()

	// All three nodes reachable.
	for _, name := range []string{"web", "app", "db"} {
		out := cluster.Node(name).Exec("echo " + name + "-alive")
		if !strings.Contains(out, name+"-alive") {
			t.Errorf("%s SSH failed: %q", name, out)
		}
	}

	// Full mesh connectivity: every node can ping every other node.
	nodes := []struct {
		name string
		ip   string
	}{
		{"web", "10.0.0.2"},
		{"app", "10.0.0.3"},
		{"db", "10.0.0.4"},
	}

	for _, src := range nodes {
		for _, dst := range nodes {
			if src.name == dst.name {
				continue
			}
			novatest.Eventually(t, 30*time.Second, func() bool {
				result := cluster.Node(src.name).ExecResult("ping -c1 -W2 " + dst.ip)
				return result.ExitCode == 0
			})
		}
	}
}

// TestIntegration_Provisioner_Runs verifies that shell provisioners execute
// after VM boot.
func TestIntegration_Provisioner_Runs(t *testing.T) {
	ensureAlpineImage(t)

	cluster := novatest.NewCluster(t, novatest.WithHCL(`
		vm {
			name   = "prov-test"
			image  = "alpine:3.21"
			cpus   = 2
			memory = "1G"

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
