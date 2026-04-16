//go:build linux

package hypervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// qemuEngine implements the Hypervisor interface using QEMU/KVM on Linux.
type qemuEngine struct {
	mu          sync.Mutex
	state       State
	cfg         VMConfig
	cmd         *exec.Cmd
	proc        *os.Process // set for reattached VMs (not started by this process)
	qmpSock     string      // path to QMP Unix socket
	stderrPath  string      // path to QEMU stderr log file
	qmp         *qmpClient  // QMP connection for VM control
	waitCh      chan error   // receives process exit error from background watcher
}

func newQEMUEngine() (Hypervisor, error) {
	return &qemuEngine{state: StateStopped}, nil
}

func (e *qemuEngine) Start(ctx context.Context, cfg VMConfig) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.cfg = cfg
	e.state = StateStarting
	slog.Info("configuring VM", "name", cfg.Name, "cpus", cfg.CPUs, "memory_mb", cfg.MemoryMB)

	binary, err := qemuBinary()
	if err != nil {
		e.state = StateError
		return err
	}

	e.qmpSock = filepath.Join(cfg.MachineDir, "qmp.sock")

	if err := setupOVMFVars(cfg); err != nil {
		e.state = StateError
		return err
	}

	args := e.buildArgs()
	slog.Debug("starting QEMU", "binary", binary, "args", args)

	e.cmd = exec.CommandContext(context.Background(), binary, args...)
	if cfg.Network.SwitchFile != nil {
		// Pass the QEMU-side socketpair fd as fd=3 in the child process.
		e.cmd.ExtraFiles = []*os.File{cfg.Network.SwitchFile}
	}

	// Capture QEMU stderr to a log file for diagnostics.
	stderrPath := filepath.Join(cfg.MachineDir, "qemu-stderr.log")
	e.stderrPath = stderrPath
	if stderrFile, err := os.Create(stderrPath); err == nil {
		e.cmd.Stderr = stderrFile
		defer stderrFile.Close()
	}

	if err := e.cmd.Start(); err != nil {
		e.state = StateError
		return fmt.Errorf("starting QEMU process: %w", err)
	}
	pid := e.cmd.Process.Pid
	slog.Info("QEMU process started", "name", cfg.Name, "pid", pid)

	if cfg.PIDPath != "" {
		_ = os.WriteFile(cfg.PIDPath, []byte(fmt.Sprintf("%d\n", pid)), 0644)
	}

	e.waitCh = make(chan error, 1)
	go func() {
		err := e.cmd.Wait()
		e.waitCh <- err
		e.mu.Lock()
		defer e.mu.Unlock()
		// Only update state for unexpected exits; Stop/ForceKill set it themselves.
		if e.state == StateRunning || e.state == StateStarting {
			if err != nil {
				e.state = StateError
				slog.Error("QEMU process exited unexpectedly", "name", cfg.Name, "err", err)
			} else {
				e.state = StateStopped
				slog.Info("QEMU process exited", "name", cfg.Name)
			}
		}
		os.Remove(e.qmpSock)
	}()

	qmp, err := e.waitForQMP(ctx)
	if err != nil {
		e.forceKillLocked()
		return err
	}
	e.qmp = qmp

	if err := waitForRunning(ctx, qmp); err != nil {
		e.forceKillLocked()
		return fmt.Errorf("waiting for VM to start: %w", err)
	}

	e.state = StateRunning
	slog.Info("VM started", "name", cfg.Name)
	return nil
}

// waitForQMP connects to the QMP socket, but also watches waitCh so that a
// QEMU startup crash is detected immediately rather than after a 30-second timeout.
// Must be called with e.mu held (it releases and re-acquires around the blocking connect).
func (e *qemuEngine) waitForQMP(ctx context.Context) (*qmpClient, error) {
	type result struct {
		qmp *qmpClient
		err error
	}
	ch := make(chan result, 1)
	go func() {
		q, err := connectQMP(ctx, e.qmpSock)
		ch <- result{q, err}
	}()

	select {
	case r := <-ch:
		return r.qmp, r.err
	case exitErr := <-e.waitCh:
		// Re-queue the exit error so the background watcher goroutine can read it.
		e.waitCh <- exitErr
		// Try to include QEMU's stderr output in the error message.
		if e.stderrPath != "" {
			if stderrBytes, err := os.ReadFile(e.stderrPath); err == nil && len(stderrBytes) > 0 {
				return nil, fmt.Errorf("QEMU exited unexpectedly: %s", strings.TrimSpace(string(stderrBytes)))
			}
		}
		return nil, fmt.Errorf("QEMU exited unexpectedly before QMP socket was ready (check %s for details)", e.stderrPath)
	}
}

