//go:build linux

package hypervisor

import (
	"context"
	"encoding/json"
	"net"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// --- helpers ---

// argValue finds the value after flag in an args slice, or returns "".
func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// hasFlag reports whether flag appears in args.
func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// mockQMPServer serves a minimal QMP exchange over a Unix socket.
// Call serveOne to handle one client connection.
type mockQMPServer struct {
	l net.Listener
}

func newMockQMPServer(t *testing.T, sockPath string) *mockQMPServer {
	t.Helper()
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix %s: %v", sockPath, err)
	}
	t.Cleanup(func() { l.Close() })
	return &mockQMPServer{l: l}
}

// serveOne accepts one connection, sends the QMP greeting, then calls fn with the
// encoder/decoder so the test can drive the conversation.
func (s *mockQMPServer) serveOne(fn func(enc *json.Encoder, dec *json.Decoder)) {
	go func() {
		conn, err := s.l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		enc := json.NewEncoder(conn)
		dec := json.NewDecoder(conn)

		// Send QMP greeting.
		enc.Encode(map[string]interface{}{
			"QMP": map[string]interface{}{
				"version":      map[string]interface{}{},
				"capabilities": []string{},
			},
		})

		fn(enc, dec)
	}()
}

// handleCapabilities reads the qmp_capabilities command and responds with {"return":{}}.
func handleCapabilities(enc *json.Encoder, dec *json.Decoder) {
	var cmd map[string]interface{}
	dec.Decode(&cmd)
	enc.Encode(map[string]interface{}{"return": map[string]interface{}{}})
}

// --- newQEMUEngine ---

func TestNewQEMUEngine(t *testing.T) {
	h, err := newQEMUEngine()
	if err != nil {
		t.Fatalf("newQEMUEngine() error: %v", err)
	}
	if h == nil {
		t.Fatal("newQEMUEngine() returned nil")
	}
	if got := h.GetState(); got != StateStopped {
		t.Errorf("initial state = %q, want %q", got, StateStopped)
	}
}

// --- buildArgs ---

func baseEngine() *qemuEngine {
	return &qemuEngine{
		cfg: VMConfig{
			Name:     "test-vm",
			CPUs:     2,
			MemoryMB: 1024,
			DiskPath: "/var/lib/nova/test.qcow2",
		},
		qmpSock: "/tmp/nova-test/qmp.sock",
	}
}

func TestBuildArgs_Machine(t *testing.T) {
	args := baseEngine().buildArgs()
	machine := argValue(args, "-machine")
	if machine == "" {
		t.Fatal("-machine flag not found")
	}
	if !strings.HasPrefix(machine, "q35,accel=") {
		t.Errorf("-machine %q: want q35 with accel=", machine)
	}
}

func TestBuildArgs_CPU(t *testing.T) {
	args := baseEngine().buildArgs()
	if got := argValue(args, "-cpu"); got != "host" {
		t.Errorf("-cpu = %q, want %q", got, "host")
	}
}

func TestBuildArgs_SMP(t *testing.T) {
	args := baseEngine().buildArgs()
	if got := argValue(args, "-smp"); got != "2" {
		t.Errorf("-smp = %q, want %q", got, "2")
	}
}

func TestBuildArgs_Memory(t *testing.T) {
	args := baseEngine().buildArgs()
	if got := argValue(args, "-m"); got != "1024" {
		t.Errorf("-m = %q, want %q", got, "1024")
	}
}

func TestBuildArgs_Disk(t *testing.T) {
	args := baseEngine().buildArgs()
	drive := argValue(args, "-drive")
	if !strings.Contains(drive, "file=/var/lib/nova/test.qcow2") {
		t.Errorf("-drive %q: missing disk path", drive)
	}
	if !strings.Contains(drive, "format=qcow2") {
		t.Errorf("-drive %q: missing format=qcow2", drive)
	}
	if !strings.Contains(drive, "if=virtio") {
		t.Errorf("-drive %q: missing if=virtio", drive)
	}
}

