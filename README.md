# Nova

A modern, lightning-fast, cloud-native replacement for Vagrant. Single binary, rootless by default, built on OS-native hypervisors.

Nova manages local virtual machines using declarative HCL configuration, OCI image distribution, and cloud-init provisioning — the same tools and patterns you use in production.

## Features

- **Single binary, zero daemons** — one Go executable, no background services
- **Rootless by default** — user-space networking and hypervisor APIs, no sudo
- **OS-native hypervisors** — Apple Virtualization.framework on macOS, QEMU/KVM on Linux
- **OCI image distribution** — pull VM images from container registries (GHCR, Docker Hub)
- **Cloud-init provisioning** — standard cloud-config YAML, just like production clouds
- **Shell provisioners** — run scripts and inline commands over SSH after boot
- **Golden image export** — sysprep and export VMs as standalone images (replaces Packer)
- **Declarative HCL config** — variables, interpolation, multi-node clusters
- **Multi-node clusters** — spin up networked node groups with a single command
- **Configurable users** — define SSH users with keys, password hashes, and groups
- **Network chaos engineering** — inject latency, packet loss, and partitions between nodes
- **Interactive TUI** — real-time monitoring dashboard with interactive network controls
- **Cluster snapshots** — save/restore/share full cluster state via OCI registries or portable archives
- **Cross-architecture** — run x86_64 VMs on ARM Macs via Rosetta 2
- **Lua plugin system** — extend Nova with custom DNS resolvers, secret injectors, and lifecycle hooks

## Quick Start

```bash
# Initialize a new project
nova init

# Edit nova.hcl to configure your VM, then:
nova up

# SSH into the VM
nova shell

# Check status
nova status

# Tear it down
nova down
```

## Installation

### Prerequisites

- **Go 1.22+**
- **qemu-img** — for disk overlays and snapshots
  - macOS: `brew install qemu`
  - Linux: `apt install qemu-utils` or `dnf install qemu-img`

### Linux

```bash
go install github.com/tripleclabs/nova/cmd/nova@latest
```

### macOS

macOS requires the `com.apple.security.virtualization` entitlement to use Virtualization.framework. Install and codesign in one block:

```bash
go install github.com/tripleclabs/nova/cmd/nova@latest
codesign --force -s - --entitlements <(cat <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
    <key>com.apple.security.virtualization</key><true/>
</dict></plist>
EOF
) $(go env GOPATH)/bin/nova
```

The binary lands in `$GOPATH/bin/nova` (usually `~/go/bin/nova`). Make sure that's on your `$PATH`.

### Shell completions

```bash
# bash
nova completion bash > /etc/bash_completion.d/nova

# zsh
nova completion zsh > "${fpath[1]}/_nova"

# fish
nova completion fish > ~/.config/fish/completions/nova.fish
```

## Images

Nova has built-in support for common Linux distributions. Just use the shorthand in `nova.hcl` and Nova will pull the official cloud image automatically on `nova up`:

```hcl
vm {
  image = "ubuntu:24.04"   # or ubuntu:22.04, alpine:3.21, alpine:3.20
}
```

No manual downloading required. The image is cached locally after the first pull.

### Pre-fetching

To download an image ahead of time (e.g. before going offline):

```bash
nova image get ubuntu:24.04
nova image get alpine:3.21
```

### Custom images

Package any local qcow2 or raw disk image into the nova cache:

```bash
nova image build myimage.qcow2 --tag nova.local/myimage:latest --os ubuntu
```

Use `--push` to also upload it to a registry so your team can pull it automatically:

```bash
nova image build myimage.qcow2 --tag ghcr.io/myorg/myimage:latest --os ubuntu --push
```

Once pushed, anyone can reference `ghcr.io/myorg/myimage:latest` in `nova.hcl` and it will be pulled on `nova up`.

### Managing the cache

```bash
nova image list                        # show all cached images
nova image rm nova.local/ubuntu:24.04  # remove an image
```

## Configuration

Nova uses HCL configuration files. Run `nova init` to generate a starter config.

### Single VM

```hcl
variable "project_name" {
  default = "my-project"
}

vm {
  name   = var.project_name
  image  = "ubuntu:24.04"
  cpus   = 2
  memory = "2G"

  port_forward {
    host  = 8080
    guest = 80
  }

  shared_folder {
    host_path  = "."
    guest_path = "/workspace"
  }
}
```

### Multi-Node Cluster