func (e *qemuEngine) Stop(ctx context.Context) error {
	e.mu.Lock()

	if e.state == StateStopped {
		e.mu.Unlock()
		return nil
	}
	if e.qmp == nil {
		e.mu.Unlock()
		return fmt.Errorf("VM not started")
	}

	waitCh := e.waitCh // capture before releasing the lock

	// Request graceful ACPI shutdown via QMP.
	if _, err := e.qmp.execute("system_powerdown", nil); err != nil {
		slog.Warn("QMP system_powerdown failed, forcing kill", "err", err)
		err2 := e.forceKillLocked()
		e.mu.Unlock()
		return err2
	}

	// Release lock while waiting; the background watcher will update state.
	e.mu.Unlock()

	select {
	case <-waitCh:
		slog.Info("VM stopped gracefully", "name", e.cfg.Name)
		return nil
	case <-ctx.Done():
		e.mu.Lock()
		err := e.forceKillLocked()
		e.mu.Unlock()
		if err != nil {
			return fmt.Errorf("stop timed out, force kill failed: %w", err)
		}
		return fmt.Errorf("stop timed out, VM force killed: %w", ctx.Err())
	}
}

func (e *qemuEngine) ForceKill() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.forceKillLocked()
}

// forceKillLocked must be called with e.mu held.
func (e *qemuEngine) forceKillLocked() error {
	proc := e.proc
	if e.cmd != nil && e.cmd.Process != nil {
		proc = e.cmd.Process
	}
	if proc == nil {
		return fmt.Errorf("VM not started")
	}

	// Try QMP quit first for a cleaner shutdown than SIGKILL.
	if e.qmp != nil {
		if _, err := e.qmp.execute("quit", nil); err == nil {
			e.state = StateStopped
			slog.Info("VM force killed via QMP quit", "name", e.cfg.Name)
			return nil
		}
	}

	if err := proc.Kill(); err != nil {
		e.state = StateError
		return fmt.Errorf("killing QEMU process: %w", err)
	}
	e.state = StateStopped
	slog.Info("VM force killed", "name", e.cfg.Name)
	return nil
}

func (e *qemuEngine) GetState() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	// Refresh from live QMP status when the VM is running.
	if e.state == StateRunning && e.qmp != nil {
		if s, err := e.liveQMPState(); err == nil {
			e.state = s
		}
	}
	return e.state
}

// liveQMPState queries QEMU for the current VM status and maps it to State.
// Must be called with e.mu held.
func (e *qemuEngine) liveQMPState() (State, error) {
	// Short deadline so GetState() never hangs.
	_ = e.qmp.conn.SetDeadline(time.Now().Add(time.Second))
	defer e.qmp.conn.SetDeadline(time.Time{}) //nolint:errcheck

	raw, err := e.qmp.execute("query-status", nil)
	if err != nil {
		return "", err
	}
	var status struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return "", err
	}
	return mapQMPStatus(status.Status), nil
}

// mapQMPStatus converts a QEMU query-status string to a hypervisor State.
func mapQMPStatus(status string) State {
	switch status {
	case "running", "save-vm", "debug":
		return StateRunning
	case "paused", "prelaunch", "inmigrate", "finish-migrate", "restore-vm":
		return StateStarting
	case "shutdown":
		return StateStopped
	case "guest-panicked", "io-error":
		return StateError
	default:
		return StateRunning // conservative: treat unknown as still running
	}
}

// GuestIP returns the guest IP address.
// For multi-node, it returns the configured static IP (on the mcast NIC).
// For single-VM, it queries the QEMU guest agent then falls back to 10.0.2.15
// (the fixed SLIRP address).
func (e *qemuEngine) GuestIP() (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state != StateRunning {
		return "", fmt.Errorf("VM is not running (state: %s)", e.state)
	}
	if e.cfg.Network.MultiNode && e.cfg.Network.StaticIP != "" {
		return e.cfg.Network.StaticIP, nil
	}
	if ip, err := e.guestAgentIP(); err == nil {
		return ip, nil
	}
	return "10.0.2.15", nil
}

