# Hyper-V Backend Implementation Spec

## Overview

This document specifies how to implement the Windows Hyper-V backend for Nova.
The implementation follows the same pattern as the macOS VZ engine
(`vz_darwin.go`) and the Linux QEMU engine (`qemu_linux.go`): a struct
conforming to the `Hypervisor` interface behind a `//go:build windows` tag.

## Files to Create

| File | Purpose |
|---|---|
| `hyperv_windows.go` | `hypervEngine` struct implementing `Hypervisor` |
| `hyperv_stub.go` | Stub for non-windows builds (`//go:build !windows`) |

You also need to update:

| File | Change |
|---|---|
| `hypervisor.go` | Add `case "windows": return newHyperVEngine()` to `New()` |
| `qemu_stub.go` | Change build tag to `//go:build !linux && !windows` if you want QEMU on Windows too, or leave as-is if Hyper-V is the only Windows backend |

## Architecture

Hyper-V is controlled via **PowerShell cmdlets** or the **WMI/HCS API**. The
PowerShell approach is simpler and what we recommend for v1. The HCS
(Host Compute Service) Go bindings (`microsoft/hcsshim`) are an option for v2.

### PowerShell approach (recommended for v1)

All VM operations map to PowerShell commands executed via `exec.Command("powershell", "-NoProfile", "-Command", ...)`.

### Go HCS approach (v2, optional)

Use `github.com/microsoft/hcsshim` for direct API access. More complex but
avoids PowerShell parsing and is faster. Consider for later.

## Hypervisor Interface Mapping

### `newHyperVEngine() (Hypervisor, error)`

```go
type hypervEngine struct {
    mu      sync.Mutex
    state   State
    cfg     VMConfig
    vmName  string // Hyper-V VM name (must be unique on the host)
}
```

Verify Hyper-V is available:
```powershell
Get-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V
```

### `Start(ctx context.Context, cfg VMConfig) error`

Steps:

1. **Create the VM:**
```powershell
New-VM -Name "{name}" -MemoryStartupBytes {memoryMB * 1024 * 1024} -Generation 2 -NoVHD
```
   Generation 2 = UEFI boot (required for modern Linux guests).

2. **Set CPU count:**
```powershell
Set-VMProcessor -VMName "{name}" -Count {cpus}
```

3. **Attach the disk (VHDX format):**
   Hyper-V requires VHDX, not qcow2 or raw. Convert before attaching:
```powershell
# Convert qcow2 → vhdx (requires qemu-img on PATH)
qemu-img convert -f qcow2 -O vhdx {diskPath} {machineDir}/disk.vhdx
```
   Then attach:
```powershell
Add-VMHardDiskDrive -VMName "{name}" -Path "{machineDir}/disk.vhdx"
```

4. **Attach cloud-init ISO:**
```powershell
Add-VMDvdDrive -VMName "{name}" -Path "{cidataPath}"
```

5. **Configure networking (Default Switch — no admin required):**
```powershell
Connect-VMNetworkAdapter -VMName "{name}" -SwitchName "Default Switch"
```
   The "Default Switch" provides NAT networking without requiring admin
   privileges or custom virtual switches. It's available on Windows 10 1709+.

6. **Disable Secure Boot for Linux guests:**
```powershell
Set-VMFirmware -VMName "{name}" -EnableSecureBoot Off
```

7. **Start the VM:**
```powershell
Start-VM -Name "{name}"
```

8. **Wait for running state:**
```powershell
# Poll until State = "Running"
(Get-VM -Name "{name}").State
```

### `Stop(ctx context.Context) error`

Graceful ACPI shutdown:
```powershell
Stop-VM -Name "{name}" -Force:$false
```

This sends an ACPI shutdown signal. The `-Force:$false` flag means it waits
for the guest to shut down gracefully. Add a timeout via the context and fall
through to `ForceKill` if it doesn't stop in time.

### `ForceKill() error`

```powershell
Stop-VM -Name "{name}" -Force -TurnOff
```

Then clean up:
```powershell
Remove-VM -Name "{name}" -Force
```

### `GetState() State`

```powershell
(Get-VM -Name "{name}").State
```

Map the Hyper-V states:

| Hyper-V State | Nova State |
|---|---|
| `Running` | `StateRunning` |
| `Off` | `StateStopped` |
| `Starting` | `StateStarting` |
| `Stopping` | `StateRunning` (still alive) |
| `Saved`, `Paused` | `StateStopped` |
| `Critical`, `Other` | `StateError` |

### `GuestIP() (string, error)`

```powershell
(Get-VMNetworkAdapter -VMName "{name}").IPAddresses
```

This returns the guest IP(s) via Hyper-V integration services. Filter for
the first IPv4 address that isn't `169.254.*` (link-local).

## Disk Format

Hyper-V uses **VHDX** format exclusively. The orchestrator needs to convert
the qcow2 overlay to VHDX before starting. Add this to `vm.go` alongside the
existing darwin raw conversion:

```go
if runtime.GOOS == "windows" {
    slog.Info("converting overlay to vhdx for Hyper-V")
    vhdxPath, err := image.ConvertToVHDX(overlayPath)
    if err != nil {
        o.store.Delete(machineID)
        return fmt.Errorf("converting to vhdx: %w", err)
    }
    overlayPath = vhdxPath
}
```