func TestBuildArgs_CIDATAPresent(t *testing.T) {
	e := baseEngine()
	e.cfg.CIDATAPath = "/var/lib/nova/cidata.iso"
	args := e.buildArgs()

	driveCount := 0
	cidataFound := false
	for i, a := range args {
		if a == "-drive" && i+1 < len(args) {
			driveCount++
			if strings.Contains(args[i+1], "cidata.iso") {
				cidataFound = true
				if !strings.Contains(args[i+1], "media=cdrom") {
					t.Errorf("cidata -drive %q: missing media=cdrom", args[i+1])
				}
			}
		}
	}
	if driveCount != 2 {
		t.Errorf("got %d -drive flags, want 2 (disk + cidata)", driveCount)
	}
	if !cidataFound {
		t.Error("cidata ISO drive not found in args")
	}
}

func TestBuildArgs_NoCIDATA(t *testing.T) {
	args := baseEngine().buildArgs() // CIDATAPath is empty

	driveCount := 0
	for _, a := range args {
		if a == "-drive" {
			driveCount++
		}
	}
	if driveCount != 1 {
		t.Errorf("got %d -drive flags, want 1 (disk only)", driveCount)
	}
}

func TestBuildArgs_PortForwards(t *testing.T) {
	e := baseEngine()
	e.cfg.Network.PortForwards = []PortForward{
		{HostPort: 2222, GuestPort: 22, Protocol: "tcp"},
		{HostPort: 8080, GuestPort: 80, Protocol: "tcp"},
	}
	args := e.buildArgs()

	netdev := argValue(args, "-netdev")
	if netdev == "" {
		t.Fatal("-netdev flag not found")
	}
	if !strings.Contains(netdev, "hostfwd=tcp::2222-:22") {
		t.Errorf("-netdev %q: missing SSH port forward", netdev)
	}
	if !strings.Contains(netdev, "hostfwd=tcp::8080-:80") {
		t.Errorf("-netdev %q: missing HTTP port forward", netdev)
	}
}

func TestBuildArgs_NoPortForwards(t *testing.T) {
	args := baseEngine().buildArgs()
	netdev := argValue(args, "-netdev")
	if netdev == "" {
		t.Fatal("-netdev flag not found")
	}
	if strings.Contains(netdev, "hostfwd") {
		t.Errorf("-netdev %q: unexpected hostfwd with no port forwards", netdev)
	}
	if !strings.HasPrefix(netdev, "user,id=net0") {
		t.Errorf("-netdev %q: expected user-mode NAT", netdev)
	}
}

func TestBuildArgs_VirtioNet(t *testing.T) {
	args := baseEngine().buildArgs()
	dev := argValue(args, "-device")
	if !strings.Contains(dev, "virtio-net-pci") {
		t.Errorf("-device %q: missing virtio-net-pci", dev)
	}
	if !strings.Contains(dev, "netdev=net0") {
		t.Errorf("-device %q: missing netdev=net0", dev)
	}
}

func TestBuildArgs_QMPSocket(t *testing.T) {
	e := baseEngine()
	e.qmpSock = "/run/nova/vm1/qmp.sock"
	args := e.buildArgs()

	chardev := argValue(args, "-chardev")
	if !strings.Contains(chardev, "socket") {
		t.Errorf("-chardev %q: missing socket type", chardev)
	}
	if !strings.Contains(chardev, "/run/nova/vm1/qmp.sock") {
		t.Errorf("-chardev %q: missing qmp socket path", chardev)
	}
	if !strings.Contains(chardev, "server=on") {
		t.Errorf("-chardev %q: missing server=on", chardev)
	}

	mon := argValue(args, "-mon")
	if !strings.Contains(mon, "mode=control") {
		t.Errorf("-mon %q: missing mode=control", mon)
	}
}

func TestBuildArgs_LogPath(t *testing.T) {
	e := baseEngine()
	e.cfg.LogPath = "/var/log/nova/vm1.log"
	args := e.buildArgs()

	serial := argValue(args, "-serial")
	if serial != "file:/var/log/nova/vm1.log" {
		t.Errorf("-serial = %q, want file:/var/log/nova/vm1.log", serial)
	}
}

