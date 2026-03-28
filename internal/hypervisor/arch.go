package hypervisor

import "runtime"

// HostArch returns the architecture of the host machine in Nova's canonical form.
func HostArch() string {
	return normalizeArch(runtime.GOARCH)
}

// NeedsEmulation returns true if the guest arch doesn't match the host
// and requires emulation or translation.
func NeedsEmulation(guestArch string) bool {
	guest := normalizeArch(guestArch)
	if guest == "" || guest == "host" {
		return false
	}
	return guest != HostArch()
}

// normalizeArch maps various arch names to a canonical form.
func normalizeArch(arch string) string {
	switch arch {
	case "amd64", "x86_64":
		return "amd64"
	case "arm64", "aarch64":
		return "arm64"
	case "host", "":
		return ""
	default:
		return arch
	}
}
