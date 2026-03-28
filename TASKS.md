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
- [ ] Define `Hypervisor` interface: `Start()`, `Stop()`, `ForceKill()`, `GetState()`
- [ ] Define `VMConfig` struct passed to the hypervisor (CPU, memory, disks, network, devices)
- [ ] Define `VMState` enum: `Starting`, `Running`, `Stopped`, `Error`
- [ ] Implement hypervisor registry/factory for selecting backend by platform

### 3.2 macOS: Apple Virtualization.framework (VZ) Engine
- [ ] Add `Code-Hex/vz` Go bindings dependency
- [ ] Implement `VZEngine.Start()` — configure VZ VM (bootloader, CPU, memory, storage, entropy)
- [ ] Implement `VZEngine.Stop()` — graceful shutdown via VZ API
- [ ] Implement `VZEngine.ForceKill()` — force stop
- [ ] Implement `VZEngine.GetState()` — map VZ state to `VMState`
- [ ] Handle EFI boot with kernel/initrd or full UEFI disk boot
- [ ] Wire serial console output to a log file per machine

### 3.3 Wire HAL to CLI
- [ ] Implement `nova up`: parse config -> pull image (if needed) -> create CoW disk -> start VM -> update state
- [ ] Implement `nova down`: lookup machine -> graceful stop -> update state
- [ ] Implement `nova nuke`: force kill -> delete disk overlay -> delete state record
- [ ] Implement `nova status`: read state store -> display table of machines with state, uptime, resources
- [ ] Write integration test: full `up` -> `status` -> `down` lifecycle

---

## Phase 4: Cloud-Init & Bootstrapping

### 4.1 SSH Key Management
- [ ] Auto-generate an ED25519 keypair per machine on first `nova up`
- [ ] Store keys in `~/.nova/machines/<id>/ssh/`
- [ ] Clean up keys on `nova nuke`

### 4.2 Cloud-Init Generator
- [ ] Implement `CloudInitGenerator` — merges user `cloud-config.yaml` with Nova defaults
- [ ] Nova defaults: inject SSH public key, set hostname, disable password auth
- [ ] Preserve user-provided packages, runcmd, write_files without clobbering
- [ ] Validate merged cloud-config against cloud-init schema (basic structural checks)
- [ ] Write unit tests for merge logic (user-only, defaults-only, combined, conflicting keys)

### 4.3 NoCloud Data Source
- [ ] Generate `meta-data` and `user-data` files from merged config
- [ ] Package into a NoCloud ISO (cidata volume label) using a pure-Go ISO9660 writer or `mkisofs`
- [ ] Attach ISO as secondary CD-ROM device in HAL `VMConfig`
- [ ] Verify cloud-init runs on first boot by checking SSH connectivity

### 4.4 `nova shell` Command
- [ ] Look up machine state and retrieve stored private key path
- [ ] Determine guest IP (from DHCP lease or VZ API)
- [ ] Establish interactive SSH session using `golang.org/x/crypto/ssh` or exec `ssh`
- [ ] Support `-c` flag for non-interactive command execution
- [ ] Handle connection retries during boot (VM may not be ready immediately)

---

## Phase 5: Rootless Networking & VirtioFS

### 5.1 VirtioFS Shared Folders
- [ ] Parse `shared_folder` blocks from `nova.hcl` (host path, guest mount point, read-only flag)
- [ ] Implement VirtioFS device attachment in VZ engine
- [ ] Add 9p fallback for Linux/QEMU backend
- [ ] Inject guest-side mount commands via cloud-init `runcmd` or `mounts`
- [ ] Test: write file on host, verify visible in guest and vice versa

### 5.2 User-Space Port Forwarding
- [ ] Implement TCP port forwarding: listen on host port, proxy to guest IP:port
- [ ] Implement UDP port forwarding
- [ ] Use gVisor netstack or native VZ NAT for user-space networking (no root)
- [ ] Parse `port_forward` blocks from `nova.hcl`
- [ ] Detect and error on host port conflicts at startup
- [ ] Test: forward host:8080 -> guest:80, verify HTTP connectivity