func TestBuildArgs_NoLogPath(t *testing.T) {
	args := baseEngine().buildArgs() // LogPath is empty
	serial := argValue(args, "-serial")
	if serial != "null" {
		t.Errorf("-serial = %q, want null", serial)
	}
}

func TestBuildArgs_Nographic(t *testing.T) {
	args := baseEngine().buildArgs()
	if !hasFlag(args, "-nographic") {
		t.Error("-nographic flag not found")
	}
}

func TestBuildArgs_9pShares(t *testing.T) {
	e := baseEngine()
	e.cfg.Shares = []ShareConfig{
		{Tag: "workspace", HostPath: "/home/user/projects", ReadOnly: false},
		{Tag: "data", HostPath: "/mnt/data", ReadOnly: true},
	}
	args := e.buildArgs()

	// Count -fsdev and -device virtio-9p-pci occurrences.
	fsdevCount := 0
	ninepDevCount := 0
	for i, a := range args {
		if a == "-fsdev" && i+1 < len(args) {
			fsdevCount++
			val := args[i+1]
			if !strings.Contains(val, "local,id=fs") {
				t.Errorf("-fsdev %q: expected local backend", val)
			}
			if !strings.Contains(val, "security_model=mapped-xattr") {
				t.Errorf("-fsdev %q: expected security_model=mapped-xattr", val)
			}
		}
		if a == "-device" && i+1 < len(args) && strings.Contains(args[i+1], "virtio-9p-pci") {
			ninepDevCount++
		}
	}
	if fsdevCount != 2 {
		t.Errorf("got %d -fsdev flags, want 2", fsdevCount)
	}
	if ninepDevCount != 2 {
		t.Errorf("got %d virtio-9p-pci devices, want 2", ninepDevCount)
	}
}

func TestBuildArgs_9pShare_HostPaths(t *testing.T) {
	e := baseEngine()
	e.cfg.Shares = []ShareConfig{
		{Tag: "ws", HostPath: "/home/user/workspace", ReadOnly: false},
	}
	args := e.buildArgs()

	for i, a := range args {
		if a == "-fsdev" && i+1 < len(args) {
			val := args[i+1]
			if !strings.Contains(val, "/home/user/workspace") {
				t.Errorf("-fsdev %q: expected host path", val)
			}
			if strings.Contains(val, "readonly") {
				t.Errorf("-fsdev %q: should not have readonly for writable share", val)
			}
			// Check corresponding -device has the right tag.
			if i+3 < len(args) && args[i+2] == "-device" {
				dev := args[i+3]
				if !strings.Contains(dev, "mount_tag=ws") {
					t.Errorf("-device %q: expected mount_tag=ws", dev)
				}
			}
			return
		}
	}
	t.Error("-fsdev not found")
}

func TestBuildArgs_9pShare_ReadOnly(t *testing.T) {
	e := baseEngine()
	e.cfg.Shares = []ShareConfig{
		{Tag: "ro", HostPath: "/data", ReadOnly: true},
	}
	args := e.buildArgs()

	for i, a := range args {
		if a == "-fsdev" && i+1 < len(args) {
			if !strings.Contains(args[i+1], "readonly=on") {
				t.Errorf("-fsdev %q: expected readonly=on", args[i+1])
			}
			return
		}
	}
	t.Error("-fsdev not found")
}

func TestBuildArgs_NoShares(t *testing.T) {
	args := baseEngine().buildArgs() // Shares is nil
	if hasFlag(args, "-fsdev") {
		t.Error("-fsdev should not appear when there are no shares")
	}
}

// --- qemuBinary ---

func TestQEMUBinaryName(t *testing.T) {
	// We can't guarantee QEMU is installed in CI, but we can verify the resolved
	// name follows the right convention.
	wantSuffix := runtime.GOARCH
	if runtime.GOARCH == "amd64" {
		wantSuffix = "x86_64"
	}

	// Call the function; if it succeeds, verify the binary name.
	// If it fails (QEMU not installed), that's an acceptable environment constraint.
	path, err := qemuBinary()
	if err != nil {
		if !strings.Contains(err.Error(), "not found in PATH") {
			t.Errorf("unexpected error from qemuBinary: %v", err)
		}
		t.Skipf("QEMU not installed (%v)", err)
	}
	if !strings.HasSuffix(path, wantSuffix) {
		t.Errorf("qemuBinary() = %q, want suffix %q", path, wantSuffix)
	}
}

