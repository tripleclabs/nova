package cloudinit

import (
	"fmt"
	"os"

	"github.com/3clabs/nova/internal/distro"
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

// UserConfig describes a non-Nova user to create via cloud-init.
type UserConfig struct {
	Name         string
	SSHKey       string
	PasswordHash string
	Groups       []string
	Shell        string
}

// GeneratorConfig holds the inputs for generating a merged cloud-config.
type GeneratorConfig struct {
	Hostname      string
	AuthorizedKey string        // SSH public key in authorized_keys format.
	UserDataPath  string        // Path to user-provided cloud-config.yaml (optional).
	Mounts        []SharedMount // VirtioFS/9p mounts to inject.
	Hosts         []HostEntry   // /etc/hosts entries for multi-node clusters.
	Rosetta       bool          // Enable Rosetta 2 binfmt_misc registration in the guest.
	OS            string        // OS identifier (e.g. "ubuntu", "alpine"); empty = generic defaults.
	ExtraUser     *UserConfig   // Optional non-Nova user to create alongside the internal nova user.
}

// Generate merges Nova's required defaults with a user-provided cloud-config.
// Nova defaults: set hostname, inject SSH key, disable password auth.
// User-provided lists (packages, runcmd, write_files) are preserved and merged.
func Generate(cfg GeneratorConfig) ([]byte, error) {
	profile := profileForOS(cfg.OS)

	novaUser := map[string]any{
		"name":                "nova",
		"shell":               profile.Shell,
		"passwd":              "*",
		"lock_passwd":         false,
		"ssh_authorized_keys": []any{cfg.AuthorizedKey},
	}
	if profile.SudoLine != "" {
		novaUser["sudo"] = profile.SudoLine
	}

	users := []any{
		novaUser,
		map[string]any{
			"name":                "root",
			"ssh_authorized_keys": []any{cfg.AuthorizedKey},
		},
	}

	// Add configured extra user if present.
	if cfg.ExtraUser != nil {
		extraUser := map[string]any{
			"name":        cfg.ExtraUser.Name,
			"lock_passwd": true,
		}
		shell := cfg.ExtraUser.Shell
		if shell == "" {
			shell = profile.Shell
		}
		extraUser["shell"] = shell

		if cfg.ExtraUser.SSHKey != "" {
			extraUser["ssh_authorized_keys"] = []any{cfg.ExtraUser.SSHKey}
		}
		if cfg.ExtraUser.PasswordHash != "" {
			extraUser["passwd"] = cfg.ExtraUser.PasswordHash
			extraUser["lock_passwd"] = false
		}
		if len(cfg.ExtraUser.Groups) > 0 {
			extraUser["groups"] = cfg.ExtraUser.Groups
		}
		if profile.SudoLine != "" {
			extraUser["sudo"] = profile.SudoLine
		}
		users = append(users, extraUser)
	}

	defaults := CloudConfig{
		"hostname":         cfg.Hostname,
		"manage_etc_hosts": true,
		"ssh_pwauth":       false,
		"disable_root":     false,
		"users":            users,
	}

	// Accumulate write_files entries.
	var writeFiles []any
	if profile.DoasConf != "" {
		writeFiles = append(writeFiles, map[string]any{
			"path":        "/etc/doas.d/nova.conf",
			"content":     profile.DoasConf,
			"permissions": "0400",
		})
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
		var hostsContent string
		for _, h := range cfg.Hosts {
			hostsContent += fmt.Sprintf("%s %s\n", h.IP, h.Hostname)
		}
		writeFiles = append(writeFiles, map[string]any{
			"path":    "/etc/hosts",
			"append":  true,
			"content": hostsContent,
		})
	}

	if len(writeFiles) > 0 {
		defaults["write_files"] = writeFiles
	}

	// Rosetta 2 guest-side setup: mount the Rosetta VirtioFS share and
	// register it with binfmt_misc so x86_64 binaries run transparently.
	if cfg.Rosetta {
		rosettaCmds := []any{
			"mkdir -p /media/rosetta",
			"mount -t virtiofs rosetta /media/rosetta",
			"/usr/sbin/update-binfmts --install rosetta /media/rosetta/rosetta --magic '\\x7fELF\\x02\\x01\\x01\\x00\\x00\\x00\\x00\\x00\\x00\\x00\\x00\\x00\\x02\\x00\\x3e\\x00' --mask '\\xff\\xff\\xff\\xff\\xff\\xfe\\xfe\\x00\\xff\\xff\\xff\\xff\\xff\\xff\\xff\\xff\\xfe\\xff\\xff\\xff' --credentials yes --preserve no --fix-binary yes",
		}
		if existing, ok := defaults["runcmd"].([]any); ok {
			defaults["runcmd"] = append(existing, rosettaCmds...)
		} else {
			defaults["runcmd"] = rosettaCmds
		}
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

// profileForOS returns the distro profile for the given OS key or family name.
// Falls back to generic Ubuntu-compatible defaults for empty or unknown values.
func profileForOS(os string) distro.Profile {
	if os == "" {
		return distro.ProfileFor("") // returns generic defaults
	}
	return distro.ProfileFor(os)
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
