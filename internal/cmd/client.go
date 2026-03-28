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

	pb "github.com/3clabs/nova/pkg/novapb/nova/v1"
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

	return pb.NewNovaClient(conn), conn, nil
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
