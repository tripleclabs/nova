# Building a Nova Test Driver

Nova exposes its full functionality over a gRPC API via a Unix domain socket. The Go `pkg/novatest` package is a thin wrapper around this API — you can build an equivalent test driver in any language with gRPC support.

This guide explains how.

## Architecture

```
Your test code (any language)
      |
      | gRPC over Unix socket
      v
Nova daemon (nova daemon --state-dir /tmp/test-xxx)
      |
      | Manages VMs, networking, snapshots
      v
Hypervisor (Apple VZ / QEMU-KVM)
```

Your test driver needs to:

1. Start a `nova daemon` process with an isolated `--state-dir`
2. Connect to its Unix socket via gRPC
3. Call RPCs to manage VMs, run commands, inject chaos
4. Shut down the daemon when done

## The Proto Definition

The full API is defined in [`proto/nova/v1/nova.proto`](proto/nova/v1/nova.proto). Generate client stubs for your language using `protoc`:

```bash
# Python
pip install grpcio-tools
python -m grpc_tools.protoc \
  -Iproto -I$(brew --prefix protobuf)/include \
  --python_out=. --grpc_python_out=. \
  proto/nova/v1/nova.proto

# TypeScript (using ts-proto)
npx protoc --ts_proto_out=./src \
  -Iproto -I$(brew --prefix protobuf)/include \
  proto/nova/v1/nova.proto

# Rust (using tonic)
# Add to build.rs:
# tonic_build::compile_protos("proto/nova/v1/nova.proto")?;
```

## Lifecycle of a Test

Every test follows the same pattern regardless of language:

### 1. Create an isolated state directory

Each test gets its own temporary directory. This ensures complete isolation — no shared state between tests.

```
/tmp/nova-test-xxxxx/
  daemon.sock          # gRPC socket (created by daemon)
  daemon.pid           # PID file
  machines/            # VM disks and state
  cache/               # Image cache (symlink to shared cache)
  snapshots/           # Snapshot metadata
```

Symlink the shared image cache to avoid re-downloading images every test:

```
ln -s ~/.nova/cache /tmp/nova-test-xxxxx/cache
```

### 2. Start the daemon

```bash
nova daemon --state-dir /tmp/nova-test-xxxxx
```

Start this as a background process. Wait for `daemon.sock` to appear before connecting.

### 3. Connect via gRPC

Connect to the Unix domain socket:

```
unix:///tmp/nova-test-xxxxx/daemon.sock
```

No TLS, no authentication — the socket is local and scoped to the test.

### 4. Apply configuration

Send HCL as a string via the `Apply` RPC. This boots all VMs.

```protobuf
rpc Apply(ApplyRequest) returns (ApplyResponse);

message ApplyRequest {
  string hcl_config = 1;        // Inline HCL content
  string cloud_config_path = 2; // Optional path to cloud-config.yaml
}
```

The response tells you the node names, IPs, and state.

### 5. Wait for readiness

VMs need a few seconds to boot and start SSH. Use `WaitReady`:

```protobuf
rpc WaitReady(WaitReadyRequest) returns (google.protobuf.Empty);
```

### 6. Run your test

Execute commands, inject chaos, save/restore snapshots — see the API reference below.

### 7. Shutdown

Call `Shutdown` to destroy all VMs and stop the daemon:

```protobuf
rpc Shutdown(google.protobuf.Empty) returns (google.protobuf.Empty);
```

Then kill the daemon process as a safety net.

## API Reference

### Cluster Lifecycle

| RPC | Description |
|-----|-------------|
| `Apply(ApplyRequest)` | Boot VMs from inline HCL config. Returns node names, IPs, state. |
| `Destroy(DestroyRequest)` | Tear down all or a specific node. |
| `Status()` | Get state of all nodes. |
| `Shutdown()` | Destroy all VMs and exit the daemon. |

### Node Control

| RPC | Description |
|-----|-------------|
| `NodeStop(NodeRequest)` | Graceful shutdown of a node. |
| `NodeStart(NodeRequest)` | Start a previously stopped node. |
| `NodeKill(NodeRequest)` | Force-terminate (simulates power failure). Disk preserved. |
| `NodeStatus(NodeRequest)` | Get state, IP, OS of a single node. |

### SSH Execution

| RPC | Description |
|-----|-------------|
| `Exec(ExecRequest)` | Run a command on a node. Returns exit code, stdout, stderr. |
| `WaitReady(WaitReadyRequest)` | Block until a node is reachable via SSH. |

### Network Chaos

