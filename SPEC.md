# **Project Nova: Next-Generation Local VM Orchestrator**

## **1\. Project Vision & Goal**

The goal of Project Nova is to build a modern, lightning-fast, and cloud-native replacement for Vagrant. As the industry has shifted towards immutable infrastructure and containerization, the need for local virtual machines remains critical for testing distributed systems, kernel-level networking, and non-containerizable workloads.

Nova will provide a frictionless developer experience for managing local VMs, eliminating legacy dependencies (Ruby, VirtualBox) in favor of a single compiled Go binary, OS-native hypervisors, OCI-compliant image distribution, and standard cloud-init provisioning.

### **Core Tenets**

1. **Single Binary, Zero Daemons:** Distributed as a single Go executable. No background services.  
2. **Rootless by Default:** Networking and virtualization must run in user-space wherever the host OS permits, eliminating the need for sudo.  
3. **Cloud-Native Parity:** Images are distributed via standard container registries (OCI). Provisioning is handled exclusively via cloud-init to mirror production cloud environments.  
4. **Declarative State with HCL:** Environments are defined by a strict, declarative configuration file (nova.hcl). Using HashiCorp Configuration Language provides a robust, human-readable structure with built-in support for variable interpolation and expressions, replacing complex Ruby DSLs while avoiding the strict, static limitations of YAML.  
5. **Native Performance:** Leverage modern OS hypervisor APIs (Apple VZ, KVM/QEMU, Hyper-V) and modern virtio drivers (VirtioFS, Virtio-net) for near-bare-metal I/O.  
6. **Chaos Engineering Ready:** Native ability to programmatically degrade, partition, and throttle network links between nodes to simulate real-world distributed system failures.

## **2\. Acceptance Criteria (v1.0 MVP)**

To consider the Minimum Viable Product complete, Nova must satisfy the following criteria:

* **AC1: CLI Lifecycle Management**  
  * A user can run nova init to generate a default configuration file.  
  * A user can run nova up, nova down, nova status, and nova nuke to manage the full lifecycle of a VM.  
  * A user can run nova shell to securely access the VM without manually managing SSH keys.  
* **AC2: Declarative Configuration**  
  * The system successfully parses a nova.hcl file defining CPU, Memory, Base Image, Port Forwarding, and Shared Folders.  
  * The parser successfully evaluates HCL variables and interpolations (e.g., dynamically setting values using ${var.project\_name}) to allow for reusable template definitions.  
* **AC3: OCI Image Distribution**  
  * The system can pull a base VM image (e.g., Ubuntu Cloud Image formatted as raw/qcow2) directly from a standard container registry (like GHCR or Docker Hub) using OCI artifact standards.  
  * Images are cached locally to ensure subsequent boots are instantaneous.  
* **AC4: Cloud-Init Provisioning**  
  * The system accepts a standard user-data cloud-init file (which remains YAML, as per the cloud-init standard).  
  * The system generates a NoCloud data source (e.g., an attached virtual CIDATA ISO or seed directory) and injects it into the VM on first boot to configure users, SSH keys, and run initial scripts.  
* **AC5: Rootless Networking & Storage**  
  * High-performance host-to-guest file sharing is established using VirtioFS (or 9p as a fallback).  
  * Port forwarding (Host \-\> Guest) works without requiring root privileges.

## **3\. Construction Plan (Stepwise Build Phases)**

Building a VM orchestrator is a complex systems engineering task. The development will be split into sequential phases, focusing on abstractions early to ensure cross-platform compatibility later.

### **Phase 1: Foundation (CLI & Configuration)**

**Goal:** Establish the project skeleton, CLI framework, and configuration parser.

