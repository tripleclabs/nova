// Package cmd client.go provides shared gRPC client helpers for CLI commands
// that route through the daemon. It handles auto-starting the daemon and
// connecting to the Unix domain socket.
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/tripleclabs/nova/pkg/novapb/nova/v1"
)

// daemonClient returns a connected gRPC client to the Nova daemon,
// auto-starting the daemon if it isn't already running.
func daemonClient() (pb.NovaClient, *grpc.ClientConn, error) {
	stateDir := novaStateDir()
	socketPath := filepath.Join(stateDir, "daemon.sock")

	// If the socket doesn't exist, start the daemon.
	if _, err := os.Stat(socketPath); err != nil {
		if err := startDaemon(stateDir); err != nil {
			return nil, nil, fmt.Errorf("starting daemon: %w", err)
		}
	}

	conn, err := grpc.NewClient(
		"unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to daemon: %w", err)
	}

	// Verify the connection is actually alive. If the daemon crashed and left
	// a stale socket, NewClient succeeds but RPCs will fail with "connection refused".
	// Do a quick health check — if it fails, restart the daemon.
	client := pb.NewNovaClient(conn)
	checkCtx, checkCancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, checkErr := client.Status(checkCtx, &emptypb.Empty{})
	checkCancel()
	if checkErr != nil {
		conn.Close()
		// Stale socket — remove it and start a fresh daemon.
		os.Remove(socketPath)
		os.Remove(filepath.Join(stateDir, "daemon.pid"))
		if err := startDaemon(stateDir); err != nil {
			return nil, nil, fmt.Errorf("restarting daemon after stale socket: %w", err)
		}
		conn, err = grpc.NewClient(
			"unix://"+socketPath,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			return nil, nil, fmt.Errorf("connecting to restarted daemon: %w", err)
		}
		client = pb.NewNovaClient(conn)
	}

	return client, conn, nil
}

// startDaemon launches `nova daemon` as a background process and waits
// for the socket to appear.
func startDaemon(stateDir string) error {
	novaBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding nova binary: %w", err)
	}

	cmd := exec.Command(novaBin, "daemon", "--state-dir", stateDir)
	cmd.Stdout = nil
	cmd.Stderr = nil
	// Detach from the parent process group so the daemon outlives the CLI.
	cmd.SysProcAttr = daemonSysProcAttr()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting daemon process: %w", err)
	}

	// Don't wait on the child — it's a background daemon.
	cmd.Process.Release()

	// Poll for the socket.
	socketPath := filepath.Join(stateDir, "daemon.sock")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("daemon socket not found after 10s: %s", socketPath)
}

// novaStateDir returns the Nova state directory path.
func novaStateDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".nova")
}

// withDaemon is a helper that gets a daemon client, calls fn, and closes the connection.
func withDaemon(fn func(ctx context.Context, client pb.NovaClient) error) error {
	client, conn, err := daemonClient()
	if err != nil {
		return err
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	return fn(ctx, client)
}
