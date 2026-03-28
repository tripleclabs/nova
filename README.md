# Nova

A modern, lightning-fast, cloud-native replacement for Vagrant. Single binary, rootless by default, built on OS-native hypervisors.

Nova manages local virtual machines using declarative HCL configuration, OCI image distribution, and cloud-init provisioning — the same tools and patterns you use in production.

## Features

- **Single binary, zero daemons** — one Go executable, no background services
- **Rootless by default** — user-space networking and hypervisor APIs, no sudo
- **OS-native hypervisors** — Apple Virtualization.framework on macOS, QEMU/KVM on Linux
- **OCI image distribution** — pull VM images from container registries (GHCR, Docker Hub)
- **Cloud-init provisioning** — standard cloud-config YAML, just like production clouds
- **Declarative HCL config** — variables, interpolation, multi-node clusters
- **Multi-node clusters** — spin up networked node groups with a single command
- **Network chaos engineering** — inject latency, packet loss, and partitions between nodes
- **Interactive TUI** — real-time monitoring dashboard with interactive network controls
- **Cluster snapshots** — save/restore/share full cluster state via OCI registries
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

### From source

```bash
git clone https://github.com/3clabs/nova.git
cd nova
make build
# Binary is at ./nova
```

### From releases

Download the latest binary from [GitHub Releases](https://github.com/3clabs/nova/releases) for your platform.

## Configuration

Nova uses HCL configuration files. Run `nova init` to generate a starter config.

### Single VM

```hcl
variable "project_name" {
  default = "my-project"
}

vm {
  name   = var.project_name
  image  = "ghcr.io/3clabs/ubuntu-cloud:24.04"
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
  image  = "ghcr.io/3clabs/ubuntu-cloud:24.04"
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

## CLI Reference

| Command | Description |
|---|---|
| `nova init` | Generate default `nova.hcl` and `cloud-config.yaml` |
| `nova up` | Create and start VMs from configuration |
| `nova down [name]` | Gracefully stop a VM |
| `nova status` | Show status of all managed VMs |
| `nova nuke [name]` | Force kill a VM and delete all data |
| `nova shell [name]` | SSH into a running VM |
| `nova shell -c "cmd"` | Run a command in the VM |
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

Share exact cluster states with your team via OCI registries:

```bash
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
 +-- internal/vm          Orchestrator (ties everything together)
 +-- internal/snapshot    Cluster snapshots, pack/unpack
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