1. Initialize the Go module and set up the CLI framework (e.g., Cobra).  
2. Define the configuration schemas (nova.hcl) using Go structs and HCL tags.  
3. Implement the configuration parser using the hashicorp/hcl/v2 package, adding logic to resolve variables, enforce CPU/Memory bounds, validate image URIs, and detect port collisions.  
4. Create the local state management system (e.g., a .nova hidden directory in the user's home folder to track active machine PIDs, states, and cached images).

### **Phase 2: The OCI Image Engine**

**Goal:** Treat VM disk images like container images.

1. Integrate an OCI client library (e.g., google/go-containerregistry).  
2. Build the ImageManager interface to handle pulling, unpacking, and caching VM disk artifacts from remote registries.  
3. Implement Copy-on-Write (CoW) disk creation. When a user runs nova up, the engine should create a fast, differential clone of the cached base image rather than copying the entire file, ensuring startup times in milliseconds.

### **Phase 3: The Hypervisor Abstraction Layer (HAL)**

**Goal:** Define the interface for booting VMs and implement the first OS-native engine.

1. Define the Hypervisor Go interface with methods: Start(), Stop(), ForceKill(), GetState().  
2. **Choose an initial target OS** (e.g., macOS or Linux) to build the first concrete implementation.  
   * *If macOS:* Implement the VZ engine using Go bindings for Apple's Virtualization.framework.  
   * *If Linux:* Implement the QEMU engine, wrapping the QEMU binary execution and communicating via the QEMU Machine Protocol (QMP).  
3. Wire the HAL to the CLI, allowing the tool to actually boot the underlying cloned disk image.

### **Phase 4: Cloud-Init & Bootstrapping**

**Goal:** Automate the OS setup so the user gets a fully configured machine.

1. Build the CloudInitGenerator.  
2. Implement logic to read a user-provided cloud-config.yaml and merge it with Nova's required system defaults (e.g., injecting an auto-generated SSH public key for nova shell access).  
3. Create a mechanism to package this data into a NoCloud ISO/disk format and attach it to the hypervisor as a secondary CD-ROM or flash drive during the boot process.  
4. Implement the nova shell command to look up the injected private key and establish an SSH connection to the guest.

### **Phase 5: Rootless Networking & VirtioFS**

**Goal:** Connect the VM to the host system seamlessly.

1. Implement the VirtioFS device attachment in the Hypervisor implementation to mount host directories specified in nova.hcl into the guest.  
2. Implement user-space port forwarding. Instead of using root network bridges, embed a user-space network stack (like gVisor's netstack or slirp4netns) to listen on host ports and proxy TCP/UDP traffic into the VM's virtual network interface.

### **Phase 6: Multi-Node & Cross-Platform Polish**

**Goal:** Expand beyond a single node and a single OS.

1. Expand the nova.hcl schema to support an array of nodes, allowing a single command to spin up multiple networked VMs.  
2. Implement cross-node networking logic so VMs defined in the same file can resolve each other via DNS or static IPs over a shared virtual switch.  
3. Implement the remaining Hypervisor interfaces for the other operating systems (e.g., adding Hyper-V/WSL2 support for Windows, or QEMU support if macOS was built first).  
4. Build the CI/CD pipeline to compile, test, and distribute the single Go binaries for all major architectures.

### **Phase 7: Network Chaos & The TUI (The Killer Feature)**

**Goal:** Provide programmatic and interactive network conditioning to test distributed system failures.

1. **Network Conditioning API:** Extend the user-space network stack (or leverage tc/netem on Linux hosts) to intercept packets between nodes. Implement rules for latency injection, jitter, packet loss, and hard link partitions.  
2. **Programmatic Control:** Add HCL blocks to define network states (e.g., link "node-a" "node-b" { latency \= "50ms", drop \= "5%" }) and CLI commands (e.g., nova link degrade node-a node-b \--latency 100ms).  
3. **The Nova TUI:** Build a rich terminal user interface (using a library like charmbracelet/bubbletea). When running nova monitor, the user gets a real-time dashboard showing CPU/RAM usage of nodes and an interactive map where they can toggle network partitions or drag sliders to increase latency on the fly, instantly viewing how their distributed cluster reacts.

### **Phase 8: Advanced Ecosystem & Emulation (World-Class Features)**

**Goal:** Elevate Nova to an industry-leading tool with shareable states, cross-architecture support, and safe extensibility.

1. **Instant Cluster Snapshots ("Time Travel"):** Leverage CoW disks to snapshot entire multi-node cluster states (disk, memory, network). Implement nova snapshot save and allow pushing these states to OCI registries (nova snapshot push). This allows teams to share exact, reproducible bug states instantly.  
2. **Cross-Architecture Emulation:** Integrate Apple's Rosetta 2 translation layer directly into the VZ framework bindings or leverage QEMU's TCG (Tiny Code Generator). Allow developers on ARM64 hosts to seamlessly define arch \= "amd64" in nova.hcl and run Intel VMs at near-native speeds.  
3. **Wasm-based Plugin System:** Implement a WebAssembly plugin engine using a Go library like wazero. Allow the community to build custom DNS resolvers, secret-manager injectors, or TUI widgets in any language (Rust, Go, Zig) compiled to Wasm. This provides a secure sandbox for ecosystem growth without the dependency issues of legacy Ruby plugins.