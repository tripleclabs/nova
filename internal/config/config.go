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
// It supports both single-VM mode (a single "vm" block) and multi-node mode
// (one or more "node" blocks). If both are present, parsing returns an error.
type Config struct {
	Variables []Variable `hcl:"variable,block"`
	VM        *VM        `hcl:"vm,block"`
	Defaults  *Defaults  `hcl:"defaults,block"`
	Nodes     []Node     `hcl:"node,block"`
	Network   *Network   `hcl:"network,block"`
	Links     []Link     `hcl:"link,block"`
}

// Link defines declarative network conditions between two nodes.
type Link struct {
	NodeA   string `hcl:"node_a,label"`
	NodeB   string `hcl:"node_b,label"`
	Latency string `hcl:"latency,optional"` // e.g. "50ms"
	Jitter  string `hcl:"jitter,optional"`  // e.g. "10ms"
	Loss    string `hcl:"loss,optional"`    // e.g. "5%"
	Down    bool   `hcl:"down,optional"`    // Hard partition.
}

// Variable defines a user-settable variable with an optional default.
type Variable struct {
	Name    string         `hcl:"name,label"`
	Default *hcl.Attribute `hcl:"default,optional"`
}

// Defaults holds global default values that nodes inherit.
type Defaults struct {
	Image  string `hcl:"image,optional"`
	CPUs   int    `hcl:"cpus,optional"`
	Memory string `hcl:"memory,optional"`
	Arch   string `hcl:"arch,optional"`
}

// VM describes a single virtual machine (single-VM mode).
type VM struct {
	Name          string         `hcl:"name,optional"`
	Image         string         `hcl:"image"`
	CPUs          int            `hcl:"cpus,optional"`
	Memory        string         `hcl:"memory,optional"`
	Arch          string         `hcl:"arch,optional"`
	PortForwards  []PortForward  `hcl:"port_forward,block"`
	SharedFolders []SharedFolder `hcl:"shared_folder,block"`
}

// Node describes a single node in a multi-node configuration.
type Node struct {
	Name          string         `hcl:"name,label"`
	Image         string         `hcl:"image,optional"`
	CPUs          int            `hcl:"cpus,optional"`
	Memory        string         `hcl:"memory,optional"`
	Arch          string         `hcl:"arch,optional"`
	IP            string         `hcl:"ip,optional"`
	PortForwards  []PortForward  `hcl:"port_forward,block"`
	SharedFolders []SharedFolder `hcl:"shared_folder,block"`
}