Add to `internal/image/disk.go`:

```go
func ConvertToVHDX(qcow2Path string) (string, error) {
    vhdxPath := strings.TrimSuffix(qcow2Path, ".qcow2") + ".vhdx"
    cmd := exec.Command("qemu-img", "convert",
        "-f", "qcow2", "-O", "vhdx", "-o", "subformat=dynamic",
        qcow2Path, vhdxPath,
    )
    var stderr bytes.Buffer
    cmd.Stderr = &stderr
    if err := cmd.Run(); err != nil {
        return "", fmt.Errorf("qemu-img convert to vhdx: %s: %w",
            strings.TrimSpace(stderr.String()), err)
    }
    os.Remove(qcow2Path)
    return vhdxPath, nil
}
```

## Shared Folders (SMB)

Hyper-V doesn't support VirtioFS or 9p. Use **SMB sharing** instead:

```powershell
# Create an SMB share on the host
New-SmbShare -Name "nova-{tag}" -Path "{hostPath}" -FullAccess "Everyone"

# Mount in guest via cloud-init runcmd
mount -t cifs //{hostIP}/nova-{tag} {guestPath} -o guest,vers=3.0
```

Set `MountType` to `"smb"` in `cloudinit.SharedMount` for Windows.

Add to the cloud-init generator:
```go
if fsType == "smb" {
    opts = "guest,vers=3.0"
    // Tag is the SMB share name, GuestPath is the mount point.
    // Device is //<host-ip>/<share-name>
    device = fmt.Sprintf("//%s/%s", hostIP, m.Tag)
}
```

The guest needs the `cifs-utils` package. Add it to cloud-init packages
when running on Windows host.

## Port Forwarding

The "Default Switch" provides NAT. Port forwarding uses `netsh`:

```powershell
netsh interface portproxy add v4tov4 ^
    listenport={hostPort} listenaddress=127.0.0.1 ^
    connectport={guestPort} connectaddress={guestIP}
```

Clean up on stop:
```powershell
netsh interface portproxy delete v4tov4 ^
    listenport={hostPort} listenaddress=127.0.0.1
```

Alternatively, use our existing Go user-space port forwarder from
`internal/network/portforward.go` — it's pure Go and works on Windows
without `netsh`. This is probably simpler and more consistent.

## Cloud-Init

Cloud-init works the same way — the CIDATA ISO is attached as a DVD drive.
Hyper-V Generation 2 VMs boot from UEFI and can read the ISO. No changes
needed to the cloud-init generator or ISO builder.

## EFI / UEFI

Generation 2 Hyper-V VMs use UEFI by default. No EFI variable store is
needed — Hyper-V manages it internally. Disable Secure Boot for Linux guests.

## Prerequisites

Users need:
- **Windows 10 Pro/Enterprise/Education** or **Windows 11 Pro** (Hyper-V not available on Home)
- **Hyper-V enabled**: `Enable-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V -All`
- **qemu-img**: install via `choco install qemu-img` or `winget install qemu`
- User must be in the **Hyper-V Administrators** group (no full admin needed after setup)

## Build Tags

```
hyperv_windows.go   → //go:build windows
hyperv_stub.go      → //go:build !windows
```

## Testing

The integration test (`integration_test.go`) should work on Windows with
minimal changes:
- Alpine provides UEFI cloud images for amd64 that boot on Hyper-V Gen2
- The `make integration` target works if `qemu-img` and PowerShell are available
- The `nova shell` command uses `ssh` which is available on Windows 10+ natively

## Skeleton

```go
//go:build windows

package hypervisor

import (
    "context"
    "fmt"
    "log/slog"
    "os/exec"
    "strings"
    "sync"
    "time"
)

type hypervEngine struct {
    mu     sync.Mutex
    state  State
    cfg    VMConfig
    vmName string
}

func newHyperVEngine() (Hypervisor, error) {
    // Verify Hyper-V is available.
    out, err := psRun("(Get-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V).State")
    if err != nil || !strings.Contains(out, "Enabled") {
        return nil, fmt.Errorf("Hyper-V is not enabled on this system")
    }
    return &hypervEngine{state: StateStopped}, nil
}

func (e *hypervEngine) Start(ctx context.Context, cfg VMConfig) error {
    // TODO: implement per spec above
    return fmt.Errorf("Hyper-V engine not yet implemented")
}

func (e *hypervEngine) Stop(ctx context.Context) error {
    return fmt.Errorf("Hyper-V engine not yet implemented")
}

func (e *hypervEngine) ForceKill() error {
    return fmt.Errorf("Hyper-V engine not yet implemented")
}

func (e *hypervEngine) GetState() State {
    e.mu.Lock()
    defer e.mu.Unlock()
    return e.state
}

func (e *hypervEngine) GuestIP() (string, error) {
    return "", fmt.Errorf("Hyper-V engine not yet implemented")
}

// psRun executes a PowerShell command and returns stdout.
func psRun(script string) (string, error) {
    cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
    out, err := cmd.CombinedOutput()
    return strings.TrimSpace(string(out)), err
}
```
