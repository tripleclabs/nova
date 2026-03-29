package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tripleclabs/nova/internal/daemon"
	"github.com/spf13/cobra"
)

var daemonStateDir string

func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Start the Nova daemon (gRPC server for SDK and CLI)",
		Long: `Starts a background daemon that manages VM lifecycle, network chaos,
snapshots, and SSH exec. The CLI and test SDKs connect to this daemon
via a Unix domain socket.

The daemon listens at $STATE_DIR/daemon.sock and writes its PID to
$STATE_DIR/daemon.pid.`,
		RunE: runDaemon,
	}

	home, _ := os.UserHomeDir()
	defaultDir := filepath.Join(home, ".nova")
	cmd.Flags().StringVar(&daemonStateDir, "state-dir", defaultDir, "state directory for this daemon instance")

	cmd.AddCommand(newDaemonReloadCmd())

	return cmd
}

func runDaemon(cmd *cobra.Command, args []string) error {
	d, err := daemon.New(daemonStateDir)
	if err != nil {
		return err
	}
	return d.Run()
}

func newDaemonReloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reload",
		Short: "Reload the daemon (picks up a new binary without destroying VMs)",
		RunE: func(cmd *cobra.Command, args []string) error {
			stateDir := novaStateDir()
			pidPath := filepath.Join(stateDir, "daemon.pid")
			socketPath := filepath.Join(stateDir, "daemon.sock")

			data, err := os.ReadFile(pidPath)
			if err != nil {
				return fmt.Errorf("daemon does not appear to be running (no PID file)")
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				return fmt.Errorf("invalid PID file: %w", err)
			}

			proc, err := os.FindProcess(pid)
			if err != nil {
				return fmt.Errorf("finding daemon process: %w", err)
			}
			if err := proc.Signal(syscall.SIGUSR1); err != nil {
				return fmt.Errorf("sending SIGUSR1 to daemon (pid %d): %w", pid, err)
			}
			fmt.Printf("Sent reload signal to daemon (pid %d)...\n", pid)

			// Wait for the old socket to disappear.
			deadline := time.Now().Add(15 * time.Second)
			for time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
				if _, err := os.Stat(socketPath); os.IsNotExist(err) {
					break
				}
			}

			// Start the new daemon (same binary the user just built).
			if err := startDaemon(stateDir); err != nil {
				return fmt.Errorf("starting new daemon: %w", err)
			}
			fmt.Println("Daemon reloaded.")
			return nil
		},
	}
}