// Network configures the virtual network for multi-node clusters.
type Network struct {
	Subnet string `hcl:"subnet,optional"` // e.g. "10.0.0.0/24"
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

// ResolvedNode is a fully-resolved node with all defaults applied.
// Used by the orchestrator so it doesn't need to know about inheritance.
type ResolvedNode struct {
	Name          string
	Image         string
	CPUs          int
	Memory        string
	Arch          string
	IP            string
	PortForwards  []PortForward
	SharedFolders []SharedFolder
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

	// Cannot mix single-VM and multi-node modes.
	if cfg.VM != nil && len(cfg.Nodes) > 0 {
		return nil, fmt.Errorf("cannot use both 'vm' and 'node' blocks in the same file")
	}

	if cfg.VM != nil {
		applyVMDefaults(cfg.VM)
		if err := validateVM(cfg.VM); err != nil {
			return nil, err
		}
	}

	if len(cfg.Nodes) > 0 {
		if err := applyAndValidateNodes(&cfg); err != nil {
			return nil, err
		}
	}

	// Must have at least one VM or node.
	if cfg.VM == nil && len(cfg.Nodes) == 0 {
		return nil, fmt.Errorf("config must contain a 'vm' block or one or more 'node' blocks")
	}

	return &cfg, nil
}

// ResolveNodes returns the list of fully-resolved nodes from the config.
// For single-VM configs, it returns a single node derived from the VM block.
func (c *Config) ResolveNodes() []ResolvedNode {
	if c.VM != nil {
		name := c.VM.Name
		if name == "" {
			name = "default"
		}
		return []ResolvedNode{{
			Name:          name,
			Image:         c.VM.Image,
			CPUs:          c.VM.CPUs,
			Memory:        c.VM.Memory,
			Arch:          c.VM.Arch,
			PortForwards:  c.VM.PortForwards,
			SharedFolders: c.VM.SharedFolders,
		}}
	}

	nodes := make([]ResolvedNode, len(c.Nodes))
	for i, n := range c.Nodes {
		nodes[i] = ResolvedNode{
			Name:          n.Name,
			Image:         n.Image,
			CPUs:          n.CPUs,
			Memory:        n.Memory,
			Arch:          n.Arch,
			IP:            n.IP,
			PortForwards:  n.PortForwards,
			SharedFolders: n.SharedFolders,
		}
	}
	return nodes
}

// Subnet returns the configured cluster subnet, or a default.
func (c *Config) Subnet() string {
	if c.Network != nil && c.Network.Subnet != "" {
		return c.Network.Subnet
	}
	return "10.0.0.0/24"
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
	if len(vals) == 0 {
		return nil
	}
	return &hcl.EvalContext{
		Variables: map[string]cty.Value{
			"var": cty.ObjectVal(vals),
		},
	}
}

func applyVMDefaults(vm *VM) {
	if vm.CPUs == 0 {
		vm.CPUs = 2
	}
	if vm.Memory == "" {
		vm.Memory = "2G"
	}
	if vm.Arch == "" {
		vm.Arch = "host"
	}
	for i := range vm.PortForwards {
		if vm.PortForwards[i].Protocol == "" {
			vm.PortForwards[i].Protocol = "tcp"
		}
	}
}

func applyAndValidateNodes(cfg *Config) error {
	// Build defaults.
	def := Defaults{Image: "", CPUs: 2, Memory: "2G", Arch: "host"}
	if cfg.Defaults != nil {
		if cfg.Defaults.Image != "" {
			def.Image = cfg.Defaults.Image
		}
		if cfg.Defaults.CPUs != 0 {
			def.CPUs = cfg.Defaults.CPUs
		}
		if cfg.Defaults.Memory != "" {
			def.Memory = cfg.Defaults.Memory
		}
		if cfg.Defaults.Arch != "" {
			def.Arch = cfg.Defaults.Arch
		}
	}

	names := make(map[string]bool)
	allHostPorts := make(map[int]string) // port -> node name

	subnet := cfg.Subnet()
	baseIP, err := parseSubnetBase(subnet)
	if err != nil {
		return err
	}
	usedIPs := make(map[string]bool)
	nextHost := byte(2) // .1 reserved for gateway

	for i := range cfg.Nodes {
		n := &cfg.Nodes[i]

		// Unique name check.
		if names[n.Name] {
			return fmt.Errorf("duplicate node name: %q", n.Name)
		}
		names[n.Name] = true

		// Inherit defaults.
		if n.Image == "" {
			n.Image = def.Image
		}
		if n.CPUs == 0 {
			n.CPUs = def.CPUs
		}
		if n.Memory == "" {
			n.Memory = def.Memory
		}
		if n.Arch == "" {
			n.Arch = def.Arch
		}

		// Auto-assign IP if not specified.
		if n.IP == "" {
			n.IP = fmt.Sprintf("%s%d", baseIP, nextHost)
			nextHost++
		}
		if usedIPs[n.IP] {
			return fmt.Errorf("duplicate IP address: %s (node %q)", n.IP, n.Name)
		}
		usedIPs[n.IP] = true

		// Apply port forward defaults and check cross-node collisions.
		for j := range n.PortForwards {
			if n.PortForwards[j].Protocol == "" {
				n.PortForwards[j].Protocol = "tcp"
			}
			hp := n.PortForwards[j].Host
			if other, exists := allHostPorts[hp]; exists {
				return fmt.Errorf("host port %d conflicts between nodes %q and %q", hp, other, n.Name)
			}
			allHostPorts[hp] = n.Name
		}

		// Validate this node as if it were a VM.
		vm := &VM{
			Name:         n.Name,
			Image:        n.Image,
			CPUs:         n.CPUs,
			Memory:       n.Memory,
			Arch:         n.Arch,
			PortForwards: n.PortForwards,
		}
		if err := validateVM(vm); err != nil {
			return fmt.Errorf("node %q: %w", n.Name, err)
		}
	}

	return nil
}

// parseSubnetBase extracts the base IP prefix from a CIDR like "10.0.0.0/24" -> "10.0.0."
func parseSubnetBase(cidr string) (string, error) {
	parts := strings.SplitN(cidr, "/", 2)
	ip := parts[0]
	octets := strings.Split(ip, ".")
	if len(octets) != 4 {
		return "", fmt.Errorf("invalid subnet: %s", cidr)
	}
	return strings.Join(octets[:3], ".") + ".", nil
}

func validateVM(vm *VM) error {
	if vm.Image == "" {
		return fmt.Errorf("image is required")
	}
	if vm.CPUs < 1 || vm.CPUs > 128 {
		return fmt.Errorf("cpus must be between 1 and 128, got %d", vm.CPUs)
	}
	if err := validateMemory(vm.Memory); err != nil {
		return err
	}

	seen := make(map[int]bool)
	for _, pf := range vm.PortForwards {
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
		return fmt.Errorf("memory is required")
	}
	mem = strings.TrimSpace(mem)
	if len(mem) < 2 {
		return fmt.Errorf("memory must be a value like '2G' or '512M', got %q", mem)
	}
	suffix := strings.ToUpper(mem[len(mem)-1:])
	if suffix != "M" && suffix != "G" {
		return fmt.Errorf("memory must end with M or G, got %q", mem)
	}
	numStr := mem[:len(mem)-1]
	for _, c := range numStr {
		if c < '0' || c > '9' {
			return fmt.Errorf("memory must be a numeric value with M or G suffix, got %q", mem)
		}
	}
	return nil
}
