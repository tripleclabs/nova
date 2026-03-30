package cloudinit

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/tripleclabs/nova/internal/distro"
	"golang.org/x/crypto/bcrypt"
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
	Password     string // plain text — hashed by Generate; enables console login
	PasswordHash string // pre-hashed (sha512crypt $6$ or bcrypt $2b$)
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
	StaticIP      string        // Static IP for multi-node guest NIC (e.g., "10.0.0.2").
	Subnet        string        // CIDR subnet for multi-node (e.g., "10.0.0.0/24").
	MACAddress    string        // MAC address of the primary NIC (NAT/DHCP).
	SwitchMAC     string        // MAC address of the switched NIC (static IP, inter-VM).
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
		if cfg.ExtraUser.Password != "" {
			hash, err := bcrypt.GenerateFromPassword([]byte(cfg.ExtraUser.Password), bcrypt.DefaultCost)
			if err != nil {
				return nil, fmt.Errorf("hashing password for user %q: %w", cfg.ExtraUser.Name, err)
			}
			extraUser["passwd"] = string(hash)
			extraUser["lock_passwd"] = false
		} else if cfg.ExtraUser.PasswordHash != "" {
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

	// Always write /etc/resolv.conf — cloud-init network-config nameservers
	// are not reliable across distros (Alpine ignores them entirely).
	writeFiles = append(writeFiles, map[string]any{
		"path":    "/etc/resolv.conf",
		"content": "nameserver 8.8.8.8\nnameserver 8.8.4.4\n",
	})

	// Write an sshd drop-in that enables agent forwarding and cleans up stale
	// agent sockets on reconnect. Placed in sshd_config.d/ for distros that
	// support Include (Ubuntu 20.04+, Debian 10+, Alpine 3.15+); the runcmd
	// below also injects the setting directly into sshd_config for older images.
	writeFiles = append(writeFiles, map[string]any{
		"path":        "/etc/ssh/sshd_config.d/50-nova.conf",
		"permissions": "0600",
		"content":     "AllowAgentForwarding yes\nStreamLocalBindUnlink yes\n",
	})

	// Write a global SSH client config so outbound SSH connections (e.g. git clone)
	// accept new host keys on first use without prompting. StrictHostKeyChecking
	// accept-new still rejects changed keys, so MITM protection is preserved.
	writeFiles = append(writeFiles, map[string]any{
		"path":    "/etc/ssh/ssh_config.d/50-nova.conf",
		"content": "Host *\n    StrictHostKeyChecking accept-new\n    ForwardAgent yes\n",
	})

	if profile.DoasConf != "" {
		writeFiles = append(writeFiles, map[string]any{
			"path":        "/etc/doas.d/nova.conf",
			"content":     profile.DoasConf,
			"permissions": "0400",
		})
		if cfg.ExtraUser != nil {
			writeFiles = append(writeFiles, map[string]any{
				"path":        "/etc/doas.d/" + cfg.ExtraUser.Name + ".conf",
				"content":     "permit nopass " + cfg.ExtraUser.Name + "\n",
				"permissions": "0400",
			})
		}
	}

	// Ensure AllowAgentForwarding is active even on distros without sshd_config.d Include.
	// Creates the drop-in directory, patches sshd_config directly as a fallback,
	// and reloads sshd so the setting takes effect on first boot.
	sshdCmds := []any{
		"mkdir -p /etc/ssh/sshd_config.d",
		"grep -qxF 'Include /etc/ssh/sshd_config.d/*.conf' /etc/ssh/sshd_config || echo 'Include /etc/ssh/sshd_config.d/*.conf' >> /etc/ssh/sshd_config",
		"systemctl reload sshd 2>/dev/null || rc-service sshd reload 2>/dev/null || true",
	}
	if existing, ok := defaults["runcmd"].([]any); ok {
		defaults["runcmd"] = append(existing, sshdCmds...)
	} else {
		defaults["runcmd"] = sshdCmds
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
			opts := "rw,relatime,nofail"
			if fsType == "9p" {
				opts = "trans=virtio,version=9p2000.L,rw,relatime,cache=loose,msize=1048576,nofail"
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

// GenerateNetworkConfig produces a cloud-init network-config v2 YAML document.
// For multi-node (StaticIP set): assigns the static IP on the MAC-matched NIC.
// For single-VM: enables DHCP on the MAC-matched NIC (ensures the NIC is
// configured regardless of interface naming).
// Returns nil only if no MACAddress is set.
func GenerateNetworkConfig(cfg GeneratorConfig) []byte {
	if cfg.MACAddress == "" {
		return nil
	}

	// Dual-NIC: NAT NIC (DHCP) + switched NIC (static IP).
	// Used on macOS multi-node where VMs have two NICs — one for host/internet,
	// one for inter-VM communication via the L2 switch.
	if cfg.SwitchMAC != "" && cfg.StaticIP != "" {
		prefixLen := "24"
		if cfg.Subnet != "" {
			if parts := strings.SplitN(cfg.Subnet, "/", 2); len(parts) == 2 {
				prefixLen = parts[1]
			}
		}
		config := fmt.Sprintf(`version: 2
ethernets:
  nova-nat:
    match:
      macaddress: "%s"
    dhcp4: true
    dhcp-identifier: mac
  nova-switch:
    match:
      macaddress: "%s"
    addresses:
      - %s/%s
    dhcp4: false
    nameservers:
      addresses:
        - 8.8.8.8
        - 8.8.4.4
`, strings.ToLower(cfg.MACAddress), strings.ToLower(cfg.SwitchMAC), cfg.StaticIP, prefixLen)
		return []byte(config)
	}

	if cfg.StaticIP != "" {
		// Single-NIC static IP (Linux with L2 switch or mcast).
		prefixLen := "24"
		if cfg.Subnet != "" {
			if parts := strings.SplitN(cfg.Subnet, "/", 2); len(parts) == 2 {
				prefixLen = parts[1]
			}
		}
		// Derive gateway as the first host in the subnet (e.g. 10.0.0.1 for 10.0.0.0/24).
		gateway := ""
		if cfg.Subnet != "" {
			if _, ipNet, err := net.ParseCIDR(cfg.Subnet); err == nil {
				gw := make(net.IP, len(ipNet.IP))
				copy(gw, ipNet.IP)
				gw[len(gw)-1]++
				gateway = gw.String()
			}
		}
		gwLine := ""
		if gateway != "" {
			gwLine = fmt.Sprintf("    gateway4: %s\n", gateway)
		}
		config := fmt.Sprintf(`version: 2
ethernets:
  nova0:
    match:
      macaddress: "%s"
    addresses:
      - %s/%s
    dhcp4: false
%s    nameservers:
      addresses:
        - 8.8.8.8
        - 8.8.4.4
`, strings.ToLower(cfg.MACAddress), cfg.StaticIP, prefixLen, gwLine)
		return []byte(config)
	}

	// Single-VM: DHCP on our NIC, matched by MAC.
	// dhcp-identifier: mac ensures the DHCP client sends the hardware MAC
	// (not a DUID), so macOS bootpd stores it and we can look up the IP.
	config := fmt.Sprintf(`version: 2
ethernets:
  nova0:
    match:
      macaddress: "%s"
    dhcp4: true
    dhcp-identifier: mac
    nameservers:
      addresses:
        - 8.8.8.8
        - 8.8.4.4
`, strings.ToLower(cfg.MACAddress))
	return []byte(config)
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
