package daemon

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"google.golang.org/grpc"

	pb "github.com/tripleclabs/nova/pkg/novapb/nova/v1"
)

// Daemon manages the gRPC server lifecycle.
type Daemon struct {
	stateDir   string
	socketPath string
	grpcServer *grpc.Server
	server     *Server
}

// New creates a Daemon that will listen at the given state directory.
func New(stateDir string) (*Daemon, error) {
	socketPath := filepath.Join(stateDir, "daemon.sock")

	// Clean up stale socket.
	if err := cleanStaleSocket(socketPath, stateDir); err != nil {
		return nil, err
	}

	return &Daemon{
		stateDir:   stateDir,
		socketPath: socketPath,
	}, nil
}

// SocketPath returns the Unix domain socket path.
func (d *Daemon) SocketPath() string {
	return d.socketPath
}

// Run starts the gRPC server and blocks until shutdown.
func (d *Daemon) Run() error {
	if err := os.MkdirAll(d.stateDir, 0755); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}

	// Write PID file.
	pidPath := filepath.Join(d.stateDir, "daemon.pid")
	os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0644)
	defer os.Remove(pidPath)

	// Create gRPC server.
	d.grpcServer = grpc.NewServer()

	srv, err := NewServer(d.stateDir, func() {
		d.grpcServer.GracefulStop()
	})
	if err != nil {
		return fmt.Errorf("creating server: %w", err)
	}
	d.server = srv

	pb.RegisterNovaServer(d.grpcServer, srv)

	// Listen on Unix domain socket.
	lis, err := net.Listen("unix", d.socketPath)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", d.socketPath, err)
	}
	defer os.Remove(d.socketPath)

	// Handle signals.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		srv.orch.DestroyAll()
		d.grpcServer.GracefulStop()
	}()

	slog.Info("daemon listening", "socket", d.socketPath, "pid", os.Getpid())
	return d.grpcServer.Serve(lis)
}

// cleanStaleSocket removes a leftover socket file if the daemon that
// created it is no longer running.
func cleanStaleSocket(socketPath, stateDir string) error {
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		return nil
	}

	// Check if the old daemon is still alive via its PID file.
	pidPath := filepath.Join(stateDir, "daemon.pid")
	data, err := os.ReadFile(pidPath)
	if err == nil {
		pid, err := strconv.Atoi(string(data))
		if err == nil {
			proc, err := os.FindProcess(pid)
			if err == nil {
				// Signal 0 tests if the process exists.
				if proc.Signal(syscall.Signal(0)) == nil {
					return fmt.Errorf("daemon already running (pid %d)", pid)
				}
			}
		}
	}

	// Stale socket — remove it.
	slog.Info("removing stale daemon socket", "path", socketPath)
	os.Remove(socketPath)
	os.Remove(pidPath)
	return nil
}
