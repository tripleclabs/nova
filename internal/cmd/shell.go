package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	pb "github.com/tripleclabs/nova/pkg/novapb/nova/v1"
	"google.golang.org/protobuf/types/known/durationpb"
	"github.com/spf13/cobra"
)

var shellCommand string

var shellTimeout time.Duration

func newShellCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shell [name]",
		Short: "Open an SSH session to a running VM",
		RunE:  runShell,
	}
	cmd.Flags().StringVarP(&shellCommand, "command", "c", "", "execute a command (streams output; use --timeout for buffered mode)")
	cmd.Flags().DurationVar(&shellTimeout, "timeout", 0, "if set, run via daemon RPC with this timeout and buffer output")
	return cmd
}

func runShell(cmd *cobra.Command, args []string) error {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}

	// Resolve name and endpoint via the daemon.
	var guestIP string
	err := withDaemon(func(ctx context.Context, client pb.NovaClient) error {
		if name == "" {
			var err error
			if name, err = resolveVMName(ctx, client); err != nil {
				return err
			}
		}

		// --timeout: buffered command mode via daemon Exec RPC.
		if shellCommand != "" && shellTimeout > 0 {
			resp, err := client.Exec(ctx, &pb.ExecRequest{
				Node:    name,
				Command: shellCommand,
				Timeout: durationpb.New(shellTimeout),
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
		}

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
	// Buffered --timeout mode handled everything inside withDaemon; nothing left to do.
	if guestIP == "" && shellCommand != "" && shellTimeout > 0 {
		return nil
	}

	machineDir := filepath.Join(novaStateDir(), "machines", name)
	keyPath := filepath.Join(machineDir, "ssh", "nova_ed25519")
	if _, err := os.Stat(keyPath); err != nil {
		return fmt.Errorf("SSH key not found at %s", keyPath)
	}

	sshUser := "nova"

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

	return sshExec(sshHost, sshPort, sshUser, keyPath, shellCommand)
}

// sshExec launches an SSH session. If command is non-empty it runs that command
// and streams stdout/stderr; otherwise it opens an interactive PTY shell.
// Agent forwarding is enabled when SSH_AUTH_SOCK is set in the environment.
func sshExec(host, port, user, keyPath, command string) error {
	sshArgs := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-i", keyPath,
		"-p", port,
	}

	// Forward the SSH agent when one is available.
	if os.Getenv("SSH_AUTH_SOCK") != "" {
		sshArgs = append(sshArgs, "-A")
	}

	sshArgs = append(sshArgs, fmt.Sprintf("%s@%s", user, host))

	if command != "" {
		sshArgs = append(sshArgs, command)
	}

	for attempt := 0; attempt < 30; attempt++ {
		ssh := exec.Command("ssh", sshArgs...)
		ssh.Stdin = os.Stdin
		ssh.Stdout = os.Stdout
		ssh.Stderr = os.Stderr

		err := ssh.Run()
		if err == nil {
			return nil
		}

		// SSH exit 255 means a connection-level failure (host unreachable, refused,
		// timeout). Keep retrying. Any other exit code means the remote session or
		// command ended — return immediately without retrying.
		if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 255 {
			return err
		}

		// Only retry on connection failures for interactive sessions;
		// for commands, a failed connection is immediately an error.
		if command != "" {
			return err
		}

		fmt.Fprintf(os.Stderr, "Waiting for SSH... (attempt %d/30)\n", attempt+1)
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("SSH connection failed after 30 attempts")
}
