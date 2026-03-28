# Project Nova: Task Plan

## Phase 1: Foundation (CLI & Configuration)

### 1.1 Project Scaffold
- [x] Initialize Go module (`go mod init`)
- [x] Set up directory structure: `cmd/`, `internal/`, `pkg/`
- [x] Add Cobra CLI dependency and wire up root command
- [x] Implement `nova version` command as a smoke test

### 1.2 CLI Commands (Stubs)
- [x] Register command stubs: `init`, `up`, `down`, `status`, `nuke`, `shell`
- [x] Add global flags: `--config`, `--verbose`, `--no-color`
- [x] Wire up structured logging (e.g., `slog`)

### 1.3 HCL Configuration Parser
- [x] Define Go structs for `nova.hcl` schema: VM block (cpu, memory, image, arch), port forwarding, shared folders, variables
- [x] Integrate `hashicorp/hcl/v2` and `hclsimple` for decoding
- [x] Implement variable resolution and interpolation (`${var.*}`)
- [x] Add validation: CPU/memory bounds, image URI format, port collision detection
- [x] Write unit tests for parser (valid configs, malformed configs, edge cases)

### 1.4 `nova init` Command
- [x] Generate a default `nova.hcl` template with commented examples
- [x] Generate a default `cloud-config.yaml` stub
- [x] Prevent overwriting existing files (prompt or `--force` flag)

### 1.5 Local State Management
- [x] Define state directory layout (`~/.nova/` — machines, cache, keys)
- [x] Implement state store: create/read/update/delete machine records (JSON or bolt)
- [x] Store per-machine metadata: PID, state (running/stopped), config hash, creation time
- [x] Implement state locking to prevent concurrent mutations on the same machine
- [x] Write unit tests for state store CRUD operations

---

## Phase 2: OCI Image Engine

### 2.1 OCI Client Integration
- [x] Add `google/go-containerregistry` dependency
- [x] Define `ImageManager` interface: `Pull()`, `Resolve()`, `List()`, `Delete()`
- [x] Implement registry authentication (anonymous + token-based for GHCR/Docker Hub)

### 2.2 Image Pull & Cache
- [x] Implement `Pull()` — download OCI artifact layers to local cache (`~/.nova/cache/images/`)
- [x] Support content-addressable storage (digest-based dedup)
- [x] Add progress reporting (download bytes / total bytes)
- [x] Implement `List()` — show cached images with size and age
- [x] Implement `Delete()` — prune cached images

### 2.3 Copy-on-Write Disk Creation
- [x] Detect disk format of cached base image (raw vs qcow2)
- [x] Implement CoW clone using qcow2 backing files (`qemu-img create -b`)
- [x] For raw images, create qcow2 overlay with raw backing format
- [x] Store per-machine overlay disks in `~/.nova/machines/<id>/disk.qcow2`
- [x] Write integration tests: format detection, qcow2 overlay, raw overlay

---

## Phase 3: Hypervisor Abstraction Layer (HAL)

### 3.1 HAL Interface
- [x] Define `Hypervisor` interface: `Start()`, `Stop()`, `ForceKill()`, `GetState()`, `GuestIP()`
- [x] Define `VMConfig` struct passed to the hypervisor (CPU, memory, disks, network, shares)
- [x] Define `VMState` enum: `Starting`, `Running`, `Stopped`, `Error`
- [x] Implement hypervisor factory for selecting backend by platform (`New()`)

### 3.2 macOS: Apple Virtualization.framework (VZ) Engine
- [x] Add `Code-Hex/vz/v3` Go bindings dependency
- [x] Implement `VZEngine.Start()` — configure VZ VM (EFI bootloader, CPU, memory, storage, entropy, NAT network)
- [x] Implement `VZEngine.Stop()` — graceful shutdown via `RequestStop()`, fallback to force kill
- [x] Implement `VZEngine.ForceKill()` — force stop via VZ `Stop()`
- [x] Implement `VZEngine.GetState()` — map VZ state via `StateChangedNotify` watcher
- [x] Handle full UEFI disk boot via `EFIBootLoader`
- [x] Wire serial console output to a log file per machine
- [x] Add VirtioFS shared folder support in VZ config builder
- [x] Add non-darwin stub (`vz_stub.go`) for cross-compilation

### 3.3 Wire HAL to CLI
- [x] Implement `nova up`: parse config -> pull image -> create CoW disk -> start VM -> update state
- [x] Implement `nova down`: lookup machine -> graceful stop -> update state
- [x] Implement `nova nuke`: force kill -> delete disk overlay -> delete state record
- [x] Implement `nova status`: read state store -> display table of machines with state, uptime, resources
- [x] Build `Orchestrator` layer (`internal/vm/`) connecting config, image, hypervisor, and state
- [x] Write unit tests for HAL interface, VM config, memory parsing, tag sanitization