```hcl
defaults {
  image  = "ubuntu:24.04"
  cpus   = 2
  memory = "2G"
}

network {
  subnet = "10.0.0.0/24"
}

node "control" {
  cpus   = 4
  memory = "8G"
  port_forward {
    host  = 6443
    guest = 6443
  }
}

node "worker-1" {}
node "worker-2" {}
```

Nodes automatically get static IPs from the subnet and `/etc/hosts` entries for name resolution.

### Cloud-Init

Place a `cloud-config.yaml` alongside your `nova.hcl`:

```yaml
#cloud-config
package_update: true
packages:
  - curl
  - git
runcmd:
  - echo "Hello from Nova" > /tmp/hello
```

Nova merges your config with its own defaults (SSH key injection, hostname, nova user) without clobbering your entries.

### Shell Provisioners

Run scripts and commands over SSH after the VM boots. Provisioners run during `nova up`, not just during export.

```hcl
# Top-level provisioners run on ALL nodes first
provisioner "shell" {
  inline = [
    "apt-get update",
    "apt-get install -y nginx curl",
  ]
}

node "control" {
  image = "ubuntu:24.04"

  # Node-level provisioners run after top-level ones
  provisioner "shell" {
    script  = "./scripts/control-plane.sh"
    timeout = "10m"
  }
}
```

Provisioner options:

| Field | Description |
|---|---|
| `script` | Path to a local script to upload and execute (mutually exclusive with `inline`) |
| `inline` | List of commands to run in a single SSH session (joined with `&&`) |
| `timeout` | Max execution time (default: `5m`) |
| `env` | Map of environment variables to pass to the script |

### Users

Define a user to create alongside the internal `nova` user. When a user block is present, `nova shell` connects as that user.

```hcl
user {
  name    = "deploy"
  ssh_key = file("~/.ssh/id_ed25519.pub")
  groups  = ["sudo", "docker"]
  shell   = "/bin/bash"
}

vm {
  image = "ubuntu:24.04"
}
```

User options:

| Field | Description |
|---|---|
| `name` | Username (required, cannot be `nova` or `root`) |
| `ssh_key` | SSH public key string |
| `password_hash` | Pre-hashed password (crypt(3) format, use `mkpasswd` to generate) |
| `groups` | Additional groups for the user |
| `shell` | Login shell (defaults to distro default) |

At least one of `ssh_key` or `password_hash` is required. The user block can be placed at top-level (applies to all nodes) or inside `vm`/`node` blocks (node-level overrides top-level).

## Exporting Images