| RPC | Description |
|-----|-------------|
| `LinkDegrade(LinkDegradeRequest)` | Add latency, jitter, and/or packet loss between two nodes. |
| `LinkPartition(LinkPairRequest)` | Hard network partition between two nodes. |
| `LinkHeal(LinkPairRequest)` | Remove all conditions between two nodes. |
| `LinkReset()` | Clear all network conditions cluster-wide. |
| `LinkStatus()` | List all active network conditions. |

### Snapshots

| RPC | Description |
|-----|-------------|
| `SnapshotSave(SnapshotRequest)` | Save a named snapshot of all node disks. |
| `SnapshotRestore(SnapshotRequest)` | Revert all nodes to a saved snapshot. |
| `SnapshotList()` | List saved snapshots. |
| `SnapshotDelete(SnapshotRequest)` | Delete a snapshot. |

### Export

| RPC | Description |
|-----|-------------|
| `Export(ExportRequest)` | Sysprep, shutdown, and export a VM as a standalone disk image. |

## Example: Python Test Driver

```python
import grpc
import subprocess
import tempfile
import time
import os
from pathlib import Path

# Generated from proto:
import nova.v1.nova_pb2 as pb
import nova.v1.nova_pb2_grpc as rpc
from google.protobuf.duration_pb2 import Duration
from google.protobuf.empty_pb2 import Empty

class NovaCluster:
    def __init__(self, hcl: str):
        self.state_dir = tempfile.mkdtemp(prefix="nova-test-")
        self.socket = f"{self.state_dir}/daemon.sock"

        # Symlink shared image cache.
        shared = Path.home() / ".nova" / "cache"
        if shared.exists():
            os.symlink(shared, f"{self.state_dir}/cache")

        # Start daemon.
        self.daemon = subprocess.Popen(
            ["nova", "daemon", "--state-dir", self.state_dir],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )

        # Wait for socket.
        for _ in range(100):
            if os.path.exists(self.socket):
                break
            time.sleep(0.1)
        else:
            raise RuntimeError("daemon socket not found")

        # Connect.
        self.channel = grpc.insecure_channel(f"unix://{self.socket}")
        self.client = rpc.NovaStub(self.channel)

        # Boot VMs.
        resp = self.client.Apply(pb.ApplyRequest(hcl_config=hcl))
        self.nodes = {n.name: n for n in resp.nodes}

    def wait_ready(self):
        for name in self.nodes:
            self.client.WaitReady(pb.WaitReadyRequest(
                node=name,
                timeout=Duration(seconds=120),
            ))

    def exec(self, node: str, command: str) -> pb.ExecResponse:
        return self.client.Exec(pb.ExecRequest(
            node=node, command=command,
            timeout=Duration(seconds=30),
        ))

    def partition(self, a: str, b: str):
        self.client.LinkPartition(pb.LinkPairRequest(node_a=a, node_b=b))

    def heal(self, a: str, b: str):
        self.client.LinkHeal(pb.LinkPairRequest(node_a=a, node_b=b))

    def snapshot(self, name: str):
        self.client.SnapshotSave(pb.SnapshotRequest(name=name))

    def restore(self, name: str):
        self.client.SnapshotRestore(pb.SnapshotRequest(name=name))

    def destroy(self):
        try:
            self.client.Shutdown(Empty())
        except:
            pass
        self.channel.close()
        self.daemon.kill()
        self.daemon.wait()


# Usage in pytest:
import pytest

@pytest.fixture
def cluster():
    c = NovaCluster("""
        defaults { image = "ubuntu:24.04" cpus = 2 memory = "2G" }
        node "n1" {}
        node "n2" {}
    """)
    c.wait_ready()
    yield c
    c.destroy()

def test_partition_recovery(cluster):
    # Write data to n1.
    cluster.exec("n1", "echo hello > /tmp/data")

    # Partition the nodes.
    cluster.partition("n1", "n2")

    # n2 can't reach n1.
    result = cluster.exec("n2", "ping -c1 -W1 n1 || echo unreachable")
    assert "unreachable" in result.stdout

    # Heal and verify connectivity.
    cluster.heal("n1", "n2")
    result = cluster.exec("n2", "ping -c1 -W5 n1")
    assert result.exit_code == 0
```

## Example: TypeScript / Vitest

