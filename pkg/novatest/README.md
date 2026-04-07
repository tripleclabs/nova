# novatest

Go test SDK for Nova. Spin up real VM clusters inside `go test`, run commands
over SSH, inject network chaos, and snapshot/restore state.

## Quick start

```go
import "github.com/tripleclabs/nova/pkg/novatest"

func TestMyApp(t *testing.T) {
    cluster := novatest.NewCluster(t, novatest.WithHCL(`
        defaults { image = "alpine:3.23" cpus = 2 memory = "1G" }
        node "n1" {}
        node "n2" {}
    `))
    cluster.WaitReady()

    out := cluster.Node("n1").Exec("echo hello")
    // out == "hello\n"
}
```

That's it. Everything else (daemon lifecycle, SSH keys, cloud-init, networking,
cleanup) is handled automatically.

## Prerequisites

- `qemu-img` must be installed
- First run downloads the base image (cached at `~/.nova/cache` for subsequent runs)
- Tests should use a build tag: `//go:build integration`
- Set `NOVA_BIN` env var to skip building nova from source each test run

## What you configure

The HCL string passed to `WithHCL` defines your cluster. There are two modes:

**Multi-node** (use `defaults` + `node` blocks):

```hcl
defaults {
    image  = "ubuntu:24.04"   # required: base image
    cpus   = 2                # CPU cores per node
    memory = "2G"             # RAM per node
}

node "db"    { memory = "4G" }   # override defaults per node
node "app"   {}
node "cache" {}
```

**Single-VM** (use a `vm` block):

```hcl
vm {
    name   = "myvm"
    image  = "alpine:3.23"
    cpus   = 2
    memory = "1G"
}
```

You can also load config from a file with `novatest.WithHCLFile("path/to/nova.hcl")`.

### Available images

Built-in: `ubuntu:24.04`, `ubuntu:22.04`, `alpine:3.23`

Custom images built with `nova image build` work too, referenced by their tag
(e.g. `myorg/myimage:v1`).

## What you DON'T configure

The following are all automatic — do not set these up manually:

- **Cloud-init**: Nova generates and injects cloud-init config internally.
  Hostname, SSH keys, user accounts, and network config are all handled.
  You do not need to write cloud-config YAML for tests.
- **SSH**: Nova creates an internal user with SSH keys. All `Exec` calls
  use this. You do not need to manage keys or SSH config.
- **Networking**: Nodes get deterministic IPs and can reach each other
  by name. No manual network setup needed.
- **Cleanup**: `t.Cleanup()` tears down the daemon and all VMs automatically,
  even if the test fails or panics.
- **Isolation**: Each `NewCluster` call gets its own temp state directory
  and daemon process. Tests cannot interfere with each other.

## API

### Cluster

| Method | Description |
|--------|-------------|
| `NewCluster(t, opts...)` | Create cluster, boot VMs, register cleanup |
| `WaitReady()` | Block until all nodes are reachable via SSH |
| `Node(name) *Node` | Get a node handle by name |
| `Nodes() []*Node` | Get all nodes |
| `Snapshot(name)` | Save named snapshot of all node disks |
| `Restore(name)` | Revert all nodes to a named snapshot |
| `Partition(a, b)` | Hard network partition between two nodes |
| `Heal(a, b)` | Remove all conditions between two nodes |
| `Degrade(a, b, opts...)` | Add latency/jitter/loss to a link |
| `HealAll()` | Remove all network conditions cluster-wide |

### Node

| Method | Description |
|--------|-------------|
| `Exec(cmd) string` | Run command, return stdout, fail test on non-zero exit |
| `ExecResult(cmd) ExecResult` | Run command, return full result (exit code, stdout, stderr) |
| `ExecResultWithTimeout(cmd, d) ExecResult` | Same as ExecResult with custom timeout |
| `ExecExpectFailure(cmd) ExecResult` | Run command, fail test if exit code IS zero |
| `Stop()` | Graceful shutdown |
| `Start()` | Start a previously stopped node |
| `Kill()` | Force-terminate (simulates power failure, disk preserved) |
| `IsRunning() bool` | Check if node is in "running" state |

### Link options (for Degrade)

```go
cluster.Degrade("n1", "n2",
    novatest.WithLatency(100*time.Millisecond),
    novatest.WithJitter(20*time.Millisecond),
    novatest.WithLoss(0.05),  // 5% packet loss
)
```

