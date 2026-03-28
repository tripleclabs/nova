// Package config parses and validates nova.hcl configuration files.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/zclconf/go-cty/cty"
)

// Config is the top-level representation of a nova.hcl file.
type Config struct {
	Variables []Variable `hcl:"variable,block"`
	VM        VM         `hcl:"vm,block"`
}

// Variable defines a user-settable variable with an optional default.
type Variable struct {
	Name    string         `hcl:"name,label"`
	Default *hcl.Attribute `hcl:"default,optional"`
}

// VM describes a single virtual machine.
type VM struct {
	Name          string         `hcl:"name,optional"`
	Image         string         `hcl:"image"`
	CPUs          int            `hcl:"cpus,optional"`
	Memory        string         `hcl:"memory,optional"`
	Arch          string         `hcl:"arch,optional"`
	PortForwards  []PortForward  `hcl:"port_forward,block"`
	SharedFolders []SharedFolder `hcl:"shared_folder,block"`
}

// PortForward maps a host port to a guest port.
type PortForward struct {
	Host     int    `hcl:"host"`
	Guest    int    `hcl:"guest"`
	Protocol string `hcl:"protocol,optional"`
}

// SharedFolder mounts a host directory into the guest.
type SharedFolder struct {
	HostPath  string `hcl:"host_path"`
	GuestPath string `hcl:"guest_path"`
	ReadOnly  bool   `hcl:"read_only,optional"`
}

// Load parses an HCL file at the given path, resolves variables, and returns a validated Config.
func Load(path string) (*Config, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	return Parse(src, path)
}

// Parse parses HCL source bytes into a validated Config.
func Parse(src []byte, filename string) (*Config, error) {
	parser := hclparse.NewParser()
	file, diags := parser.ParseHCL(src, filename)
	if diags.HasErrors() {
		return nil, fmt.Errorf("parsing HCL: %s", diags.Error())
	}

	// First pass: extract variables to build the evaluation context.
	var raw struct {
		Variables []Variable `hcl:"variable,block"`
		Remain    hcl.Body   `hcl:",remain"`
	}
	diags = gohcl.DecodeBody(file.Body, nil, &raw)
	if diags.HasErrors() {
		return nil, fmt.Errorf("decoding variables: %s", diags.Error())
	}

	ctx := buildEvalContext(raw.Variables)

	// Second pass: decode the full config with the variable context.
	var cfg Config
	diags = gohcl.DecodeBody(file.Body, ctx, &cfg)
	if diags.HasErrors() {
		return nil, fmt.Errorf("decoding config: %s", diags.Error())
	}

	if err := applyDefaults(&cfg); err != nil {
		return nil, err
	}
	if err := validate(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// buildEvalContext creates an HCL evaluation context from variable definitions.
func buildEvalContext(vars []Variable) *hcl.EvalContext {
	vals := make(map[string]cty.Value)
	for _, v := range vars {
		if v.Default != nil {
			val, diags := v.Default.Expr.Value(nil)
			if !diags.HasErrors() {
				vals[v.Name] = val
			}
		}
	}
	return &hcl.EvalContext{
		Variables: map[string]cty.Value{
			"var": cty.ObjectVal(vals),
		},
	}
}

func applyDefaults(cfg *Config) error {
	if cfg.VM.CPUs == 0 {
		cfg.VM.CPUs = 2
	}
	if cfg.VM.Memory == "" {
		cfg.VM.Memory = "2G"
	}
	if cfg.VM.Arch == "" {
		cfg.VM.Arch = "host"
	}
	for i := range cfg.VM.PortForwards {
		if cfg.VM.PortForwards[i].Protocol == "" {
			cfg.VM.PortForwards[i].Protocol = "tcp"
		}
	}
	return nil
}

func validate(cfg *Config) error {
	if cfg.VM.Image == "" {
		return fmt.Errorf("vm.image is required")
	}

	// CPU bounds.
	if cfg.VM.CPUs < 1 || cfg.VM.CPUs > 128 {
		return fmt.Errorf("vm.cpus must be between 1 and 128, got %d", cfg.VM.CPUs)
	}

	// Memory format.
	if err := validateMemory(cfg.VM.Memory); err != nil {
		return err
	}

	// Port collision detection.
	seen := make(map[int]bool)
	for _, pf := range cfg.VM.PortForwards {
		if pf.Host < 1 || pf.Host > 65535 {
			return fmt.Errorf("port_forward.host must be 1-65535, got %d", pf.Host)
		}
		if pf.Guest < 1 || pf.Guest > 65535 {
			return fmt.Errorf("port_forward.guest must be 1-65535, got %d", pf.Guest)
		}
		if seen[pf.Host] {
			return fmt.Errorf("duplicate host port: %d", pf.Host)
		}
		seen[pf.Host] = true
	}

	return nil
}

func validateMemory(mem string) error {
	if mem == "" {
		return fmt.Errorf("vm.memory is required")
	}
	mem = strings.TrimSpace(mem)
	if len(mem) < 2 {
		return fmt.Errorf("vm.memory must be a value like '2G' or '512M', got %q", mem)
	}
	suffix := strings.ToUpper(mem[len(mem)-1:])
	if suffix != "M" && suffix != "G" {
		return fmt.Errorf("vm.memory must end with M or G, got %q", mem)
	}
	numStr := mem[:len(mem)-1]
	for _, c := range numStr {
		if c < '0' || c > '9' {
			return fmt.Errorf("vm.memory must be a numeric value with M or G suffix, got %q", mem)
		}
	}
	return nil
}
