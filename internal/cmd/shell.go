package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	pb "github.com/tripleclabs/nova/pkg/novapb/nova/v1"
	"google.golang.org/protobuf/types/known/durationpb"
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

	// Non-interactive mode: use the daemon's Exec RPC.
	if shellCommand != "" {
		return withDaemon(func(ctx context.Context, client pb.NovaClient) error {
			resp, err := client.Exec(ctx, &pb.ExecRequest{
				Node:    name,
				Command: shellCommand,
				Timeout: durationpb.New(30 * time.Second),
			})
			if err != nil {
				return err
			}
			fmt.Print(resp.Stdout)
			if resp.Stderr != "" {
				fmt.Fprint(os.Stderr, resp.Stderr)
			}
			if resp.ExitCode != 0 {
				os.Exit(int(resp.ExitCode))
			}
			return nil
		})
	}

	// Interactive mode: get the guest IP from the daemon, then exec ssh directly
	// so we get a proper PTY.
	var guestIP string
	err := withDaemon(func(ctx context.Context, client pb.NovaClient) error {
		resp, err := client.NodeStatus(ctx, &pb.NodeRequest{Name: name})
		if err != nil {
			return fmt.Errorf("VM %q not found: %w", name, err)
		}
		if resp.State != "running" {
			return fmt.Errorf("VM %q is not running (state: %s)", name, resp.State)
		}
		guestIP = resp.Ip
		return nil
	})
	if err != nil {
		return err
	}

	machineDir := filepath.Join(novaStateDir(), "machines", name)
	keyPath := filepath.Join(machineDir, "ssh", "nova_ed25519")
	if _, err := os.Stat(keyPath); err != nil {
		return fmt.Errorf("SSH key not found at %s", keyPath)
	}

	// Use configured user if present, otherwise default to "nova".
	sshUser := "nova"
	if data, err := os.ReadFile(filepath.Join(machineDir, "shell_user")); err == nil {
		sshUser = strings.TrimSpace(string(data))
	}

	// Read SSH endpoint — may differ from guest IP for Linux multi-node.
	sshHost := guestIP
	sshPort := "22"
	if data, err := os.ReadFile(filepath.Join(machineDir, "ssh_endpoint.json")); err == nil {
		var ep struct {
			Host string `json:"host"`
			Port int    `json:"port"`
		}
		if json.Unmarshal(data, &ep) == nil {
			if ep.Host != "" {
				sshHost = ep.Host
			}
			if ep.Port > 0 {
				sshPort = fmt.Sprintf("%d", ep.Port)
			}
		}
	}

	if sshHost == "" {
		return fmt.Errorf("could not determine guest IP for %q — try 'nova down %s && nova up'", name, name)
	}

	return sshInteractive(sshHost, sshPort, sshUser, keyPath)
}

// sshInteractive launches an interactive SSH session with a PTY.
func sshInteractive(host, port, user, keyPath string) error {
	sshArgs := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-i", keyPath,
		"-p", port,
		fmt.Sprintf("%s@%s", user, host),
	}

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

		// If the user exited an interactive session, don't retry.
		if attempt > 0 {
			return lastErr
		}

		fmt.Fprintf(os.Stderr, "Waiting for SSH... (attempt %d/30)\n", attempt+1)
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("SSH connection failed after 30 attempts: %w", lastErr)
}
