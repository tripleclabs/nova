package cmd

import (
	"os"
	"path/filepath"

	"github.com/3clabs/nova/internal/daemon"
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

	return cmd
}

func runDaemon(cmd *cobra.Command, args []string) error {
	d, err := daemon.New(daemonStateDir)
	if err != nil {
		return err
	}
	return d.Run()
}