Nova can replace [Packer](https://www.packer.io/) for building golden VM images. The workflow: define a VM, provision it, then export a standalone disk image.

```bash
nova up                                           # Boot and provision
nova export myvm                                  # Export as qcow2 (default)
nova export myvm --format vmdk -o golden.vmdk     # Export as VMware image
nova export myvm --snapshot pre-export            # Snapshot before export for safety
nova export myvm --zero                           # Zero free space for smaller images
```

Supported output formats:

| Format | Target Hypervisors |
|---|---|
| `qcow2` (default) | KVM, libvirt, Proxmox |
| `raw` | Direct dd-to-disk, Apple VZ |
| `vmdk` | VMware ESXi, Workstation, Fusion |
| `vhdx` | Microsoft Hyper-V |
| `ova` | VMware vSphere/ESXi, Proxmox (includes OVF descriptor) |

Export automatically runs **sysprep** to clean machine-specific state (machine-id, SSH host keys, cloud-init state, DHCP leases, logs) so the image boots cleanly in a new environment. The cleanup is OS-aware — Ubuntu/Debian, Alpine, and Fedora each get appropriate treatment.

**A `user` block is required for export** — this prevents the internal `nova` user from leaking into production images. Use `--no-clean` to bypass this for debugging.

## CLI Reference

| Command | Description |
|---|---|
| `nova init` | Generate default `nova.hcl` and `cloud-config.yaml` |
| `nova up` | Create and start VMs from configuration |
| `nova down [name]` | Gracefully stop a VM |
| `nova status` | Show status of all managed VMs |
| `nova destroy [name]` | Force kill a VM and delete all data |
| `nova shell [name]` | SSH into a running VM (uses configured user if set) |
| `nova shell -c "cmd"` | Run a command in the VM |
| `nova export [name]` | Export a VM as a standalone disk image |
| `nova image get <distro:version>` | Pre-fetch a known distro image |
| `nova image build <file> --tag <ref>` | Package a local disk image into the cache |
| `nova image list` | List cached images |
| `nova image rm <ref>` | Remove a cached image |
| `nova link degrade a b` | Add latency/loss between nodes |
| `nova link partition a b` | Hard partition two nodes |
| `nova link heal a b` | Remove conditions between nodes |
| `nova link status` | Show active network conditions |
| `nova link reset` | Clear all network conditions |
| `nova monitor` | Launch interactive TUI dashboard |
| `nova snapshot save <name>` | Snapshot all machine disks |
| `nova snapshot restore <name>` | Revert to a saved snapshot |
| `nova snapshot list` | List saved snapshots |
| `nova snapshot delete <name>` | Delete a snapshot |
| `nova snapshot export <name>` | Export snapshot as portable archive |
| `nova snapshot import <file>` | Import snapshot from archive |
| `nova snapshot push <name> <ref>` | Push snapshot to OCI registry |
| `nova snapshot pull <ref>` | Pull snapshot from OCI registry |
| `nova version` | Print version |

## Network Chaos Engineering

Degrade the link between two nodes:

```bash
nova link degrade control worker-1 --latency 100ms --jitter 20ms --loss 5%
```

Create a hard network partition:

```bash
nova link partition worker-1 worker-2
```

Heal a link:

```bash
nova link heal worker-1 worker-2
```

Launch the interactive TUI to visualize and control network conditions in real time:

```bash
nova monitor
```

## Snapshots

Save the state of your entire cluster (instant, uses qcow2 CoW snapshots):

```bash
nova snapshot save before-migration
```

Restore it later:

```bash
nova snapshot restore before-migration
```

Share cluster states with your team — via portable files or OCI registries:

```bash
# Portable file (no registry needed)
nova snapshot export before-migration -o before-migration.novasnap
# scp, airdrop, USB, whatever
nova snapshot import before-migration.novasnap

# Or via OCI registries
nova snapshot push before-migration ghcr.io/myteam/nova-snaps:bug-123
nova snapshot pull ghcr.io/myteam/nova-snaps:bug-123
```

## Plugins

Nova has a Lua plugin system. Place `.lua` files in `~/.nova/plugins/` and they'll be loaded automatically.

### Example: Custom DNS Resolver

```lua
-- ~/.nova/plugins/dns-resolver.lua
local records = {
    ["api.nova"]   = "10.0.0.10",
    ["db.nova"]    = "10.0.0.20",
}

nova.register("dns_resolve", function(hostname)
    return records[hostname]
end)
```

### Available Hooks

| Hook | Arguments | Return |
|---|---|---|
| `dns_resolve` | hostname | IP string or nil |
| `on_vm_start` | vm_name | (ignored) |
| `on_vm_stop` | vm_name | (ignored) |
| `on_snapshot` | snapshot_name | (ignored) |
| `on_link` | node_a, node_b, action | (ignored) |

### Host Functions

| Function | Description |
|---|---|
| `nova.log(msg)` | Log a message from your plugin |
| `nova.register(hook, fn)` | Register a hook handler |

See [`examples/plugins/`](examples/plugins/) for complete examples.

## Architecture

```
nova (single binary)
 |
 +-- internal/config      HCL parser, multi-node schema, validation
 +-- internal/image       OCI pull/cache, CoW disks, snapshot push/pull
 +-- internal/hypervisor  HAL interface, Apple VZ engine, QEMU engine
 +-- internal/cloudinit   SSH keygen, cloud-config merging, ISO builder
 +-- internal/network     Port forwarding, chaos conditioning
 +-- internal/state       Machine state store, locking
 +-- internal/vm          Orchestrator (ties everything together), export pipeline
 +-- internal/provisioner Shell provisioner execution over SSH
 +-- internal/sysprep     OS-aware image cleanup for export
 +-- internal/snapshot    Cluster snapshots, pack/unpack, portable archives
 +-- internal/plugin      Lua plugin runtime, hook dispatch
 +-- internal/cmd         Cobra CLI commands, TUI monitor
```

## Requirements

- **macOS** (Apple Silicon or Intel): macOS 13+ for Virtualization.framework
- **Linux**: QEMU/KVM with `qemu-system` and `qemu-img`
- **Both**: `qemu-img` for CoW disk overlays and snapshots

## Development

```bash
make build    # Build the binary
make test     # Run all tests
make clean    # Remove build artifacts
```

## License

MIT