// guestAgentIP queries the QEMU guest agent for the first non-loopback IPv4 address.
// Must be called with e.mu held.
func (e *qemuEngine) guestAgentIP() (string, error) {
	if e.qmp == nil {
		return "", fmt.Errorf("QMP not connected")
	}
	_ = e.qmp.conn.SetDeadline(time.Now().Add(3 * time.Second))
	defer e.qmp.conn.SetDeadline(time.Time{}) //nolint:errcheck

	raw, err := e.qmp.execute("guest-network-get-interfaces", nil)
	if err != nil {
		return "", err
	}

	var ifaces []struct {
		Name        string `json:"name"`
		IPAddresses []struct {
			Type    string `json:"ip-address-type"`
			Address string `json:"ip-address"`
		} `json:"ip-addresses"`
	}
	if err := json.Unmarshal(raw, &ifaces); err != nil {
		return "", fmt.Errorf("parsing guest-network-get-interfaces: %w", err)
	}

	for _, iface := range ifaces {
		if iface.Name == "lo" {
			continue
		}
		for _, addr := range iface.IPAddresses {
			if addr.Type == "ipv4" && addr.Address != "" {
				return addr.Address, nil
			}
		}
	}
	return "", fmt.Errorf("no non-loopback IPv4 address found via guest agent")
}

// buildArgs constructs the qemu-system argument list from the current VMConfig.
func (e *qemuEngine) buildArgs() []string {
	cfg := e.cfg

	args := []string{
		// Try KVM acceleration; QEMU falls back to TCG if unavailable.
		"-machine", "q35,accel=kvm:tcg",
		"-cpu", "host",
		"-smp", fmt.Sprintf("%d", cfg.CPUs),
		"-m", fmt.Sprintf("%d", cfg.MemoryMB),
		"-drive", fmt.Sprintf("file=%s,format=qcow2,if=none,id=disk0", cfg.DiskPath),
		"-device", "virtio-blk-pci,drive=disk0,bootindex=1",
	}

	// UEFI firmware: unit 0 = read-only code image, unit 1 = per-VM writable vars.
	// setupOVMFVars (called before buildArgs) has already validated the paths.
	if code, _, err := ovmfPaths(normalizeArch(cfg.Arch)); err == nil {
		varsPath := filepath.Join(cfg.MachineDir, "ovmf_vars.fd")
		args = append(args,
			"-drive", fmt.Sprintf("if=pflash,format=raw,unit=0,readonly=on,file=%s", code),
			"-drive", fmt.Sprintf("if=pflash,format=raw,unit=1,file=%s", varsPath),
		)
	}

	// Cloud-init CIDATA ISO (optional).
	if cfg.CIDATAPath != "" {
		args = append(args, "-drive",
			fmt.Sprintf("file=%s,format=raw,if=virtio,media=cdrom", cfg.CIDATAPath))
	}

	if cfg.Network.SwitchFile != nil {
		// Switch-based networking: single NIC connected to the daemon's L2 switch via fd=3.
		args = append(args,
			"-netdev", "socket,id=net0,fd=3",
			"-device", fmt.Sprintf("virtio-net-pci,netdev=net0,mac=%s", cfg.Network.MACAddress),
		)
	} else if cfg.Network.MultiNode {
		// Legacy multi-node: two NICs.
		// NIC 1: socket multicast — virtual hub connecting all VMs (rootless).
		mcastAddr := "230.0.0.1:1234"
		mcastDev := fmt.Sprintf("virtio-net-pci,netdev=net0,mac=%s", cfg.Network.MACAddress)
		args = append(args,
			"-netdev", fmt.Sprintf("socket,id=net0,mcast=%s", mcastAddr),
			"-device", mcastDev,
		)
		// NIC 2: SLIRP — host SSH access + internet + port forwarding.
		slirp := "user,id=net1"
		for _, pf := range cfg.Network.PortForwards {
			slirp += fmt.Sprintf(",hostfwd=%s::%d-:%d", pf.Protocol, pf.HostPort, pf.GuestPort)
		}
		args = append(args,
			"-netdev", slirp,
			"-device", "virtio-net-pci,netdev=net1",
		)
	} else {
		// Legacy single-VM: SLIRP user-mode networking with port forwards.
		netdev := "user,id=net0"
		for _, pf := range cfg.Network.PortForwards {
			netdev += fmt.Sprintf(",hostfwd=%s::%d-:%d", pf.Protocol, pf.HostPort, pf.GuestPort)
		}
		args = append(args,
			"-netdev", netdev,
			"-device", "virtio-net-pci,netdev=net0",
		)
	}

	// QMP management socket for lifecycle control.
	args = append(args,
		"-chardev", fmt.Sprintf("socket,id=qmp0,path=%s,server=on,wait=off", e.qmpSock),
		"-mon", "chardev=qmp0,mode=control",
	)

	// Serial console output.
	if cfg.LogPath != "" {
		args = append(args, "-serial", fmt.Sprintf("file:%s", cfg.LogPath))
	} else {
		args = append(args, "-serial", "null")
	}

	// Shared folders via virtio-9p (no external daemon required, unlike VirtioFS).
	for i, share := range cfg.Shares {
		fsdevID := fmt.Sprintf("fs%d", i)
		fsdev := fmt.Sprintf("local,id=%s,path=%s,security_model=none", fsdevID, share.HostPath)
		if share.ReadOnly {
			fsdev += ",readonly=on"
		}
		args = append(args,
			"-fsdev", fsdev,
			"-device", fmt.Sprintf("virtio-9p-pci,fsdev=%s,mount_tag=%s", fsdevID, share.Tag),
		)
	}

	args = append(args, "-nographic")
	return args
}

