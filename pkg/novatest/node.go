package novatest

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	pb "github.com/tripleclabs/nova/pkg/novapb/nova/v1"
)

// Node represents a single VM in the test cluster.
type Node struct {
	Name    string
	IP      string
	OS      string // Detected OS family (e.g. "ubuntu", "alpine").
	cluster *Cluster
}

// Exec runs a command on the node via SSH and returns stdout.
// Fails the test on non-zero exit or error.
func (n *Node) Exec(command string) string {
	n.cluster.t.Helper()
	result := n.ExecResult(command)
	if result.ExitCode != 0 {
		n.cluster.t.Fatalf("novatest: %s: exec %q exited %d\nstdout: %s\nstderr: %s",
			n.Name, command, result.ExitCode, result.Stdout, result.Stderr)
	}
	return result.Stdout
}

// ExecResult runs a command on the node and returns the full result
// without failing the test on non-zero exit.
func (n *Node) ExecResult(command string) ExecResult {
	return n.ExecResultWithTimeout(command, 30*time.Second)
}

// ExecResultWithTimeout runs a command with a custom timeout.
func (n *Node) ExecResultWithTimeout(command string, timeout time.Duration) ExecResult {
	n.cluster.t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Second)
	defer cancel()

	resp, err := n.cluster.client.Exec(ctx, &pb.ExecRequest{
		Node:    n.Name,
		Command: command,
		Timeout: durationpb.New(timeout),
	})
	if err != nil {
		n.cluster.t.Fatalf("novatest: %s: exec %q: %v", n.Name, command, err)
	}

	return ExecResult{
		ExitCode: int(resp.ExitCode),
		Stdout:   resp.Stdout,
		Stderr:   resp.Stderr,
	}
}

// ExecResult holds the output of a command execution.
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// ExecExpectFailure runs a command and fails the test if it exits 0.
// Returns the full ExecResult for further inspection.
func (n *Node) ExecExpectFailure(command string) ExecResult {
	n.cluster.t.Helper()
	result := n.ExecResult(command)
	if result.ExitCode == 0 {
		n.cluster.t.Errorf("novatest: %s: exec %q expected failure but exited 0\nstdout: %s\nstderr: %s",
			n.Name, command, result.Stdout, result.Stderr)
	}
	return result
}

// Stop gracefully stops the node.
func (n *Node) Stop() {
	n.cluster.t.Helper()
	_, err := n.cluster.client.NodeStop(context.Background(), &pb.NodeRequest{Name: n.Name})
	if err != nil {
		n.cluster.t.Fatalf("novatest: stop %s: %v", n.Name, err)
	}
}

// Start starts a previously stopped node.
func (n *Node) Start() {
	n.cluster.t.Helper()
	_, err := n.cluster.client.NodeStart(context.Background(), &pb.NodeRequest{Name: n.Name})
	if err != nil {
		n.cluster.t.Fatalf("novatest: start %s: %v", n.Name, err)
	}
}

// Kill force-terminates the node, simulating a power failure.
// The node's disk state is preserved but marked as error.
func (n *Node) Kill() {
	n.cluster.t.Helper()
	_, err := n.cluster.client.NodeKill(context.Background(), &pb.NodeRequest{Name: n.Name})
	if err != nil {
		n.cluster.t.Fatalf("novatest: kill %s: %v", n.Name, err)
	}
}

// IsRunning returns whether the node is currently in the "running" state.
func (n *Node) IsRunning() bool {
	resp, err := n.cluster.client.NodeStatus(context.Background(), &pb.NodeRequest{Name: n.Name})
	if err != nil {
		return false
	}
	return resp.State == "running"
}
