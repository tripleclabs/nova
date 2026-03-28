package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/3clabs/nova/internal/state"
	"github.com/spf13/cobra"
)

var shellCommand string

func newShellCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shell [name]",
		Short: "Open an SSH session to a running VM",
		RunE:  runShell,
	}
	cmd.Flags().StringVarP(&shellCommand, "command", "c", "", "execute a command instead of opening an interactive shell")
	return cmd
}

func runShell(cmd *cobra.Command, args []string) error {
	name := "default"
	if len(args) > 0 {
		name = args[0]
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	novaDir := filepath.Join(home, ".nova")

	store, err := state.NewStore(novaDir)
	if err != nil {
		return err
	}

	machine, err := store.Get(name)
	if err != nil {
		return fmt.Errorf("VM %q not found. Run 'nova up' first", name)
	}
	if machine.State != state.StateRunning {
		return fmt.Errorf("VM %q is not running (state: %s)", name, machine.State)
	}

	keyPath := filepath.Join(store.MachineDir(name), "ssh", "nova_ed25519")
	if _, err := os.Stat(keyPath); err != nil {
		return fmt.Errorf("SSH key not found at %s. Was the VM started with cloud-init?", keyPath)
	}

	// Guest IP — for now use localhost since we're behind NAT with port forwarding.
	// The default SSH port forward is host 2222 -> guest 22.
	host := "localhost"
	port := "2222"

	return sshConnect(host, port, "nova", keyPath, shellCommand)
}

func sshConnect(host, port, user, keyPath, command string) error {
	sshArgs := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-i", keyPath,
		"-p", port,
		fmt.Sprintf("%s@%s", user, host),
	}

	if command != "" {
		sshArgs = append(sshArgs, command)
	}

	// Retry connection during boot — VM may not have SSH ready yet.
	var lastErr error
	for attempt := 0; attempt < 30; attempt++ {
		ssh := exec.Command("ssh", sshArgs...)
		ssh.Stdin = os.Stdin
		ssh.Stdout = os.Stdout
		ssh.Stderr = os.Stderr

		lastErr = ssh.Run()
		if lastErr == nil {
			return nil
		}

		// If it's an interactive session that the user exited, don't retry.
		if command == "" && attempt > 0 {
			return lastErr
		}

		// Only retry on connection refused (VM still booting).
		if attempt < 29 {
			fmt.Fprintf(os.Stderr, "Waiting for SSH... (attempt %d/30)\n", attempt+1)
			time.Sleep(2 * time.Second)
		}
	}

	return fmt.Errorf("SSH connection failed after 30 attempts: %w", lastErr)
}