// attachQEMUEngine reconnects to an already-running QEMU VM via its QMP socket.
// Used by the daemon to reattach after a reload.
func attachQEMUEngine(ctx context.Context, cfg VMConfig) (Hypervisor, error) {
	qmpSock := filepath.Join(cfg.MachineDir, "qmp.sock")
	if _, err := os.Stat(qmpSock); err != nil {
		return nil, fmt.Errorf("QMP socket not found at %s", qmpSock)
	}

	qmp, err := connectQMP(ctx, qmpSock)
	if err != nil {
		return nil, fmt.Errorf("connecting to QMP: %w", err)
	}

	raw, err := qmp.execute("query-status", nil)
	if err != nil {
		qmp.close()
		return nil, fmt.Errorf("querying VM status: %w", err)
	}
	var st struct {
		Status string `json:"status"`
	}
	json.Unmarshal(raw, &st)
	vmState := mapQMPStatus(st.Status)
	if vmState == StateStopped || vmState == StateError {
		qmp.close()
		return nil, fmt.Errorf("VM is in state %s", vmState)
	}

	e := &qemuEngine{
		cfg:     cfg,
		state:   vmState,
		qmpSock: qmpSock,
		qmp:     qmp,
		waitCh:  make(chan error, 1),
	}

	// Locate the process for monitoring and force-kill.
	if cfg.PIDPath != "" {
		if data, err := os.ReadFile(cfg.PIDPath); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
				if proc, err := os.FindProcess(pid); err == nil {
					e.proc = proc
					go e.watchAttachedProcess()
				}
			}
		}
	}

	slog.Info("reattached to running VM", "name", cfg.Name, "qmp", qmpSock)
	return e, nil
}

// watchAttachedProcess polls the PID of a reattached VM and signals waitCh when it exits.
func (e *qemuEngine) watchAttachedProcess() {
	for {
		time.Sleep(500 * time.Millisecond)
		e.mu.Lock()
		proc := e.proc
		e.mu.Unlock()
		if proc == nil {
			return
		}
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			e.waitCh <- err
			e.mu.Lock()
			if e.state == StateRunning || e.state == StateStarting {
				e.state = StateStopped
				slog.Info("attached VM process exited", "name", e.cfg.Name)
			}
			os.Remove(e.qmpSock)
			e.mu.Unlock()
			return
		}
	}
}

// ovmfPaths returns the host paths for the OVMF firmware code image and the
// writable vars template for the given guest architecture.
// Returns an error if the firmware package is not installed.
func ovmfPaths(arch string) (code, varsTmpl string, err error) {
	type pair struct{ code, vars string }
	var candidates []pair
	switch normalizeArch(arch) {
	case "arm64":
		candidates = []pair{
			{"/usr/share/AAVMF/AAVMF_CODE.fd", "/usr/share/AAVMF/AAVMF_VARS.fd"},
			{"/usr/share/edk2/aarch64/QEMU_EFI.fd", "/usr/share/edk2/aarch64/vars-template-pflash.raw"},
		}
	default: // amd64
		candidates = []pair{
			{"/usr/share/OVMF/OVMF_CODE.fd", "/usr/share/OVMF/OVMF_VARS.fd"},
			{"/usr/share/edk2/x64/OVMF_CODE.fd", "/usr/share/edk2/x64/OVMF_VARS.fd"},
			{"/usr/share/edk2-ovmf/x64/OVMF_CODE.fd", "/usr/share/edk2-ovmf/x64/OVMF_VARS.fd"},
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c.code); err == nil {
			if _, err := os.Stat(c.vars); err == nil {
				return c.code, c.vars, nil
			}
			return c.code, "", nil // code found; no vars template, will create blank
		}
	}
	pkg := "ovmf"
	if normalizeArch(arch) == "arm64" {
		pkg = "qemu-efi-aarch64"
	}
	return "", "", fmt.Errorf("UEFI firmware not found — install %s (Debian/Ubuntu) or edk2-ovmf (Fedora)", pkg)
}

