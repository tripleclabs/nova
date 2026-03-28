# QEMU/KVM Backend Implementation Notes

## File: `qemu_linux.go`

Implement `qemuEngine` struct conforming to the `Hypervisor` interface.

### Start()

Build `qemu-system-{arch}` argument list:

```
-machine q35,accel=kvm          (or accel=tcg for emulation fallback)
-cpu host
-smp {cpus}
-m {memoryMB}
-drive file={diskPath},format=qcow2,if=virtio
-drive file={cidataPath},format=raw,if=virtio,media=cdrom
-netdev user,id=net0,hostfwd=tcp::{hostPort}-:{guestPort},...
-device virtio-net-pci,netdev=net0
-chardev socket,id=qmp0,path={machineDir}/qmp.sock,server=on,wait=off
-mon chardev=qmp0,mode=control
-serial file:{logPath}
-nographic
```

- Start process via `exec.CommandContext(ctx, "qemu-system-aarch64", ...)`
- Connect QMP client to the Unix socket
- Wait for QMP greeting, then `{"execute": "qmp_capabilities"}`
- Poll `{"execute": "query-status"}` until `"running"`

### Stop()

- Send `{"execute": "system_powerdown"}` via QMP (ACPI graceful shutdown)
- Wait for process exit with context timeout
- If timeout, fall through to ForceKill

### ForceKill()

- Send `{"execute": "quit"}` via QMP
- Fallback: `cmd.Process.Kill()`

### GetState()

- Query `{"execute": "query-status"}` via QMP
- Map QMP status to `hypervisor.State`

### GuestIP()

- Parse QEMU user-mode networking DHCP lease, OR
- Query guest agent: `{"execute": "guest-network-get-interfaces"}`

## Shared Folders (9p fallback)

QEMU uses 9p for host-guest file sharing (VirtioFS requires virtiofsd daemon):

```
-fsdev local,id=fs0,path={hostPath},security_model=mapped-xattr
-device virtio-9p-pci,fsdev=fs0,mount_tag={tag}
```

Guest mount command (injected via cloud-init runcmd):
```
mount -t 9p -o trans=virtio,version=9p2000.L {tag} {guestPath}
```

The cloud-init generator already injects mount commands via `GeneratorConfig.Mounts`.
For 9p, change the filesystem type from `virtiofs` to `9p` and add the transport options.
This can be handled by adding a `MountType` field to `cloudinit.SharedMount`.