---

## Phase 4: Cloud-Init & Bootstrapping

### 4.1 SSH Key Management
- [x] Auto-generate an ED25519 keypair per machine on first `nova up`
- [x] Store keys in `~/.nova/machines/<id>/ssh/`
- [x] Clean up keys on `nova nuke` (handled by state store `Delete` which removes machine dir)

### 4.2 Cloud-Init Generator
- [x] Implement `CloudInitGenerator` — merges user `cloud-config.yaml` with Nova defaults
- [x] Nova defaults: inject SSH public key, set hostname, disable password auth, create `nova` user
- [x] Preserve user-provided packages, runcmd, write_files, mounts, bootcmd without clobbering
- [x] Protect `users` block from user override (security)
- [x] Write unit tests for merge logic (defaults-only, with user config, user override blocked, list merging, missing file)

### 4.3 NoCloud Data Source
- [x] Generate `meta-data` (instance-id, local-hostname) and `user-data` from merged config
- [x] Package into a NoCloud ISO (CIDATA volume label) using pure-Go `kdomanski/iso9660`
- [x] Attach ISO as secondary block device via HAL `VMConfig.CIDATAPath`
- [x] Write tests verifying ISO structure and content

### 4.4 `nova shell` Command
- [x] Look up machine state and retrieve stored private key path
- [x] Connect via SSH to localhost:2222 (NAT port forward to guest:22)
- [x] Exec `ssh` with StrictHostKeyChecking disabled for ephemeral VMs
- [x] Support `-c` flag for non-interactive command execution
- [x] Handle connection retries during boot (30 attempts, 2s intervals)

---

## Phase 5: Rootless Networking & VirtioFS

### 5.1 VirtioFS Shared Folders
- [x] Parse `shared_folder` blocks from `nova.hcl` (host path, guest mount point, read-only flag) — done in Phase 1
- [x] Implement VirtioFS device attachment in VZ engine — done in Phase 3
- [x] Add 9p implementation notes for Linux/QEMU backend (`QEMU_TODO.md`)
- [x] Inject guest-side mount commands via cloud-init `mounts` + `runcmd` (mkdir)
- [x] Write tests for mount injection (standalone and merged with user config)

### 5.2 User-Space Port Forwarding
- [x] Implement TCP port forwarding: listen on host port, proxy to guest IP:port
- [x] Implement UDP port forwarding with reply proxying
- [x] Pure user-space networking via Go `net` package (no root required)
- [x] Parse `port_forward` blocks from `nova.hcl` — done in Phase 1
- [x] Detect and error on host port conflicts at startup (`CheckPortsAvailable`)
- [x] Write tests: TCP forwarding end-to-end, UDP forwarding, stop cleanup, port conflict detection

---

## Phase 6: Multi-Node & Cross-Platform

### 6.1 Multi-Node HCL Schema
- [x] Extend `nova.hcl` schema with `node` blocks (labeled), `defaults` block, `network` block
- [x] Each node inherits from `defaults` but can override cpu, memory, image, arch
- [x] Validate unique node names, unique IPs, and non-conflicting port forwards across nodes
- [x] `ResolveNodes()` method normalizes both single-VM and multi-node configs
- [x] Auto-assign IPs from subnet (`.2`, `.3`, ...) when not specified
- [x] Updated `nova init` template with commented multi-node example

### 6.2 Cross-Node Networking
- [x] Auto-assign static IPs from configurable subnet (default `10.0.0.0/24`)
- [x] Inject `/etc/hosts` entries via cloud-init `write_files` (append to `/etc/hosts`)
- [x] Orchestrator builds host entries from all resolved nodes and passes to each
- [x] Write tests: multi-node parsing, IP assignment, cross-node port conflict, duplicate IP

### 6.3 Linux QEMU Backend
- [ ] Implement `QEMUEngine` conforming to `Hypervisor` interface (see `QEMU_TODO.md`)
- [ ] Wrap QEMU binary execution with correct flags (KVM accel, virtio devices)
- [ ] Implement QMP (QEMU Machine Protocol) client for `Stop()`, `GetState()`
- [ ] Wire VirtioFS via virtiofsd or 9p for shared folders
- [ ] Test lifecycle on Linux

### 6.4 CI/CD & Distribution
- [x] Set up GitHub Actions CI: lint (golangci-lint), tests (macOS + Linux matrix)
- [x] Cross-compile for darwin/arm64, darwin/amd64, linux/arm64, linux/amd64
- [x] Set up GoReleaser config for tagged releases with checksums and archives
- [x] Release workflow triggered on `v*` tags, publishes to GitHub Releases

