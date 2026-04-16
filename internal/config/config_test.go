package config

import (
	"os"
	"path/filepath"
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

func TestLoad_RealFile(t *testing.T) {
	dir := t.TempDir()
	hclContent := []byte(`
vm {
  image  = "ghcr.io/test/ubuntu:24.04"
  cpus   = 4
  memory = "4G"
}
`)
	path := filepath.Join(dir, "nova.hcl")
	if err := os.WriteFile(path, hclContent, 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.VM == nil {
		t.Fatal("VM should not be nil")
	}
	if cfg.VM.Image != "ghcr.io/test/ubuntu:24.04" {
		t.Errorf("image = %q, want ghcr.io/test/ubuntu:24.04", cfg.VM.Image)
	}
	if cfg.VM.CPUs != 4 {
		t.Errorf("cpus = %d, want 4", cfg.VM.CPUs)
	}
	if cfg.VM.Memory != "4G" {
		t.Errorf("memory = %q, want 4G", cfg.VM.Memory)
	}
}

func TestLoad_NonExistentFile(t *testing.T) {
	_, err := Load("/tmp/non-existent-nova-config-file.hcl")
	if err == nil {
		t.Fatal("expected error for non-existent file")
	}
}

func TestResolveNodes_MultiNode_IPPropagation(t *testing.T) {
	src := []byte(`
defaults {
  image = "test:latest"
}

network {
  subnet = "192.168.1.0/24"
}

node "web" {
  cpus = 4
}

node "db" {
  memory = "8G"
}

node "cache" {
  ip = "192.168.1.100"
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	nodes := cfg.ResolveNodes()
	if len(nodes) != 3 {
		t.Fatalf("len = %d, want 3", len(nodes))
	}

	// Verify IPs are propagated to resolved nodes.
	if nodes[0].IP != "192.168.1.2" {
		t.Errorf("web.IP = %q, want 192.168.1.2", nodes[0].IP)
	}
	if nodes[1].IP != "192.168.1.3" {
		t.Errorf("db.IP = %q, want 192.168.1.3", nodes[1].IP)
	}
	if nodes[2].IP != "192.168.1.100" {
		t.Errorf("cache.IP = %q, want 192.168.1.100", nodes[2].IP)
	}

	// Verify defaults are inherited.
	if nodes[0].Image != "test:latest" {
		t.Errorf("web.Image = %q, want test:latest", nodes[0].Image)
	}
	if nodes[0].CPUs != 4 {
		t.Errorf("web.CPUs = %d, want 4", nodes[0].CPUs)
	}
	if nodes[1].Memory != "8G" {
		t.Errorf("db.Memory = %q, want 8G", nodes[1].Memory)
	}
	// Default memory should be applied to web.
	if nodes[0].Memory != "2G" {
		t.Errorf("web.Memory = %q, want default 2G", nodes[0].Memory)
	}
}

func TestParseSubnetBase_Invalid(t *testing.T) {
	cases := []string{
		"not-a-subnet",
		"10.0.0/24",     // Only 3 octets.
		"10/8",          // Only 1 octet.
		"",              // Empty.
		"abcd/24",       // Non-numeric.
	}
	for _, c := range cases {
		_, err := parseSubnetBase(c)
		if err == nil {
			t.Errorf("parseSubnetBase(%q) should fail", c)
		}
	}
}

func TestParse_EmptyMemoryGetsDefault(t *testing.T) {
	// When memory is not specified, it should get the default "2G".
	src := []byte(`
vm {
  image = "test:latest"
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.VM.Memory != "2G" {
		t.Errorf("memory = %q, want default 2G", cfg.VM.Memory)
	}
}

func TestResolveNodes_SingleVM_DefaultName(t *testing.T) {
	src := []byte(`
vm {
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
	if nodes[0].Name != "default" {
		t.Errorf("name = %q, want 'default' when no name specified", nodes[0].Name)
	}
}

func TestParse_PortForwardDefaultProtocol(t *testing.T) {
	src := []byte(`
vm {
  image = "test:latest"
  port_forward {
    host  = 8080
    guest = 80
  }
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.VM.PortForwards[0].Protocol != "tcp" {
		t.Errorf("protocol = %q, want tcp (default)", cfg.VM.PortForwards[0].Protocol)
	}
}

func TestParse_InvalidPortRange(t *testing.T) {
	// Host port 0 is invalid.
	src := []byte(`
vm {
  image = "test:latest"
  port_forward {
    host  = 0
    guest = 80
  }
}
`)
	_, err := Parse(src, "test.hcl")
	if err == nil {
		t.Fatal("expected error for port 0")
	}
}

func TestParse_NodeDefaultsInheritance(t *testing.T) {
	// Test that nodes inherit all fields from defaults block.
	src := []byte(`
defaults {
  image  = "default-image:latest"
  cpus   = 4
  memory = "4G"
  arch   = "aarch64"
}

node "a" {
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	n := cfg.Nodes[0]
	if n.Image != "default-image:latest" {
		t.Errorf("image = %q, want default-image:latest", n.Image)
	}
	if n.CPUs != 4 {
		t.Errorf("cpus = %d, want 4", n.CPUs)
	}
	if n.Memory != "4G" {
		t.Errorf("memory = %q, want 4G", n.Memory)
	}
	if n.Arch != "aarch64" {
		t.Errorf("arch = %q, want aarch64", n.Arch)
	}
}

func TestParse_VMArchDefault(t *testing.T) {
	src := []byte(`
vm {
  image = "test:latest"
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.VM.Arch != "host" {
		t.Errorf("arch = %q, want 'host' (default)", cfg.VM.Arch)
	}
}

func TestValidateMemory_EdgeCases(t *testing.T) {
	valid := []string{"1M", "512M", "1G", "64G", "1024m", "2g"}
	for _, m := range valid {
		if err := validateMemory(m); err != nil {
			t.Errorf("validateMemory(%q) should pass: %v", m, err)
		}
	}

	invalid := []string{"", "2", "M", "G", "2T", "abc", "2.5G", "-1G"}
	for _, m := range invalid {
		if err := validateMemory(m); err == nil {
			t.Errorf("validateMemory(%q) should fail", m)
		}
	}
}

func TestParse_LinkBlocks(t *testing.T) {
	src := []byte(`
defaults {
  image = "test:latest"
}

node "a" {
}

node "b" {
}

link "a" "b" {
  latency = "50ms"
  jitter  = "10ms"
  loss    = "5%"
  down    = false
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Links) != 1 {
		t.Fatalf("links len = %d, want 1", len(cfg.Links))
	}
	link := cfg.Links[0]
	if link.NodeA != "a" || link.NodeB != "b" {
		t.Errorf("link nodes = %q/%q, want a/b", link.NodeA, link.NodeB)
	}
	if link.Latency != "50ms" {
		t.Errorf("latency = %q, want 50ms", link.Latency)
	}
	if link.Loss != "5%" {
		t.Errorf("loss = %q, want 5%%", link.Loss)
	}
}

func TestSubnet_Custom(t *testing.T) {
	cfg := &Config{
		Network: &Network{Subnet: "172.16.0.0/16"},
	}
	if cfg.Subnet() != "172.16.0.0/16" {
		t.Errorf("Subnet() = %q, want 172.16.0.0/16", cfg.Subnet())
	}
}

func TestSubnet_NilNetwork(t *testing.T) {
	cfg := &Config{}
	if cfg.Subnet() != "10.0.0.0/24" {
		t.Errorf("Subnet() = %q, want default 10.0.0.0/24", cfg.Subnet())
	}
}

func TestSubnet_EmptySubnet(t *testing.T) {
	cfg := &Config{
		Network: &Network{Subnet: ""},
	}
	if cfg.Subnet() != "10.0.0.0/24" {
		t.Errorf("Subnet() = %q, want default 10.0.0.0/24", cfg.Subnet())
	}
}

func TestParse_GuestPortOutOfRange(t *testing.T) {
	src := []byte(`
vm {
  image = "test:latest"
  port_forward {
    host  = 8080
    guest = 70000
  }
}
`)
	_, err := Parse(src, "test.hcl")
	if err == nil {
		t.Fatal("expected error for guest port out of range")
	}
	if !strings.Contains(err.Error(), "guest") {
		t.Errorf("error = %q, should mention guest", err)
	}
}

func TestParseSubnetBase_Valid(t *testing.T) {
	base, err := parseSubnetBase("10.0.0.0/24")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if base != "10.0.0." {
		t.Errorf("base = %q, want 10.0.0.", base)
	}

	base2, err := parseSubnetBase("192.168.1.0/16")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if base2 != "192.168.1." {
		t.Errorf("base = %q, want 192.168.1.", base2)
	}
}

// --- Provisioner tests ---

func TestParse_ProvisionerInline(t *testing.T) {
	src := []byte(`
vm {
  image = "test:latest"

  provisioner "shell" {
    inline = ["apt-get update", "apt-get install -y nginx"]
  }
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.VM.Provisioners) != 1 {
		t.Fatalf("provisioners len = %d, want 1", len(cfg.VM.Provisioners))
	}
	p := cfg.VM.Provisioners[0]
	if p.Type != "shell" {
		t.Errorf("type = %q, want shell", p.Type)
	}
	if len(p.Inline) != 2 {
		t.Errorf("inline len = %d, want 2", len(p.Inline))
	}
}

func TestParse_ProvisionerScript(t *testing.T) {
	src := []byte(`
vm {
  image = "test:latest"

  provisioner "shell" {
    script  = "./setup.sh"
    timeout = "10m"
  }
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p := cfg.VM.Provisioners[0]
	if p.Script != "./setup.sh" {
		t.Errorf("script = %q, want ./setup.sh", p.Script)
	}
	if p.Timeout != "10m" {
		t.Errorf("timeout = %q, want 10m", p.Timeout)
	}
}

func TestParse_ProvisionerEnv(t *testing.T) {
	src := []byte(`
vm {
  image = "test:latest"

  provisioner "shell" {
    inline = ["echo $FOO"]
    env = {
      FOO = "bar"
      BAZ = "qux"
    }
  }
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p := cfg.VM.Provisioners[0]
	if len(p.Env) != 2 {
		t.Fatalf("env len = %d, want 2", len(p.Env))
	}
	if p.Env["FOO"] != "bar" {
		t.Errorf("env[FOO] = %q, want bar", p.Env["FOO"])
	}
}

func TestParse_ProvisionerBothScriptAndInline(t *testing.T) {
	src := []byte(`
vm {
  image = "test:latest"

  provisioner "shell" {
    script = "./setup.sh"
    inline = ["echo hello"]
  }
}
`)
	_, err := Parse(src, "test.hcl")
	if err == nil {
		t.Fatal("expected error for both script and inline")
	}
	if !strings.Contains(err.Error(), "cannot specify both") {
		t.Errorf("error = %q, should mention cannot specify both", err)
	}
}

func TestParse_ProvisionerNeitherScriptNorInline(t *testing.T) {
	src := []byte(`
vm {
  image = "test:latest"

  provisioner "shell" {
  }
}
`)
	_, err := Parse(src, "test.hcl")
	if err == nil {
		t.Fatal("expected error for neither script nor inline")
	}
	if !strings.Contains(err.Error(), "must specify either") {
		t.Errorf("error = %q, should mention must specify either", err)
	}
}

func TestParse_ProvisionerUnsupportedType(t *testing.T) {
	src := []byte(`
vm {
  image = "test:latest"

  provisioner "ansible" {
    inline = ["echo hello"]
  }
}
`)
	_, err := Parse(src, "test.hcl")
	if err == nil {
		t.Fatal("expected error for unsupported provisioner type")
	}
	if !strings.Contains(err.Error(), "unsupported provisioner type") {
		t.Errorf("error = %q, should mention unsupported type", err)
	}
}

func TestParse_ProvisionerInvalidTimeout(t *testing.T) {
	src := []byte(`
vm {
  image = "test:latest"

  provisioner "shell" {
    inline  = ["echo hello"]
    timeout = "not-a-duration"
  }
}
`)
	_, err := Parse(src, "test.hcl")
	if err == nil {
		t.Fatal("expected error for invalid timeout")
	}
	if !strings.Contains(err.Error(), "invalid timeout") {
		t.Errorf("error = %q, should mention invalid timeout", err)
	}
}

func TestParse_TopLevelProvisioners(t *testing.T) {
	src := []byte(`
provisioner "shell" {
  inline = ["echo common"]
}

defaults {
  image = "test:latest"
}

node "a" {
  provisioner "shell" {
    inline = ["echo a-specific"]
  }
}

node "b" {
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Top-level provisioner should be parsed.
	if len(cfg.Provisioners) != 1 {
		t.Fatalf("top-level provisioners len = %d, want 1", len(cfg.Provisioners))
	}

	nodes := cfg.ResolveNodes()

	// Node "a" should have top-level + node-level = 2 provisioners.
	if len(nodes[0].Provisioners) != 2 {
		t.Fatalf("node a provisioners len = %d, want 2", len(nodes[0].Provisioners))
	}
	if nodes[0].Provisioners[0].Inline[0] != "echo common" {
		t.Errorf("node a first provisioner should be top-level")
	}
	if nodes[0].Provisioners[1].Inline[0] != "echo a-specific" {
		t.Errorf("node a second provisioner should be node-level")
	}

	// Node "b" should inherit only the top-level provisioner.
	if len(nodes[1].Provisioners) != 1 {
		t.Fatalf("node b provisioners len = %d, want 1", len(nodes[1].Provisioners))
	}
	if nodes[1].Provisioners[0].Inline[0] != "echo common" {
		t.Errorf("node b should inherit top-level provisioner")
	}
}

func TestParse_MultipleProvisionersOrdered(t *testing.T) {
	src := []byte(`
vm {
  image = "test:latest"

  provisioner "shell" {
    inline = ["echo first"]
  }

  provisioner "shell" {
    inline = ["echo second"]
  }

  provisioner "shell" {
    script = "./third.sh"
  }
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.VM.Provisioners) != 3 {
		t.Fatalf("provisioners len = %d, want 3", len(cfg.VM.Provisioners))
	}

	nodes := cfg.ResolveNodes()
	if len(nodes[0].Provisioners) != 3 {
		t.Fatalf("resolved provisioners len = %d, want 3", len(nodes[0].Provisioners))
	}
	if nodes[0].Provisioners[0].Inline[0] != "echo first" {
		t.Error("provisioners should maintain declaration order")
	}
	if nodes[0].Provisioners[2].Script != "./third.sh" {
		t.Error("third provisioner should be the script")
	}
}

func TestParse_NodeProvisionerValidation(t *testing.T) {
	src := []byte(`
defaults {
  image = "test:latest"
}

node "bad" {
  provisioner "shell" {
    script = "./a.sh"
    inline = ["echo oops"]
  }
}
`)
	_, err := Parse(src, "test.hcl")
	if err == nil {
		t.Fatal("expected error for node provisioner with both script and inline")
	}
	if !strings.Contains(err.Error(), "node \"bad\"") {
		t.Errorf("error = %q, should mention node name", err)
	}
}

// --- User tests ---

func TestParse_UserWithSSHKey(t *testing.T) {
	src := []byte(`
vm {
  image = "test:latest"
}

user {
  name    = "deploy"
  ssh_key = "ssh-ed25519 AAAAC3... deploy@host"
  groups  = ["sudo", "docker"]
  shell   = "/bin/bash"
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.User == nil {
		t.Fatal("user should not be nil")
	}
	if cfg.User.Name != "deploy" {
		t.Errorf("user.name = %q, want deploy", cfg.User.Name)
	}
	if cfg.User.SSHKey == "" {
		t.Error("user.ssh_key should not be empty")
	}
	if len(cfg.User.Groups) != 2 {
		t.Errorf("user.groups len = %d, want 2", len(cfg.User.Groups))
	}

	nodes := cfg.ResolveNodes()
	if nodes[0].User == nil {
		t.Fatal("resolved node should have user")
	}
	if nodes[0].User.Name != "deploy" {
		t.Errorf("resolved user.name = %q, want deploy", nodes[0].User.Name)
	}
}

func TestParse_UserWithPasswordHash(t *testing.T) {
	src := []byte(`
vm {
  image = "test:latest"
}

user {
  name          = "admin"
  password_hash = "$6$rounds=4096$salt$hashedvalue"
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.User.PasswordHash == "" {
		t.Error("password_hash should be set")
	}
}

func TestParse_UserNoAuth(t *testing.T) {
	src := []byte(`
vm {
  image = "test:latest"
}

user {
  name = "noauth"
}
`)
	_, err := Parse(src, "test.hcl")
	if err == nil {
		t.Fatal("expected error for user with no auth method")
	}
	if !strings.Contains(err.Error(), "ssh_key, password, or password_hash") {
		t.Errorf("error = %q, should mention auth methods", err)
	}
}

func TestParse_UserPlaintextPassword(t *testing.T) {
	src := []byte(`
vm {
  image = "test:latest"
}

user {
  name          = "baduser"
  password_hash = "plaintext-password"
}
`)
	_, err := Parse(src, "test.hcl")
	if err == nil {
		t.Fatal("expected error for plaintext password")
	}
	if !strings.Contains(err.Error(), "crypt(3)") {
		t.Errorf("error = %q, should mention crypt(3)", err)
	}
}

func TestParse_UserReservedName(t *testing.T) {
	for _, name := range []string{"nova", "root"} {
		src := []byte(`
vm {
  image = "test:latest"
}

user {
  name    = "` + name + `"
  ssh_key = "ssh-ed25519 AAAAC3..."
}
`)
		_, err := Parse(src, "test.hcl")
		if err == nil {
			t.Fatalf("expected error for reserved user name %q", name)
		}
	}
}

func TestParse_UserInVM(t *testing.T) {
	src := []byte(`
vm {
  image = "test:latest"

  user {
    name    = "vmuser"
    ssh_key = "ssh-ed25519 AAAAC3..."
  }
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.VM.User == nil {
		t.Fatal("vm.user should not be nil")
	}

	nodes := cfg.ResolveNodes()
	if nodes[0].User.Name != "vmuser" {
		t.Errorf("resolved user = %q, want vmuser", nodes[0].User.Name)
	}
}

func TestParse_UserInheritance_NodeOverridesTopLevel(t *testing.T) {
	src := []byte(`
user {
  name    = "ops"
  ssh_key = "ssh-ed25519 OPS..."
}

defaults {
  image = "test:latest"
}

node "a" {
  user {
    name    = "custom"
    ssh_key = "ssh-ed25519 CUSTOM..."
  }
}

node "b" {
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	nodes := cfg.ResolveNodes()

	// Node "a" should have its own user (overrides top-level).
	if nodes[0].User == nil || nodes[0].User.Name != "custom" {
		t.Errorf("node a user = %v, want custom", nodes[0].User)
	}

	// Node "b" should inherit top-level user.
	if nodes[1].User == nil || nodes[1].User.Name != "ops" {
		t.Errorf("node b user = %v, want ops (inherited)", nodes[1].User)
	}
}

func TestParse_UserNodeValidation(t *testing.T) {
	src := []byte(`
defaults {
  image = "test:latest"
}

node "bad" {
  user {
    name = "noauth"
  }
}
`)
	_, err := Parse(src, "test.hcl")
	if err == nil {
		t.Fatal("expected error for node user with no auth")
	}
	if !strings.Contains(err.Error(), "node \"bad\"") {
		t.Errorf("error = %q, should mention node name", err)
	}
}

func TestParse_NoUser(t *testing.T) {
	src := []byte(`
vm {
  image = "test:latest"
}
`)
	cfg, err := Parse(src, "test.hcl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	nodes := cfg.ResolveNodes()
	if nodes[0].User != nil {
		t.Error("user should be nil when not configured")
	}
}

func TestLooksLikeCryptHash(t *testing.T) {
	valid := []string{
		"$6$rounds=4096$salt$hash",
		"$y$j9T$salt$hash",
		"$5$salt$hash",
	}
	for _, h := range valid {
		if !looksLikeCryptHash(h) {
			t.Errorf("looksLikeCryptHash(%q) should be true", h)
		}
	}

	invalid := []string{
		"plaintext",
		"$onlyone",
		"$two$sections",
		"",
		"password123",
	}
	for _, h := range invalid {
		if looksLikeCryptHash(h) {
			t.Errorf("looksLikeCryptHash(%q) should be false", h)
		}
	}
}