### Helpers

```go
// Poll until condition is true (500ms intervals), fail after timeout.
novatest.Eventually(t, 30*time.Second, func() bool {
    result := cluster.Node("n1").ExecResult("curl -s http://n2:8080/health")
    return result.ExitCode == 0
})

// Assert condition stays false for the full duration.
novatest.Never(t, 10*time.Second, func() bool {
    result := cluster.Node("n2").ExecResult("cat /tmp/leader")
    return strings.Contains(result.Stdout, "n2")
})
```

## Multi-node networking

Nodes in a multi-node cluster are connected by a userspace L2 switch and can
reach each other over static IPs. Add a `network` block to enable it:

```hcl
defaults { image = "ubuntu:24.04" cpus = 2 memory = "2G" }
network  { subnet = "10.0.0.0/24" }
node "server" {}
node "client" {}
```

IPs are assigned in declaration order: gateway is `10.0.0.1`, first node is
`10.0.0.2`, second is `10.0.0.3`, and so on. All nodes also have internet
access (NAT on Linux, separate NAT NIC on macOS).

### Testing a network service

```go
func TestHTTPService(t *testing.T) {
    cluster := novatest.NewCluster(t, novatest.WithHCL(`
        defaults { image = "ubuntu:24.04" cpus = 2 memory = "2G" }
        network  { subnet = "10.0.0.0/24" }
        node "server" {}
        node "client" {}
    `))
    cluster.WaitReady()

    // Start a service on the server (10.0.0.2).
    cluster.Node("server").Exec("echo 'hello from server' | sudo tee /var/www/html/index.html")
    cluster.Node("server").Exec("sudo apt-get update && sudo apt-get install -y nginx")

    // Reach it from the client.
    novatest.Eventually(t, 30*time.Second, func() bool {
        result := cluster.Node("client").ExecResult("curl -s http://10.0.0.2")
        return strings.Contains(result.Stdout, "hello from server")
    })
}
```

### Testing with network chaos

```go
// Verify your service handles a partition gracefully.
cluster.Partition("primary", "replica")
// ... assert replica detects the failure ...
cluster.Heal("primary", "replica")

// Simulate a degraded WAN link.
cluster.Degrade("client", "server",
    novatest.WithLatency(200*time.Millisecond),
    novatest.WithLoss(0.1),
)
```

### Platform support

| Platform | Mechanism | Inter-VM | Internet |
|----------|-----------|----------|----------|
| Linux | TAP + socketpairs + nftables NAT | Yes | Yes |
| macOS | VZ socketpairs + NAT NIC | Yes | Yes |
| Other | Stub | No | No |

## Patterns

### Snapshots for fast iteration

Avoid re-provisioning for every test. Set up once, snapshot, restore:

```go
func TestSuite(t *testing.T) {
    cluster := novatest.NewCluster(t, novatest.WithHCL(`
        defaults { image = "ubuntu:24.04" cpus = 2 memory = "2G" }
        node "n1" {}
    `))
    cluster.WaitReady()
    cluster.Node("n1").Exec("apt-get update && apt-get install -y redis")
    cluster.Snapshot("baseline")

    t.Run("test1", func(t *testing.T) {
        cluster.Restore("baseline")
        // fast: no reinstall needed
    })

    t.Run("test2", func(t *testing.T) {
        cluster.Restore("baseline")
        // also starts from a clean redis install
    })
}
```

### Testing failure scenarios

```go
// Simulate network partition
cluster.Partition("primary", "replica")
// ... assert replica detects the failure ...
cluster.Heal("primary", "replica")

// Simulate power failure
cluster.Node("db").Kill()
cluster.Node("db").Start()
cluster.WaitReady()
// ... assert data survived ...

// Simulate degraded network
cluster.Degrade("client", "server",
    novatest.WithLatency(200*time.Millisecond),
    novatest.WithLoss(0.1),
)
```

### Use ExecResult for non-fatal checks

```go
// Exec() fails the test on non-zero exit. Use ExecResult() when
// you want to inspect the result yourself:
result := cluster.Node("n1").ExecResult("cat /etc/something")
if result.ExitCode != 0 {
    t.Logf("file missing (expected in this scenario): %s", result.Stderr)
}
```