---

## Phase 7: Network Chaos & TUI

### 7.1 Network Conditioning API
- [x] `Conditioner` with `SetRule()`, `GetRule()`, `RemoveRule()`, `AllRules()`, `Reset()`
- [x] `Degrade()` — set latency, jitter, packet loss on a link
- [x] `Partition()` / `Heal()` — hard partition and recovery
- [x] `ShouldDrop()` / `Delay()` — per-packet decisions with probabilistic loss and jitter
- [x] Order-independent link keys (a<->b == b<->a)
- [x] 13 unit tests covering all operations, edge cases, jitter variance

### 7.2 HCL & CLI Integration
- [x] Add `link` blocks to `nova.hcl` schema (node_a, node_b labels + latency/jitter/loss/down)
- [x] `nova link degrade <a> <b> --latency --jitter --loss` with duration/percent parsing
- [x] `nova link partition <a> <b>` and `nova link heal <a> <b>`
- [x] `nova link status` — tabwriter output of all active conditions
- [x] `nova link reset` — clear all conditions
- [x] State persisted to `~/.nova/chaos.json`

### 7.3 Nova TUI (Monitor)
- [x] Add `charmbracelet/bubbletea`, `bubbles`, `lipgloss` dependencies
- [x] `nova monitor` launches full-screen alt-screen TUI
- [x] Node status panel: name, state indicator (● ○ ◐ ✗), uptime
- [x] Network topology panel: link list with latency/jitter/loss/partition status
- [x] Interactive controls: [p] toggle partition, [h] heal, [j/k] navigate, [q] quit
- [x] Real-time polling (2s refresh) of machine state and chaos rules

---

## Phase 8: Advanced Ecosystem & Emulation

### 8.1 Cluster Snapshots ("Time Travel")
- [x] `nova snapshot save <name>` — creates qcow2 internal snapshots for all machines + saves metadata
- [x] `nova snapshot restore <name>` — reverts all machine disks to saved snapshot via `qemu-img snapshot -a`
- [x] `nova snapshot list` — tabwriter table of all snapshots with name, machine count, timestamp
- [x] `nova snapshot delete <name>` — removes internal qcow2 snapshots + metadata file
- [x] `nova snapshot push <name> <ref>` — packs snapshot (convert each disk to standalone qcow2), pushes as OCI artifact
- [x] `nova snapshot pull <ref>` — pulls OCI artifact, unpacks into local store, recreates machine records
- [x] Snapshot name validation (alphanumeric + hyphens/underscores)
- [x] Rollback on partial save failure
- [x] `image/oci.go` — `PushDir()` / `PullDir()` for multi-layer OCI push/pull of arbitrary directories
- [x] 9 tests: save/list, duplicate save, restore, restore non-existent, delete, pack, unpack, name validation, empty cluster

### 8.2 Cross-Architecture Emulation
- [x] `arch.go` — `HostArch()`, `NeedsEmulation()`, `normalizeArch()` with alias support (x86_64/aarch64)
- [x] `Arch` field added to `VMConfig`, wired through orchestrator from config
- [x] macOS/ARM: Rosetta 2 integration in VZ engine — auto-installs if needed, attaches `LinuxRosettaDirectoryShare`
- [x] Guest-side: cloud-init injects Rosetta VirtioFS mount + `update-binfmts` registration for transparent x86_64 execution
- [x] `GenericPlatformConfiguration` set on all VZ VMs
- [x] Linux/QEMU TCG fallback supported via `qemu_linux.go` (from Linux branch)
- [x] 7 arch tests: host detection, normalization, aliases, emulation detection
- [x] 2 cloud-init Rosetta tests: standalone and combined with mounts

### 8.3 Lua Plugin System
- [x] Integrate `gopher-lua` as the embedded scripting runtime (pure Go, no CGO)
- [x] Plugin discovery: auto-loads all `.lua` files from `~/.nova/plugins/`
- [x] Hook system: `nova.register(hook, fn)` for `dns_resolve`, `on_vm_start`, `on_vm_stop`, `on_snapshot`, `on_link`
- [x] `CallHook()` returns first non-nil result (DNS), `CallHookAll()` collects all results (events)
- [x] Built-in host functions: `nova.log()`, `nova.register()`
- [x] `RegisterHostFunc()` API for app-level extensions (e.g., secret managers)
- [x] Graceful error handling: bad plugins logged and skipped, hook errors don't crash
- [x] Example plugins: `dns-resolver.lua` (custom .nova DNS), `secret-injector.lua` (lifecycle hooks)
- [x] 12 tests: load/skip/bad plugins, DNS hooks, lifecycle events, multi-plugin dispatch, custom host funcs, close
