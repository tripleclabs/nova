package cloudinit

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// CloudConfig represents a cloud-init cloud-config document.
// We use map-based merging to preserve arbitrary user fields.
type CloudConfig map[string]any

// SharedMount describes a VirtioFS/9p mount to inject into the guest.
type SharedMount struct {
	Tag       string // VirtioFS tag or 9p mount tag.
	GuestPath string // Where to mount inside the guest.
	MountType string // Filesystem type: "virtiofs" (default) or "9p".
}

// HostEntry is an /etc/hosts entry for cross-node DNS resolution.
type HostEntry struct {
	IP       string
	Hostname string
}

// GeneratorConfig holds the inputs for generating a merged cloud-config.
type GeneratorConfig struct {
	Hostname      string
	AuthorizedKey string        // SSH public key in authorized_keys format.
	UserDataPath  string        // Path to user-provided cloud-config.yaml (optional).
	Mounts        []SharedMount // VirtioFS/9p mounts to inject.
	Hosts         []HostEntry   // /etc/hosts entries for multi-node clusters.
}

// Generate merges Nova's required defaults with a user-provided cloud-config.
// Nova defaults: set hostname, inject SSH key, disable password auth.
// User-provided lists (packages, runcmd, write_files) are preserved and merged.
func Generate(cfg GeneratorConfig) ([]byte, error) {
	defaults := CloudConfig{
		"hostname":              cfg.Hostname,
		"manage_etc_hosts":      true,
		"ssh_pwauth":            false,
		"disable_root":          true,
		"users": []any{
			map[string]any{
				"name":                "nova",
				"sudo":                "ALL=(ALL) NOPASSWD:ALL",
				"groups":              "sudo",
				"shell":               "/bin/bash",
				"lock_passwd":         true,
				"ssh_authorized_keys": []any{cfg.AuthorizedKey},
			},
		},
	}

	// Inject VirtioFS/9p mount commands.
	if len(cfg.Mounts) > 0 {
		var mounts []any
		var runcmds []any
		for _, m := range cfg.Mounts {
			fsType := m.MountType
			if fsType == "" {
				fsType = "virtiofs"
			}
			opts := "rw,relatime"
			if fsType == "9p" {
				opts = "trans=virtio,version=9p2000.L,rw,relatime"
			}
			// cloud-init mounts format: [device, mountpoint, type, options]
			mounts = append(mounts, []any{m.Tag, m.GuestPath, fsType, opts})
			// Ensure mount point exists before cloud-init tries to mount.
			runcmds = append(runcmds, fmt.Sprintf("mkdir -p %s", m.GuestPath))
		}
		defaults["mounts"] = mounts
		defaults["runcmd"] = runcmds
	}

	// Inject /etc/hosts entries for multi-node clusters.
	if len(cfg.Hosts) > 0 {
		var writeFiles []any
		var hostsContent string
		for _, h := range cfg.Hosts {
			hostsContent += fmt.Sprintf("%s %s\n", h.IP, h.Hostname)
		}
		writeFiles = append(writeFiles, map[string]any{
			"path":    "/etc/hosts",
			"append":  true,
			"content": hostsContent,
		})
		defaults["write_files"] = writeFiles
	}

	// If user provided a cloud-config, merge it.
	if cfg.UserDataPath != "" {
		userCfg, err := loadUserConfig(cfg.UserDataPath)
		if err != nil {
			return nil, err
		}
		defaults = merge(defaults, userCfg)
	}

	out, err := yaml.Marshal(defaults)
	if err != nil {
		return nil, fmt.Errorf("marshaling cloud-config: %w", err)
	}

	return append([]byte("#cloud-config\n"), out...), nil
}

func loadUserConfig(path string) (CloudConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading user cloud-config %s: %w", path, err)
	}

	var cfg CloudConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing user cloud-config: %w", err)
	}
	return cfg, nil
}

// merge combines base and override configs. For list-type keys (packages,
// runcmd, write_files), entries are appended rather than replaced. For map-type
// keys, override wins. Nova's "users" key is always preserved from base.
func merge(base, override CloudConfig) CloudConfig {
	result := make(CloudConfig)

	// Copy base.
	for k, v := range base {
		result[k] = v
	}

	listKeys := map[string]bool{
		"packages":    true,
		"runcmd":      true,
		"write_files": true,
		"mounts":      true,
		"bootcmd":     true,
	}

	for k, ov := range override {
		// Never let user override our users block — they can add users via runcmd.
		if k == "users" {
			continue
		}

		bv, exists := result[k]
		if exists && listKeys[k] {
			result[k] = appendLists(bv, ov)
		} else {
			result[k] = ov
		}
	}

	return result
}

func appendLists(a, b any) any {
	aList, aOk := toSlice(a)
	bList, bOk := toSlice(b)
	if aOk && bOk {
		return append(aList, bList...)
	}
	// If either isn't a list, prefer the override.
	if bOk {
		return b
	}
	return a
}

func toSlice(v any) ([]any, bool) {
	switch val := v.(type) {
	case []any:
		return val, true
	case []string:
		out := make([]any, len(val))
		for i, s := range val {
			out[i] = s
		}
		return out, true
	default:
		return nil, false
	}
}
