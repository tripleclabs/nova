package config

import (
	"strings"
	"testing"
)

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
	if cfg.VM.Memory != "8G" {
		t.Errorf("memory = %q, want 8G", cfg.VM.Memory)
	}
	if len(cfg.VM.PortForwards) != 2 {
		t.Fatalf("port_forwards len = %d, want 2", len(cfg.VM.PortForwards))
	}
	if cfg.VM.PortForwards[0].Host != 8080 || cfg.VM.PortForwards[0].Guest != 80 {
		t.Errorf("port_forward[0] = %+v", cfg.VM.PortForwards[0])
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
	if !strings.Contains(err.Error(), "vm.cpus") {
		t.Errorf("error = %q, should mention vm.cpus", err)
	}
}

func TestParse_InvalidMemory(t *testing.T) {
	cases := []string{"", "2", "2T", "abc"}
	for _, mem := range cases {
		src := []byte(`vm { image = "test:latest" memory = "` + mem + `" }`)
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
