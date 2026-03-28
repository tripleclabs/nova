package config

import (
	"strings"
	"testing"
)

// --- Single-VM mode tests ---

func TestParse_MinimalValid(t *testing.T) {
	src := []byte(`
vm {
  image = "ghcr.io/test/ubuntu:24.04"
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.VM.Image != "ghcr.io/test/ubuntu:24.04" {
		t.Errorf("image = %q, want ghcr.io/test/ubuntu:24.04", cfg.VM.Image)
	}
	if cfg.VM.CPUs != 2 {
		t.Errorf("cpus default = %d, want 2", cfg.VM.CPUs)
	}
	if cfg.VM.Memory != "2G" {
		t.Errorf("memory default = %q, want 2G", cfg.VM.Memory)
	}
}

func TestParse_FullConfig(t *testing.T) {
	src := []byte(`
variable "name" {
  default = "myvm"
}

vm {
  name   = var.name
  image  = "ghcr.io/test/ubuntu:24.04"
  cpus   = 4
  memory = "8G"

  port_forward {
    host  = 8080
    guest = 80
  }

  port_forward {
    host     = 3306
    guest    = 3306
    protocol = "tcp"
  }

  shared_folder {
    host_path  = "/tmp/share"
    guest_path = "/mnt/share"
    read_only  = true
  }
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.VM.Name != "myvm" {
		t.Errorf("name = %q, want myvm", cfg.VM.Name)
	}
	if cfg.VM.CPUs != 4 {
		t.Errorf("cpus = %d, want 4", cfg.VM.CPUs)
	}
	if len(cfg.VM.PortForwards) != 2 {
		t.Fatalf("port_forwards len = %d, want 2", len(cfg.VM.PortForwards))
	}
	if len(cfg.VM.SharedFolders) != 1 {
		t.Fatalf("shared_folders len = %d, want 1", len(cfg.VM.SharedFolders))
	}
	if !cfg.VM.SharedFolders[0].ReadOnly {
		t.Error("shared_folder[0].read_only should be true")
	}
}

func TestParse_MissingImage(t *testing.T) {
	src := []byte(`
vm {
  cpus = 2
}
`)
	_, err := Parse(src, "test.hcl")
	if err == nil {
		t.Fatal("expected error for missing image")
	}
}

func TestParse_CPUOutOfBounds(t *testing.T) {
	src := []byte(`
vm {
  image = "test:latest"
  cpus  = 256
}
`)
	_, err := Parse(src, "test.hcl")
	if err == nil {
		t.Fatal("expected error for cpu out of bounds")
	}
}

func TestParse_InvalidMemory(t *testing.T) {
	cases := []string{"2", "2T", "abc"}
	for _, mem := range cases {
		src := []byte(`
vm {
  image  = "test:latest"
  memory = "` + mem + `"
}
`)
		_, err := Parse(src, "test.hcl")
		if err == nil {
			t.Errorf("expected error for memory=%q", mem)
		}
	}
}

func TestParse_DuplicateHostPort(t *testing.T) {
	src := []byte(`
vm {
  image = "test:latest"
  port_forward {
    host  = 8080
    guest = 80
  }
  port_forward {
    host  = 8080
    guest = 443
  }
}
`)
	_, err := Parse(src, "test.hcl")
	if err == nil {
		t.Fatal("expected error for duplicate host port")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error = %q, should mention duplicate", err)
	}
}

func TestParse_VariableInterpolation(t *testing.T) {
	src := []byte(`
variable "project" {
  default = "web-app"
}

vm {
  name  = var.project
  image = "test:latest"
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.VM.Name != "web-app" {
		t.Errorf("name = %q, want web-app", cfg.VM.Name)
	}
}

func TestParse_MalformedHCL(t *testing.T) {
	src := []byte(`this is not valid HCL {{{`)
	_, err := Parse(src, "test.hcl")
	if err == nil {
		t.Fatal("expected error for malformed HCL")
	}
}

func TestParse_NoVMOrNode(t *testing.T) {
	src := []byte(`
variable "x" {
  default = "y"
}
`)
	_, err := Parse(src, "test.hcl")
	if err == nil {
		t.Fatal("expected error when neither vm nor node is present")
	}
}

func TestResolveNodes_SingleVM(t *testing.T) {
	src := []byte(`
vm {
  name  = "solo"
  image = "test:latest"
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	nodes := cfg.ResolveNodes()
	if len(nodes) != 1 {
		t.Fatalf("len = %d, want 1", len(nodes))
	}
	if nodes[0].Name != "solo" {
		t.Errorf("name = %q, want solo", nodes[0].Name)
	}
}

// --- Multi-node mode tests ---

func TestParse_MultiNode(t *testing.T) {
	src := []byte(`
defaults {
  image  = "ghcr.io/test/ubuntu:24.04"
  cpus   = 2
  memory = "4G"
}

network {
  subnet = "10.10.0.0/24"
}

node "control" {
  cpus   = 4
  memory = "8G"

  port_forward {
    host  = 6443
    guest = 6443
  }
}

node "worker-1" {
}

node "worker-2" {
  image = "ghcr.io/test/ubuntu:22.04"
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Nodes) != 3 {
		t.Fatalf("nodes len = %d, want 3", len(cfg.Nodes))
	}

	if cfg.Nodes[0].CPUs != 4 {
		t.Errorf("control.cpus = %d, want 4", cfg.Nodes[0].CPUs)
	}
	if cfg.Nodes[0].Memory != "8G" {
		t.Errorf("control.memory = %q, want 8G", cfg.Nodes[0].Memory)
	}

	if cfg.Nodes[1].Image != "ghcr.io/test/ubuntu:24.04" {
		t.Errorf("worker-1.image = %q, want inherited default", cfg.Nodes[1].Image)
	}
	if cfg.Nodes[1].CPUs != 2 {
		t.Errorf("worker-1.cpus = %d, want 2", cfg.Nodes[1].CPUs)
	}

	if cfg.Nodes[2].Image != "ghcr.io/test/ubuntu:22.04" {
		t.Errorf("worker-2.image = %q, want 22.04", cfg.Nodes[2].Image)
	}

	if cfg.Nodes[0].IP != "10.10.0.2" {
		t.Errorf("control.ip = %q, want 10.10.0.2", cfg.Nodes[0].IP)
	}
	if cfg.Nodes[1].IP != "10.10.0.3" {
		t.Errorf("worker-1.ip = %q, want 10.10.0.3", cfg.Nodes[1].IP)
	}

	if cfg.Subnet() != "10.10.0.0/24" {
		t.Errorf("subnet = %q, want 10.10.0.0/24", cfg.Subnet())
	}
}

func TestParse_MultiNode_ResolveNodes(t *testing.T) {
	src := []byte(`
defaults {
  image = "test:latest"
}

node "a" {
}

node "b" {
  cpus = 8
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatal(err)
	}
	nodes := cfg.ResolveNodes()
	if len(nodes) != 2 {
		t.Fatalf("len = %d, want 2", len(nodes))
	}
	if nodes[0].Name != "a" || nodes[1].Name != "b" {
		t.Errorf("names = %q %q", nodes[0].Name, nodes[1].Name)
	}
	if nodes[1].CPUs != 8 {
		t.Errorf("b.cpus = %d, want 8", nodes[1].CPUs)
	}
}

func TestParse_MultiNode_DuplicateName(t *testing.T) {
	src := []byte(`
defaults {
  image = "test:latest"
}

node "dup" {
}

node "dup" {
}
`)
	_, err := Parse(src, "test.hcl")
	if err == nil {
		t.Fatal("expected error for duplicate node name")
	}
}

func TestParse_MultiNode_PortConflictAcrossNodes(t *testing.T) {
	src := []byte(`
defaults {
  image = "test:latest"
}

node "a" {
  port_forward {
    host  = 8080
    guest = 80
  }
}

node "b" {
  port_forward {
    host  = 8080
    guest = 80
  }
}
`)
	_, err := Parse(src, "test.hcl")
	if err == nil {
		t.Fatal("expected error for cross-node port conflict")
	}
	if !strings.Contains(err.Error(), "conflicts") {
		t.Errorf("error = %q, should mention conflicts", err)
	}
}

func TestParse_MultiNode_ExplicitIP(t *testing.T) {
	src := []byte(`
defaults {
  image = "test:latest"
}

node "a" {
  ip = "10.0.0.50"
}

node "b" {
  ip = "10.0.0.51"
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Nodes[0].IP != "10.0.0.50" {
		t.Errorf("a.ip = %q, want 10.0.0.50", cfg.Nodes[0].IP)
	}
}

func TestParse_MultiNode_DuplicateIP(t *testing.T) {
	src := []byte(`
defaults {
  image = "test:latest"
}

node "a" {
  ip = "10.0.0.50"
}

node "b" {
  ip = "10.0.0.50"
}
`)
	_, err := Parse(src, "test.hcl")
	if err == nil {
		t.Fatal("expected error for duplicate IP")
	}
}

func TestParse_MixedVMAndNode(t *testing.T) {
	src := []byte(`
vm {
  image = "test:latest"
}

node "a" {
  image = "test:latest"
}
`)
	_, err := Parse(src, "test.hcl")
	if err == nil {
		t.Fatal("expected error for mixing vm and node")
	}
}

func TestParse_MultiNode_MissingImage(t *testing.T) {
	src := []byte(`
node "a" {
}
`)
	_, err := Parse(src, "test.hcl")
	if err == nil {
		t.Fatal("expected error for node with no image and no defaults")
	}
}

func TestSubnet_Default(t *testing.T) {
	src := []byte(`
defaults {
  image = "test:latest"
}

node "a" {
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Subnet() != "10.0.0.0/24" {
		t.Errorf("default subnet = %q, want 10.0.0.0/24", cfg.Subnet())
	}
}