// setupOVMFVars ensures a per-VM writable OVMF variable store exists in
// cfg.MachineDir. On first boot it is copied from the system template; on
// subsequent boots the existing file (which may contain saved UEFI state) is
// left untouched.
func setupOVMFVars(cfg VMConfig) error {
	varsPath := filepath.Join(cfg.MachineDir, "ovmf_vars.fd")
	if _, err := os.Stat(varsPath); err == nil {
		return nil // already present from a previous boot
	}
	_, varsTmpl, err := ovmfPaths(cfg.Arch)
	if err != nil {
		return err
	}
	if varsTmpl == "" {
		// No template on this host — create a blank 64 KiB pflash image.
		return os.WriteFile(varsPath, make([]byte, 64*1024), 0644)
	}
	data, err := os.ReadFile(varsTmpl)
	if err != nil {
		return fmt.Errorf("reading OVMF vars template %s: %w", varsTmpl, err)
	}
	return os.WriteFile(varsPath, data, 0644)
}

// qemuBinary resolves the qemu-system binary for the current architecture.
func qemuBinary() (string, error) {
	name := "qemu-system-" + runtime.GOARCH
	if runtime.GOARCH == "amd64" {
		name = "qemu-system-x86_64"
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("QEMU binary %q not found in PATH: %w", name, err)
	}
	return path, nil
}

// --- QMP client ---

// qmpClient manages a QMP (QEMU Machine Protocol) connection over a Unix socket.
type qmpClient struct {
	mu   sync.Mutex
	conn net.Conn
	dec  *json.Decoder
	enc  *json.Encoder
}

type qmpResponse struct {
	Return json.RawMessage `json:"return"`
	Error  *qmpError       `json:"error"`
	Event  string          `json:"event"`
}

type qmpError struct {
	Class string `json:"class"`
	Desc  string `json:"desc"`
}

// connectQMP dials the QMP Unix socket, reads the greeting, and negotiates capabilities.
// It retries until the socket appears or ctx expires.
func connectQMP(ctx context.Context, sockPath string) (*qmpClient, error) {
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	var conn net.Conn
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for QMP socket %s", sockPath)
		}
		var err error
		conn, err = net.DialTimeout("unix", sockPath, time.Second)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context cancelled waiting for QMP: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}

	c := &qmpClient{
		conn: conn,
		dec:  json.NewDecoder(conn),
		enc:  json.NewEncoder(conn),
	}

	// Read QMP greeting: {"QMP": {"version": ..., "capabilities": [...]}}
	var greeting map[string]json.RawMessage
	if err := c.dec.Decode(&greeting); err != nil {
		conn.Close()
		return nil, fmt.Errorf("reading QMP greeting: %w", err)
	}
	if _, ok := greeting["QMP"]; !ok {
		conn.Close()
		return nil, fmt.Errorf("unexpected QMP greeting: missing QMP key")
	}

	// Enter command mode by negotiating capabilities.
	if _, err := c.execute("qmp_capabilities", nil); err != nil {
		conn.Close()
		return nil, fmt.Errorf("QMP capabilities negotiation: %w", err)
	}

	return c, nil
}

// execute sends a QMP command and returns the decoded return value.
// Asynchronous events received before the response are silently skipped.
func (c *qmpClient) execute(command string, args interface{}) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	msg := map[string]interface{}{"execute": command}
	if args != nil {
		msg["arguments"] = args
	}
	if err := c.enc.Encode(msg); err != nil {
		return nil, fmt.Errorf("sending QMP command %q: %w", command, err)
	}

	for {
		var resp qmpResponse
		if err := c.dec.Decode(&resp); err != nil {
			return nil, fmt.Errorf("reading QMP response for %q: %w", command, err)
		}
		if resp.Event != "" {
			continue // async event; skip and wait for the command response
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("QMP error from %q: %s: %s", command, resp.Error.Class, resp.Error.Desc)
		}
		return resp.Return, nil
	}
}

func (c *qmpClient) close() error {
	return c.conn.Close()
}

// waitForRunning polls QMP query-status until the VM reports "running".
func waitForRunning(ctx context.Context, qmp *qmpClient) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled waiting for VM: %w", ctx.Err())
		default:
		}

		raw, err := qmp.execute("query-status", nil)
		if err != nil {
			return fmt.Errorf("query-status: %w", err)
		}

		var status struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(raw, &status); err != nil {
			return fmt.Errorf("parsing query-status response: %w", err)
		}

		switch status.Status {
		case "running":
			return nil
		case "prelaunch", "inmigrate", "paused":
			// VM is still coming up; wait and retry.
		default:
			return fmt.Errorf("unexpected VM status: %q", status.Status)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
