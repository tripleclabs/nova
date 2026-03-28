// Package provisioner implements post-boot provisioning of VMs.
// Currently supports shell provisioners that execute commands or scripts
// over SSH after a VM has booted and cloud-init has completed.
package provisioner

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3clabs/nova/internal/config"
)

// SSHConfig holds the connection details for reaching a guest VM.
type SSHConfig struct {
	Host       string
	Port       string
	User       string
	PrivateKey []byte
}

// OutputWriter receives provisioner output with a node-name prefix.
type OutputWriter struct {
	Prefix string
	Writer io.Writer
}

func (w *OutputWriter) Write(p []byte) (int, error) {
	lines := strings.Split(string(p), "\n")
	for i, line := range lines {
		if line == "" && i == len(lines)-1 {
			break // skip trailing empty line from split
		}
		fmt.Fprintf(w.Writer, "[%s] %s\n", w.Prefix, line)
	}
	return len(p), nil
}

// RunAll executes a list of provisioners sequentially over SSH.
// It returns an error on the first failure and does not continue.
func RunAll(ctx context.Context, sshCfg SSHConfig, provs []config.Provisioner, output io.Writer) error {
	if len(provs) == 0 {
		return nil
	}

	for i, p := range provs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		timeout := 5 * time.Minute
		if p.Timeout != "" {
			d, err := time.ParseDuration(p.Timeout)
			if err != nil {
				return fmt.Errorf("provisioner[%d]: invalid timeout %q: %w", i, p.Timeout, err)
			}
			timeout = d
		}

		var err error
		if p.Script != "" {
			err = runScript(ctx, sshCfg, p, timeout, output)
		} else {
			err = runInline(ctx, sshCfg, p, timeout, output)
		}
		if err != nil {
			return fmt.Errorf("provisioner[%d]: %w", i, err)
		}
	}
	return nil
}

// runInline joins the inline commands with && and executes them in a single SSH session.
func runInline(ctx context.Context, sshCfg SSHConfig, p config.Provisioner, timeout time.Duration, output io.Writer) error {
	command := strings.Join(p.Inline, " && ")
	return execSSH(ctx, sshCfg, command, p.Env, timeout, output)
}

// runScript reads a local script file, uploads it via SSH, makes it executable, and runs it.
func runScript(ctx context.Context, sshCfg SSHConfig, p config.Provisioner, timeout time.Duration, output io.Writer) error {
	scriptData, err := os.ReadFile(p.Script)
	if err != nil {
		return fmt.Errorf("reading script %q: %w", p.Script, err)
	}

	// Upload script, make executable, run it, then remove it.
	// Use a heredoc approach via SSH to avoid needing SCP.
	remotePath := "/tmp/nova-provisioner.sh"
	uploadCmd := fmt.Sprintf("cat > %s && chmod +x %s", remotePath, remotePath)
	if err := execSSHWithStdin(ctx, sshCfg, uploadCmd, scriptData, nil, 30*time.Second, io.Discard); err != nil {
		return fmt.Errorf("uploading script: %w", err)
	}

	// Run the script with env vars, then clean up.
	runCmd := fmt.Sprintf("%s; exit_code=$?; rm -f %s; exit $exit_code", remotePath, remotePath)
	return execSSH(ctx, sshCfg, runCmd, p.Env, timeout, output)
}

// execSSH runs a command over SSH with optional environment variables.
func execSSH(ctx context.Context, sshCfg SSHConfig, command string, env map[string]string, timeout time.Duration, output io.Writer) error {
	return execSSHWithStdin(ctx, sshCfg, command, nil, env, timeout, output)
}

// execSSHWithStdin runs a command over SSH, optionally piping stdin data.
func execSSHWithStdin(ctx context.Context, sshCfg SSHConfig, command string, stdin []byte, env map[string]string, timeout time.Duration, output io.Writer) error {
	signer, err := ssh.ParsePrivateKey(sshCfg.PrivateKey)
	if err != nil {
		return fmt.Errorf("parsing SSH key: %w", err)
	}

	clientCfg := &ssh.ClientConfig{
		User:            sshCfg.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	addr := net.JoinHostPort(sshCfg.Host, sshCfg.Port)
	client, err := ssh.Dial("tcp", addr, clientCfg)
	if err != nil {
		return fmt.Errorf("SSH dial %s: %w", addr, err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("creating SSH session: %w", err)
	}
	defer session.Close()

	// Set environment variables via command prefix (more reliable than session.Setenv
	// which requires server-side AcceptEnv configuration).
	if len(env) > 0 {
		var envPrefix strings.Builder
		for k, v := range env {
			if !isValidEnvKey(k) {
				return fmt.Errorf("invalid environment variable name %q", k)
			}
			fmt.Fprintf(&envPrefix, "export %s=%s; ", k, shellQuote(v))
		}
		command = envPrefix.String() + command
	}

	// Wrap with sudo for provisioner commands.
	command = "sudo sh -c " + shellQuote(command)

	session.Stdout = output
	session.Stderr = output

	if stdin != nil {
		session.Stdin = strings.NewReader(string(stdin))
	}

	// Run with timeout.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()

	select {
	case err := <-done:
		if err != nil {
			if exitErr, ok := err.(*ssh.ExitError); ok {
				return fmt.Errorf("command exited with status %d", exitErr.ExitStatus())
			}
			return fmt.Errorf("SSH exec: %w", err)
		}
		return nil
	case <-ctx.Done():
		session.Signal(ssh.SIGTERM)
		return fmt.Errorf("timed out after %v", timeout)
	}
}

// isValidEnvKey checks that an environment variable name is safe (alphanumeric + underscore).
func isValidEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for i, c := range k {
		if c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			continue
		}
		if i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

// shellQuote wraps a string in single quotes, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