```typescript
import { createChannel, createClient } from 'nice-grpc';
import { NovaDefinition } from './generated/nova/v1/nova';
import { execSync, spawn } from 'child_process';
import { mkdtempSync, existsSync, symlinkSync } from 'fs';
import { join } from 'path';
import { tmpdir, homedir } from 'os';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';

class NovaCluster {
  private stateDir: string;
  private daemon: ReturnType<typeof spawn>;
  private client: ReturnType<typeof createClient<typeof NovaDefinition>>;
  nodes: Map<string, { name: string; ip: string }> = new Map();

  static async create(hcl: string): Promise<NovaCluster> {
    const cluster = new NovaCluster();
    cluster.stateDir = mkdtempSync(join(tmpdir(), 'nova-test-'));

    // Symlink shared cache.
    const shared = join(homedir(), '.nova', 'cache');
    if (existsSync(shared)) {
      symlinkSync(shared, join(cluster.stateDir, 'cache'));
    }

    const socket = join(cluster.stateDir, 'daemon.sock');

    // Start daemon.
    cluster.daemon = spawn('nova', ['daemon', '--state-dir', cluster.stateDir], {
      stdio: 'ignore', detached: true,
    });

    // Wait for socket.
    for (let i = 0; i < 100; i++) {
      if (existsSync(socket)) break;
      await new Promise(r => setTimeout(r, 100));
    }

    // Connect.
    const channel = createChannel(`unix://${socket}`);
    cluster.client = createClient(NovaDefinition, channel);

    // Boot VMs.
    const resp = await cluster.client.apply({ hclConfig: hcl });
    for (const node of resp.nodes) {
      cluster.nodes.set(node.name, { name: node.name, ip: node.ip });
    }

    return cluster;
  }

  async waitReady() {
    for (const [name] of this.nodes) {
      await this.client.waitReady({ node: name, timeout: { seconds: 120n } });
    }
  }

  async exec(node: string, command: string) {
    return this.client.exec({ node, command, timeout: { seconds: 30n } });
  }

  async partition(a: string, b: string) {
    await this.client.linkPartition({ nodeA: a, nodeB: b });
  }

  async heal(a: string, b: string) {
    await this.client.linkHeal({ nodeA: a, nodeB: b });
  }

  async destroy() {
    try { await this.client.shutdown({}); } catch {}
    this.daemon.kill();
  }
}

// Usage:
describe('distributed system', () => {
  let cluster: NovaCluster;

  beforeEach(async () => {
    cluster = await NovaCluster.create(`
      defaults { image = "ubuntu:24.04" cpus = 2 memory = "2G" }
      node "n1" {}
      node "n2" {}
    `);
    await cluster.waitReady();
  });

  afterEach(async () => {
    await cluster.destroy();
  });

  it('survives a network partition', async () => {
    await cluster.partition('n1', 'n2');
    const result = await cluster.exec('n2', 'ping -c1 -W1 n1 || echo unreachable');
    expect(result.stdout).toContain('unreachable');

    await cluster.heal('n1', 'n2');
    const healed = await cluster.exec('n2', 'ping -c1 -W5 n1');
    expect(healed.exitCode).toBe(0);
  });
});
```

## Key Patterns

### Isolation

Always use a fresh `--state-dir` per test. This is the single most important pattern — it guarantees no state leaks between tests.

### Image cache sharing

Symlink `~/.nova/cache` into your test state dir. Without this, every test re-downloads base images.

### Cleanup

Always call `Shutdown` and kill the daemon process, even on test failure. In Go, `t.Cleanup()` handles this. In Python, use `try/finally` or `pytest` fixtures. In TypeScript, use `afterEach`.

### Snapshots for fast iteration

For tests that need a common baseline (packages installed, services configured), save a snapshot after setup and restore it in subsequent tests instead of re-provisioning:

```
Setup test:    Apply → WaitReady → provision → SnapshotSave("baseline")
Fast tests:    Apply → SnapshotRestore("baseline") → run assertions
```

### Eventually / Never helpers

Distributed systems are async. Polling helpers avoid flaky tests:

- **Eventually**: poll a condition until true or timeout
- **Never**: assert a condition stays false for a duration

The Go SDK provides these in `pkg/novatest/helpers.go`. Implement the same pattern in your language.

## Go SDK Reference

If you're writing Go tests, use `pkg/novatest` directly instead of building your own:

```go
func TestMyApp(t *testing.T) {
    cluster := novatest.NewCluster(t, novatest.WithHCL(`
        defaults { image = "ubuntu:24.04" cpus = 2 memory = "2G" }
        node "n1" {}
        node "n2" {}
    `))
    cluster.WaitReady()

    // Run commands.
    out := cluster.Node("n1").Exec("hostname")
    assert.Equal(t, "n1\n", out)

    // Network chaos.
    cluster.Partition("n1", "n2")
    cluster.Heal("n1", "n2")

    // Snapshots.
    cluster.Snapshot("baseline")
    cluster.Restore("baseline")

    // Async assertions.
    novatest.Eventually(t, 30*time.Second, func() bool {
        result := cluster.Node("n1").ExecResult("curl -s http://n2:8080/health")
        return result.ExitCode == 0
    })

    // Cleanup is automatic via t.Cleanup().
}
```