---

## Phase 6: Multi-Node & Cross-Platform

### 6.1 Multi-Node HCL Schema
- [ ] Extend `nova.hcl` schema to support multiple `node` blocks
- [ ] Each node inherits global defaults but can override CPU, memory, image
- [ ] Validate unique node names and non-conflicting port forwards across nodes

### 6.2 Cross-Node Networking
- [ ] Implement a virtual switch / shared network segment for nodes in the same file
- [ ] Assign static IPs or run a lightweight DHCP within the virtual network
- [ ] Inject `/etc/hosts` entries via cloud-init so nodes can resolve each other by name
- [ ] Test: two nodes can ping each other by hostname

### 6.3 Linux QEMU Backend
- [ ] Implement `QEMUEngine` conforming to `Hypervisor` interface
- [ ] Wrap QEMU binary execution with correct flags (KVM accel, virtio devices)
- [ ] Implement QMP (QEMU Machine Protocol) client for `Stop()`, `GetState()`
- [ ] Wire VirtioFS via virtiofsd or 9p for shared folders
- [ ] Test lifecycle on Linux

### 6.4 CI/CD & Distribution
- [ ] Set up GitHub Actions: lint, unit tests, integration tests (macOS + Linux matrix)
- [ ] Cross-compile for darwin/arm64, darwin/amd64, linux/arm64, linux/amd64
- [ ] Set up GoReleaser for tagged releases with checksums and archives
- [ ] Publish binaries to GitHub Releases

---

## Phase 7: Network Chaos & TUI

### 7.1 Network Conditioning API
- [ ] Define `NetworkConditioner` interface: `SetLatency()`, `SetPacketLoss()`, `SetJitter()`, `Partition()`, `Reset()`
- [ ] Implement packet interception in user-space network stack (or `tc`/`netem` on Linux)
- [ ] Support per-link rules (node-a <-> node-b)

### 7.2 HCL & CLI Integration
- [ ] Add `link` blocks to `nova.hcl` for declarative network conditions
- [ ] Implement `nova link degrade <a> <b> --latency --loss --jitter`
- [ ] Implement `nova link partition <a> <b>` and `nova link heal <a> <b>`
- [ ] Implement `nova link status` to show current conditions

### 7.3 Nova TUI (Monitor)
- [ ] Add `charmbracelet/bubbletea` and `bubbles` dependencies
- [ ] Implement `nova monitor` command launching the TUI
- [ ] Build node status panel: per-node CPU, RAM, state, uptime
- [ ] Build network topology view: visual map of nodes and links
- [ ] Interactive controls: toggle partitions, adjust latency sliders
- [ ] Real-time updates via polling or event stream from running VMs

---

## Phase 8: Advanced Ecosystem & Emulation

### 8.1 Cluster Snapshots ("Time Travel")
- [ ] Implement `nova snapshot save <name>` — snapshot all node disks (qcow2 snapshots) + state
- [ ] Implement `nova snapshot restore <name>` — revert cluster to saved state
- [ ] Implement `nova snapshot list` and `nova snapshot delete`
- [ ] Implement `nova snapshot push <name>` — pack and push snapshot to OCI registry
- [ ] Implement `nova snapshot pull <ref>` — pull and unpack from registry

### 8.2 Cross-Architecture Emulation
- [ ] Support `arch = "amd64"` on ARM hosts via Rosetta 2 (VZ framework)
- [ ] Support `arch = "arm64"` on x86 hosts via QEMU TCG
- [ ] Auto-detect host arch and select acceleration vs emulation path
- [ ] Test: boot an amd64 image on an arm64 Mac

### 8.3 Wasm Plugin System
- [ ] Integrate `wazero` as the Wasm runtime
- [ ] Define plugin API: host functions exposed to Wasm guests (DNS hooks, event hooks, TUI widgets)
- [ ] Implement plugin discovery and loading from `~/.nova/plugins/`
- [ ] Build an example plugin (e.g., custom DNS resolver)
- [ ] Document the plugin authoring guide
