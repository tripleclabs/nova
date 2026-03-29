// Package novatest provides a Go test SDK for Nova.
// It enables distributed systems testing by spinning up multi-node VM
// clusters, injecting network chaos, and executing commands — all from
// within standard Go tests.
//
// Example:
//
//	func TestMyApp(t *testing.T) {
//	    cluster := novatest.NewCluster(t, novatest.WithHCL(`
//	        defaults { image = "ubuntu:24.04" cpus = 2 memory = "2G" }
//	        node "n1" {}
//	        node "n2" {}
//	    `))
//	    cluster.WaitReady()
//	    cluster.Node("n1").Exec("echo hello")
//	    cluster.Partition("n1", "n2")
//	    cluster.HealAll()
//	}
package novatest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/tripleclabs/nova/pkg/novapb/nova/v1"
)

// Cluster is the primary handle for a Nova test environment.
// It manages the daemon lifecycle, VM creation, and guaranteed cleanup.
type Cluster struct {
	t          testing.TB
	conn       *grpc.ClientConn
	client     pb.NovaClient
	daemon     *exec.Cmd
	stateDir   string
	socketPath string
	nodes      map[string]*Node
}

// ClusterOption configures cluster creation.
type ClusterOption func(*clusterConfig)

type clusterConfig struct {
	hcl             string
	hclFile         string
	cloudConfigPath string
}

// WithHCL provides inline HCL configuration.
func WithHCL(hcl string) ClusterOption {
	return func(c *clusterConfig) { c.hcl = hcl }
}

// WithHCLFile loads configuration from a file path.
func WithHCLFile(path string) ClusterOption {
	return func(c *clusterConfig) { c.hclFile = path }
}

// WithCloudConfig provides a path to a cloud-config.yaml file.
func WithCloudConfig(path string) ClusterOption {
	return func(c *clusterConfig) { c.cloudConfigPath = path }
}

// NewCluster creates a test cluster, starts a daemon, boots all VMs,
// and registers cleanup to guarantee teardown. The daemon and all VMs
// are fully isolated from other tests via a temporary state directory.
func NewCluster(t testing.TB, opts ...ClusterOption) *Cluster {
	t.Helper()

	cfg := &clusterConfig{}
	for _, o := range opts {
		o(cfg)
	}

	if cfg.hcl == "" && cfg.hclFile == "" {
		t.Fatal("novatest: must provide WithHCL or WithHCLFile")
	}

	// Read HCL from file if needed.
	hcl := cfg.hcl
	if cfg.hclFile != "" {
		data, err := os.ReadFile(cfg.hclFile)
		if err != nil {
			t.Fatalf("novatest: reading HCL file: %v", err)
		}
		hcl = string(data)
	}

	// Use a short temp dir to avoid macOS 104-byte Unix socket path limit.
	// t.TempDir() produces paths like /var/folders/.../TestIntegration_MultiNode.../001/
	// which exceed the limit when daemon.sock is appended.
	stateDir, err := os.MkdirTemp("/tmp", "nova-t-")
	if err != nil {
		t.Fatalf("novatest: creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(stateDir) })

	// Symlink the shared image cache so we don't re-download images.
	home, _ := os.UserHomeDir()
	sharedCache := filepath.Join(home, ".nova", "cache")
	localCache := filepath.Join(stateDir, "cache")
	if _, err := os.Stat(sharedCache); err == nil {
		os.Symlink(sharedCache, localCache)
	}

	socketPath := filepath.Join(stateDir, "daemon.sock")

	c := &Cluster{
		t:          t,
		stateDir:   stateDir,
		socketPath: socketPath,
		nodes:      make(map[string]*Node),
	}

	// Register cleanup FIRST so it runs even if startup fails partway.
	t.Cleanup(c.destroy)

	// Start the daemon.
	c.startDaemon()

	// Connect gRPC client.
	c.connect()

	// Apply the configuration — boot all VMs.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	resp, err := c.client.Apply(ctx, &pb.ApplyRequest{
		HclConfig:       hcl,
		CloudConfigPath: cfg.cloudConfigPath,
	})
	if err != nil {
		t.Fatalf("novatest: Apply failed: %v", err)
	}

	for _, info := range resp.Nodes {
		c.nodes[info.Name] = &Node{
			Name:    info.Name,
			IP:      info.Ip,
			OS:      info.Os,
			cluster: c,
		}
	}

	t.Logf("novatest: cluster ready with %d node(s)", len(c.nodes))
	return c
}

// Node returns a handle to a specific node by name.
func (c *Cluster) Node(name string) *Node {
	c.t.Helper()
	n, ok := c.nodes[name]
	if !ok {
		c.t.Fatalf("novatest: node %q not found", name)
	}
	return n
}

// Nodes returns all nodes in the cluster.
func (c *Cluster) Nodes() []*Node {
	nodes := make([]*Node, 0, len(c.nodes))
	for _, n := range c.nodes {
		nodes = append(nodes, n)
	}
	return nodes
}