func TestQEMUBinaryNotFound(t *testing.T) {
	t.Setenv("PATH", "")
	_, err := qemuBinary()
	if err == nil {
		t.Fatal("expected error with empty PATH, got nil")
	}
	if !strings.Contains(err.Error(), "not found in PATH") {
		t.Errorf("error %q: expected 'not found in PATH'", err)
	}
}

// --- Start error path ---

func TestStart_MissingQEMUBinary(t *testing.T) {
	t.Setenv("PATH", "")

	h, _ := newQEMUEngine()
	ctx := context.Background()
	err := h.Start(ctx, VMConfig{
		Name:     "test",
		CPUs:     1,
		MemoryMB: 512,
		DiskPath: "/tmp/test.qcow2",
	})
	if err == nil {
		t.Fatal("Start() with missing QEMU binary should fail")
	}
	if !strings.Contains(err.Error(), "not found in PATH") {
		t.Errorf("error %q: expected 'not found in PATH'", err)
	}
	if got := h.GetState(); got != StateError {
		t.Errorf("state after failed Start = %q, want %q", got, StateError)
	}
}

// --- GuestIP ---

func TestGuestIP_WhenRunning(t *testing.T) {
	e := &qemuEngine{state: StateRunning}
	ip, err := e.GuestIP()
	if err != nil {
		t.Fatalf("GuestIP() error: %v", err)
	}
	if ip != "10.0.2.15" {
		t.Errorf("GuestIP() = %q, want 10.0.2.15", ip)
	}
}

func TestGuestIP_WhenStopped(t *testing.T) {
	e := &qemuEngine{state: StateStopped}
	_, err := e.GuestIP()
	if err == nil {
		t.Fatal("GuestIP() on stopped VM should return error")
	}
}

func TestGuestIP_WhenStarting(t *testing.T) {
	e := &qemuEngine{state: StateStarting}
	_, err := e.GuestIP()
	if err == nil {
		t.Fatal("GuestIP() during StateStarting should return error")
	}
}

// --- QMP client ---

func TestConnectQMP(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "qmp.sock")

	srv := newMockQMPServer(t, sockPath)
	srv.serveOne(func(enc *json.Encoder, dec *json.Decoder) {
		handleCapabilities(enc, dec)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	qmp, err := connectQMP(ctx, sockPath)
	if err != nil {
		t.Fatalf("connectQMP: %v", err)
	}
	defer qmp.close()
}

func TestConnectQMP_Timeout(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "missing.sock") // socket never created

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := connectQMP(ctx, sockPath)
	if err == nil {
		t.Fatal("expected error when socket never appears")
	}
}

