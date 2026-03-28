//go:build linux

package hypervisor

import (
	"context"
	"fmt"
	"sync"
)

// qemuEngine implements the Hypervisor interface using QEMU/KVM on Linux.
type qemuEngine struct {
	mu    sync.Mutex
	state State
	cfg   VMConfig

	// TODO: Add fields for QEMU process management and QMP client.
	// cmd    *exec.Cmd       // The running qemu-system process.
	// qmp    *qmpClient      // QMP (QEMU Machine Protocol) socket client.
}

func newQEMUEngine() (Hypervisor, error) {
	return &qemuEngine{state: StateStopped}, nil
}

func (e *qemuEngine) Start(ctx context.Context, cfg VMConfig) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.cfg = cfg
	e.state = StateStarting

	// TODO: Implement QEMU startup:
	// 1. Build qemu-system-{arch} argument list:
	//    - -machine q35,accel=kvm (or tcg fallback)
	//    - -cpu host
	//    - -smp {cpus}
	//    - -m {memoryMB}
	//    - -drive file={diskPath},format=qcow2,if=virtio
	//    - -drive file={cidataPath},format=raw,if=virtio,media=cdrom (if cloud-init)
	//    - -netdev user,id=net0,hostfwd=... -device virtio-net-pci,netdev=net0
	//    - -chardev socket,id=qmp0,path={qmpSocket},server=on,wait=off -mon chardev=qmp0,mode=control
	//    - -serial file:{logPath}
	//    - -nographic
	// 2. Start the process via exec.CommandContext(ctx, ...)
	// 3. Connect QMP client to the socket
	// 4. Wait for QMP greeting / query-status == "running"

	return fmt.Errorf("QEMU engine not yet implemented")
}

func (e *qemuEngine) Stop(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// TODO: Send {"execute": "system_powerdown"} via QMP for graceful ACPI shutdown.
	// Wait for process exit with timeout from ctx, then force kill if needed.

	return fmt.Errorf("QEMU engine not yet implemented")
}

func (e *qemuEngine) ForceKill() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// TODO: Send {"execute": "quit"} via QMP, or cmd.Process.Kill().

	return fmt.Errorf("QEMU engine not yet implemented")
}

func (e *qemuEngine) GetState() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

func (e *qemuEngine) GuestIP() (string, error) {
	// TODO: Parse QEMU user-mode networking DHCP lease,
	// or query guest agent via QMP {"execute": "guest-network-get-interfaces"}.
	return "", fmt.Errorf("guest IP discovery not yet implemented for QEMU engine")
}