// WaitReady blocks until all nodes are reachable via SSH.
func (c *Cluster) WaitReady() {
	c.t.Helper()
	for _, n := range c.nodes {
		c.t.Logf("novatest: waiting for %s...", n.Name)
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		_, err := c.client.WaitReady(ctx, &pb.WaitReadyRequest{
			Node:    n.Name,
			Timeout: durationpb.New(45 * time.Second),
		})
		cancel()
		if err != nil {
			c.t.Fatalf("novatest: %s not ready: %v", n.Name, err)
		}
	}
}

// --- Network Chaos ---

// Partition creates a hard network partition between two nodes.
func (c *Cluster) Partition(a, b string) {
	c.t.Helper()
	_, err := c.client.LinkPartition(context.Background(), &pb.LinkPairRequest{NodeA: a, NodeB: b})
	if err != nil {
		c.t.Fatalf("novatest: partition %s<->%s: %v", a, b, err)
	}
}

// Heal removes all conditions between two nodes.
func (c *Cluster) Heal(a, b string) {
	c.t.Helper()
	_, err := c.client.LinkHeal(context.Background(), &pb.LinkPairRequest{NodeA: a, NodeB: b})
	if err != nil {
		c.t.Fatalf("novatest: heal %s<->%s: %v", a, b, err)
	}
}

// Degrade adds latency/jitter/loss to a link between two nodes.
func (c *Cluster) Degrade(a, b string, opts ...LinkOption) {
	c.t.Helper()
	lo := &linkOpts{}
	for _, o := range opts {
		o(lo)
	}
	_, err := c.client.LinkDegrade(context.Background(), &pb.LinkDegradeRequest{
		NodeA:   a,
		NodeB:   b,
		Latency: durationpb.New(lo.latency),
		Jitter:  durationpb.New(lo.jitter),
		Loss:    lo.loss,
	})
	if err != nil {
		c.t.Fatalf("novatest: degrade %s<->%s: %v", a, b, err)
	}
}

// HealAll removes all network conditions.
func (c *Cluster) HealAll() {
	c.t.Helper()
	_, err := c.client.LinkReset(context.Background(), &emptypb.Empty{})
	if err != nil {
		c.t.Fatalf("novatest: heal all: %v", err)
	}
}

// --- Snapshots ---

// Snapshot saves a named snapshot of all nodes.
func (c *Cluster) Snapshot(name string) {
	c.t.Helper()
	_, err := c.client.SnapshotSave(context.Background(), &pb.SnapshotRequest{Name: name})
	if err != nil {
		c.t.Fatalf("novatest: snapshot save %q: %v", name, err)
	}
}

// Restore reverts all nodes to a named snapshot.
func (c *Cluster) Restore(name string) {
	c.t.Helper()
	_, err := c.client.SnapshotRestore(context.Background(), &pb.SnapshotRequest{Name: name})
	if err != nil {
		c.t.Fatalf("novatest: snapshot restore %q: %v", name, err)
	}
}

// --- Internal ---

func (c *Cluster) startDaemon() {
	c.t.Helper()

	// Always build from source to avoid picking up unrelated binaries
	// with the same name (e.g., the Panic Nova text editor on macOS).
	// Use NOVA_BIN env var to override if a pre-built binary is available.
	novaBin := os.Getenv("NOVA_BIN")
	if novaBin == "" {
		novaBin = filepath.Join(os.TempDir(), "nova-test-"+fmt.Sprintf("%d", os.Getpid()))
		c.t.Logf("novatest: building nova binary at %s", novaBin)
		build := exec.Command("go", "build", "-o", novaBin, "./cmd/nova/")
		if out, err := build.CombinedOutput(); err != nil {
			c.t.Fatalf("novatest: building nova: %s\n%s", err, out)
		}
	}

	c.daemon = exec.Command(novaBin, "daemon", "--state-dir", c.stateDir)
	c.daemon.Stdout = os.Stdout
	c.daemon.Stderr = os.Stderr

	if err := c.daemon.Start(); err != nil {
		c.t.Fatalf("novatest: starting daemon: %v", err)
	}

	// Wait for the socket to appear.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(c.socketPath); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	c.t.Fatalf("novatest: daemon socket not found after 10s: %s", c.socketPath)
}

func (c *Cluster) connect() {
	c.t.Helper()

	conn, err := grpc.NewClient(
		"unix://"+c.socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		c.t.Fatalf("novatest: connecting to daemon: %v", err)
	}
	c.conn = conn
	c.client = pb.NewNovaClient(conn)
}

func (c *Cluster) destroy() {
	// Best-effort: tell the daemon to clean up.
	if c.client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		c.client.Shutdown(ctx, &emptypb.Empty{})
		cancel()
	}

	if c.conn != nil {
		c.conn.Close()
	}

	// Kill the daemon process if still alive.
	if c.daemon != nil && c.daemon.Process != nil {
		c.daemon.Process.Kill()
		c.daemon.Wait()
	}
}