func TestQMPExecute(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "qmp.sock")

	srv := newMockQMPServer(t, sockPath)
	srv.serveOne(func(enc *json.Encoder, dec *json.Decoder) {
		handleCapabilities(enc, dec)

		// Handle one custom command.
		var cmd map[string]interface{}
		dec.Decode(&cmd)
		if cmd["execute"] != "query-version" {
			enc.Encode(map[string]interface{}{
				"error": map[string]interface{}{"class": "CommandNotFound", "desc": "unexpected command"},
			})
			return
		}
		enc.Encode(map[string]interface{}{
			"return": map[string]interface{}{"qemu": map[string]interface{}{"major": 8}},
		})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	qmp, err := connectQMP(ctx, sockPath)
	if err != nil {
		t.Fatalf("connectQMP: %v", err)
	}
	defer qmp.close()

	raw, err := qmp.execute("query-version", nil)
	if err != nil {
		t.Fatalf("execute query-version: %v", err)
	}
	if !strings.Contains(string(raw), "qemu") {
		t.Errorf("query-version response %q: missing 'qemu' key", raw)
	}
}

func TestQMPExecute_SkipsEvents(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "qmp.sock")

	srv := newMockQMPServer(t, sockPath)
	srv.serveOne(func(enc *json.Encoder, dec *json.Decoder) {
		handleCapabilities(enc, dec)

		var cmd map[string]interface{}
		dec.Decode(&cmd)

		// Send an async event before the real response.
		enc.Encode(map[string]interface{}{
			"event":     "RTC_CHANGE",
			"timestamp": map[string]interface{}{"seconds": 0, "microseconds": 0},
			"data":      map[string]interface{}{"offset": 0},
		})
		enc.Encode(map[string]interface{}{"return": map[string]interface{}{"status": "running"}})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	qmp, err := connectQMP(ctx, sockPath)
	if err != nil {
		t.Fatalf("connectQMP: %v", err)
	}
	defer qmp.close()

	raw, err := qmp.execute("query-status", nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(string(raw), "running") {
		t.Errorf("response %q: expected 'running'", raw)
	}
}

func TestQMPExecute_Error(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "qmp.sock")

	srv := newMockQMPServer(t, sockPath)
	srv.serveOne(func(enc *json.Encoder, dec *json.Decoder) {
		handleCapabilities(enc, dec)

		var cmd map[string]interface{}
		dec.Decode(&cmd)
		enc.Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"class": "CommandNotFound",
				"desc":  "The command nonexistent has not been found",
			},
		})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	qmp, err := connectQMP(ctx, sockPath)
	if err != nil {
		t.Fatalf("connectQMP: %v", err)
	}
	defer qmp.close()

	_, err = qmp.execute("nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for unknown QMP command")
	}
	if !strings.Contains(err.Error(), "CommandNotFound") {
		t.Errorf("error %q: expected CommandNotFound", err)
	}
}

// --- waitForRunning ---

