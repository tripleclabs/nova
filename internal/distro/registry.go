// Package distro provides a registry of known Linux distributions with their
// official cloud image download URLs and cloud-init configuration profiles.
package distro

import "runtime"

// Profile describes OS-specific cloud-init behaviour for a distro.
type Profile struct {
	// Shell is the login shell for the nova user (e.g. "/bin/bash", "/bin/sh").
	Shell string
	// SudoLine is written to /etc/sudoers.d/nova when non-empty.
	SudoLine string
	// DoasConf is written to /etc/doas.d/nova.conf when non-empty (Alpine).
	DoasConf string
}

// Spec describes a known distribution: where to download it and how to configure it.
type Spec struct {
	// URLs maps GOARCH values ("amd64", "arm64") to download URLs.
	URLs    map[string]string
	Profile Profile
}

// registry is the built-in catalogue of known distributions.
// Key format: "distro:version" (e.g. "ubuntu:24.04", "alpine:3.21").
var registry = map[string]Spec{
	"ubuntu:24.04": {
		URLs: map[string]string{
			"amd64": "https://cloud-images.ubuntu.com/minimal/releases/noble/release/ubuntu-24.04-minimal-cloudimg-amd64.img",
			"arm64": "https://cloud-images.ubuntu.com/minimal/releases/noble/release/ubuntu-24.04-minimal-cloudimg-arm64.img",
		},
		Profile: Profile{
			Shell:    "/bin/bash",
			SudoLine: "nova ALL=(ALL) NOPASSWD:ALL",
		},
	},
	"ubuntu:22.04": {
		URLs: map[string]string{
			"amd64": "https://cloud-images.ubuntu.com/minimal/releases/jammy/release/ubuntu-22.04-minimal-cloudimg-amd64.img",
			"arm64": "https://cloud-images.ubuntu.com/minimal/releases/jammy/release/ubuntu-22.04-minimal-cloudimg-arm64.img",
		},
		Profile: Profile{
			Shell:    "/bin/bash",
			SudoLine: "nova ALL=(ALL) NOPASSWD:ALL",
		},
	},
	"alpine:3.21": {
		URLs: map[string]string{
			"amd64": "https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/cloud/nocloud_alpine-3.21.0-x86_64-bios-cloudinit-r0.qcow2",
			"arm64": "https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/cloud/nocloud_alpine-3.21.0-aarch64-uefi-cloudinit-r0.qcow2",
		},
		Profile: Profile{
			Shell:    "/bin/sh",
			DoasConf: "permit nopass nova\n",
		},
	},
	"alpine:3.20": {
		URLs: map[string]string{
			"amd64": "https://dl-cdn.alpinelinux.org/alpine/v3.20/releases/cloud/nocloud_alpine-3.20.0-x86_64-bios-cloudinit-r0.qcow2",
			"arm64": "https://dl-cdn.alpinelinux.org/alpine/v3.20/releases/cloud/nocloud_alpine-3.20.0-aarch64-uefi-cloudinit-r0.qcow2",
		},
		Profile: Profile{
			Shell:    "/bin/sh",
			DoasConf: "permit nopass nova\n",
		},
	},
}

// Lookup returns the Spec for a distro key (e.g. "ubuntu:24.04").
// Returns nil, false if unknown.
func Lookup(name string) (*Spec, bool) {
	s, ok := registry[name]
	if !ok {
		return nil, false
	}
	return &s, true
}

// ProfileFor returns the Profile for a distro key or OS family name.
// Accepts both "ubuntu:24.04" (exact) and "ubuntu" (returns the first match
// for that family). Falls back to generic defaults if nothing matches.
func ProfileFor(key string) Profile {
	// Exact match first.
	if s, ok := registry[key]; ok {
		return s.Profile
	}
	// Family match: find any entry whose name starts with "key:".
	prefix := key + ":"
	for k, s := range registry {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			return s.Profile
		}
	}
	// Generic fallback.
	return Profile{
		Shell:    "/bin/bash",
		SudoLine: "nova ALL=(ALL) NOPASSWD:ALL",
	}
}

// DownloadURL returns the download URL for the current host architecture.
func (s *Spec) DownloadURL() (string, bool) {
	url, ok := s.URLs[runtime.GOARCH]
	return url, ok
}

// CanonicalRef returns the nova.local cache reference for a distro shorthand.
// e.g. "ubuntu:24.04" → "nova.local/ubuntu:24.04"
func CanonicalRef(shorthand string) string {
	return "nova.local/" + shorthand
}

// IsShorthand reports whether ref looks like a distro shorthand
// (e.g. "ubuntu:24.04") rather than a full OCI reference.
func IsShorthand(ref string) bool {
	// Shorthands have no slash — no registry host, no org path.
	for _, c := range ref {
		if c == '/' {
			return false
		}
	}
	return true
}