func TestWaitForRunning_AlreadyRunning(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "qmp.sock")

	srv := newMockQMPServer(t, sockPath)
	srv.serveOne(func(enc *json.Encoder, dec *json.Decoder) {
		handleCapabilities(enc, dec)
		// query-status → running immediately
		var cmd map[string]interface{}
		dec.Decode(&cmd)
		enc.Encode(map[string]interface{}{"return": map[string]interface{}{"status": "running"}})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	qmp, err := connectQMP(ctx, sockPath)
	if err != nil {
		t.Fatalf("connectQMP: %v", err)
	}
	defer qmp.close()

	if err := waitForRunning(ctx, qmp); err != nil {
		t.Fatalf("waitForRunning: %v", err)
	}
}

func TestWaitForRunning_AfterPrelaunch(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "qmp.sock")

	srv := newMockQMPServer(t, sockPath)
	srv.serveOne(func(enc *json.Encoder, dec *json.Decoder) {
		handleCapabilities(enc, dec)

		var cmd map[string]interface{}

		// First poll: prelaunch
		dec.Decode(&cmd)
		enc.Encode(map[string]interface{}{"return": map[string]interface{}{"status": "prelaunch"}})

		// Second poll: running
		dec.Decode(&cmd)
		enc.Encode(map[string]interface{}{"return": map[string]interface{}{"status": "running"}})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	qmp, err := connectQMP(ctx, sockPath)
	if err != nil {
		t.Fatalf("connectQMP: %v", err)
	}
	defer qmp.close()

	if err := waitForRunning(ctx, qmp); err != nil {
		t.Fatalf("waitForRunning: %v", err)
	}
}

func TestWaitForRunning_UnknownStatus(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "qmp.sock")

	srv := newMockQMPServer(t, sockPath)
	srv.serveOne(func(enc *json.Encoder, dec *json.Decoder) {
		handleCapabilities(enc, dec)
		var cmd map[string]interface{}
		dec.Decode(&cmd)
		enc.Encode(map[string]interface{}{"return": map[string]interface{}{"status": "internal-error"}})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	qmp, err := connectQMP(ctx, sockPath)
	if err != nil {
		t.Fatalf("connectQMP: %v", err)
	}
	defer qmp.close()

	err = waitForRunning(ctx, qmp)
	if err == nil {
		t.Fatal("expected error for unknown VM status")
	}
	if !strings.Contains(err.Error(), "internal-error") {
		t.Errorf("error %q: expected status name in message", err)
	}
}

func TestWaitForRunning_ContextCancel(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "qmp.sock")

	srv := newMockQMPServer(t, sockPath)
	srv.serveOne(func(enc *json.Encoder, dec *json.Decoder) {
		handleCapabilities(enc, dec)
		// Keep responding with "prelaunch" so waitForRunning never exits.
		for {
			var cmd map[string]interface{}
			if err := dec.Decode(&cmd); err != nil {
				return
			}
			enc.Encode(map[string]interface{}{"return": map[string]interface{}{"status": "prelaunch"}})
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	qmp, err := connectQMP(ctx, sockPath)
	if err != nil {
		t.Fatalf("connectQMP: %v", err)
	}
	defer qmp.close()

	// Cancel the context immediately.
	shortCtx, shortCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer shortCancel()

	err = waitForRunning(shortCtx, qmp)
	if err == nil {
		t.Fatal("expected error when context is cancelled")
	}
}

// --- mapQMPStatus ---

func TestMapQMPStatus(t *testing.T) {
	cases := []struct {
		qmpStatus string
		want      State
	}{
		{"running", StateRunning},
		{"save-vm", StateRunning},
		{"debug", StateRunning},
		{"paused", StateStarting},
		{"prelaunch", StateStarting},
		{"inmigrate", StateStarting},
		{"finish-migrate", StateStarting},
		{"restore-vm", StateStarting},
		{"shutdown", StateStopped},
		{"guest-panicked", StateError},
		{"io-error", StateError},
		{"unknown-future-status", StateRunning}, // conservative default
	}
	for _, tc := range cases {
		t.Run(tc.qmpStatus, func(t *testing.T) {
			if got := mapQMPStatus(tc.qmpStatus); got != tc.want {
				t.Errorf("mapQMPStatus(%q) = %q, want %q", tc.qmpStatus, got, tc.want)
			}
		})
	}
}

// --- GetState() live QMP query ---

func TestGetState_LiveQMPQuery(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "qmp.sock")

	srv := newMockQMPServer(t, sockPath)
	srv.serveOne(func(enc *json.Encoder, dec *json.Decoder) {
		handleCapabilities(enc, dec)
		// Respond to the query-status from GetState().
		var cmd map[string]interface{}
		dec.Decode(&cmd)
		enc.Encode(map[string]interface{}{"return": map[string]interface{}{"status": "running"}})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	qmp, err := connectQMP(ctx, sockPath)
	if err != nil {
		t.Fatalf("connectQMP: %v", err)
	}

	e := &qemuEngine{state: StateRunning, qmp: qmp}
	if got := e.GetState(); got != StateRunning {
		t.Errorf("GetState() = %q, want %q", got, StateRunning)
	}
}

func TestGetState_QMPReportsPaused(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "qmp.sock")

	srv := newMockQMPServer(t, sockPath)
	srv.serveOne(func(enc *json.Encoder, dec *json.Decoder) {
		handleCapabilities(enc, dec)
		var cmd map[string]interface{}
		dec.Decode(&cmd)
		enc.Encode(map[string]interface{}{"return": map[string]interface{}{"status": "paused"}})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	qmp, err := connectQMP(ctx, sockPath)
	if err != nil {
		t.Fatalf("connectQMP: %v", err)
	}

	e := &qemuEngine{state: StateRunning, qmp: qmp}
	if got := e.GetState(); got != StateStarting {
		t.Errorf("GetState() with paused QMP status = %q, want %q", got, StateStarting)
	}
}

func TestGetState_QMPFailureFallsBackToCached(t *testing.T) {
	// Engine with a nil QMP client — GetState must return cached state.
	e := &qemuEngine{state: StateRunning, qmp: nil}
	if got := e.GetState(); got != StateRunning {
		t.Errorf("GetState() without QMP = %q, want %q (cached)", got, StateRunning)
	}
}

func TestGetState_StoppedSkipsQMP(t *testing.T) {
	// GetState should not attempt QMP when state is already Stopped.
	e := &qemuEngine{state: StateStopped, qmp: nil}
	if got := e.GetState(); got != StateStopped {
		t.Errorf("GetState() = %q, want %q", got, StateStopped)
	}
}

// --- GuestIP via guest agent ---

func TestGuestIP_GuestAgent(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "qmp.sock")

	guestAgentResponse := `{"return": [
		{"name": "lo", "ip-addresses": [{"ip-address-type": "ipv4", "ip-address": "127.0.0.1"}]},
		{"name": "eth0", "ip-addresses": [{"ip-address-type": "ipv4", "ip-address": "192.168.100.5"}]}
	]}`

	srv := newMockQMPServer(t, sockPath)
	srv.serveOne(func(enc *json.Encoder, dec *json.Decoder) {
		handleCapabilities(enc, dec)
		var cmd map[string]interface{}
		dec.Decode(&cmd)
		enc.Encode(json.RawMessage(guestAgentResponse))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	qmp, err := connectQMP(ctx, sockPath)
	if err != nil {
		t.Fatalf("connectQMP: %v", err)
	}

	e := &qemuEngine{state: StateRunning, qmp: qmp}
	ip, err := e.GuestIP()
	if err != nil {
		t.Fatalf("GuestIP(): %v", err)
	}
	if ip != "192.168.100.5" {
		t.Errorf("GuestIP() = %q, want 192.168.100.5", ip)
	}
}

func TestGuestIP_GuestAgentFallback(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "qmp.sock")

	// Guest agent returns an error (agent not installed).
	srv := newMockQMPServer(t, sockPath)
	srv.serveOne(func(enc *json.Encoder, dec *json.Decoder) {
		handleCapabilities(enc, dec)
		var cmd map[string]interface{}
		dec.Decode(&cmd)
		enc.Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"class": "CommandNotFound",
				"desc":  "guest-network-get-interfaces not available",
			},
		})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	qmp, err := connectQMP(ctx, sockPath)
	if err != nil {
		t.Fatalf("connectQMP: %v", err)
	}

	e := &qemuEngine{state: StateRunning, qmp: qmp}
	ip, err := e.GuestIP()
	if err != nil {
		t.Fatalf("GuestIP() should fall back to SLIRP address: %v", err)
	}
	if ip != "10.0.2.15" {
		t.Errorf("GuestIP() fallback = %q, want 10.0.2.15", ip)
	}
}

func TestGuestIP_SkipsLoopback(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "qmp.sock")

	// Only loopback interface present → should fall back to SLIRP address.
	guestAgentResponse := `{"return": [
		{"name": "lo", "ip-addresses": [{"ip-address-type": "ipv4", "ip-address": "127.0.0.1"}]}
	]}`

	srv := newMockQMPServer(t, sockPath)
	srv.serveOne(func(enc *json.Encoder, dec *json.Decoder) {
		handleCapabilities(enc, dec)
		var cmd map[string]interface{}
		dec.Decode(&cmd)
		enc.Encode(json.RawMessage(guestAgentResponse))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	qmp, err := connectQMP(ctx, sockPath)
	if err != nil {
		t.Fatalf("connectQMP: %v", err)
	}

	e := &qemuEngine{state: StateRunning, qmp: qmp}
	ip, err := e.GuestIP()
	if err != nil {
		t.Fatalf("GuestIP(): %v", err)
	}
	if ip != "10.0.2.15" {
		t.Errorf("GuestIP() with only loopback = %q, want SLIRP fallback 10.0.2.15", ip)
	}
}

// --- integration test (requires QEMU binary) ---

func TestStart_Integration(t *testing.T) {
	// Skip if qemu-system-x86_64 (or the arch equivalent) is not installed.
	name := "qemu-system-" + runtime.GOARCH
	if runtime.GOARCH == "amd64" {
		name = "qemu-system-x86_64"
	}
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("QEMU binary %q not found, skipping integration test", name)
	}

	// Also skip if there is no real disk image to boot — this test is purely a
	// smoke-test for the process launch + QMP handshake path.
	t.Skip("integration test requires a bootable disk image; skipping in unit test suite")
}
