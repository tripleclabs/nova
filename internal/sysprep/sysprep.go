// Package sysprep prepares a VM for export by removing machine-specific state
// so the image boots cleanly in a new environment. It detects the guest OS
// via /etc/os-release and applies the appropriate cleanup profile.
package sysprep

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHConfig holds the connection details for reaching a guest VM.
type SSHConfig struct {
	Host       string
	Port       string
	User       string
	PrivateKey []byte
}

// Options controls sysprep behaviour.
type Options struct {
	// ZeroFreeSpace fills free disk space with zeros for better compression.
	// Slow but produces significantly smaller images.
	ZeroFreeSpace bool
	// RemoveNovaUser removes the internal "nova" user entirely. Should be set
	// when exporting an image that has a configured user block.
	RemoveNovaUser bool
	// TargetHyperV injects Hyper-V storage and network drivers into the
	// initramfs so the image boots correctly on Hyper-V / Azure.
	// Required when exporting to VHDX format.
	TargetHyperV bool
}

// OSFamily represents a detected OS family for profile selection.
type OSFamily string

const (
	OSUbuntu  OSFamily = "ubuntu"
	OSDebian  OSFamily = "debian"
	OSAlpine  OSFamily = "alpine"
	OSFedora  OSFamily = "fedora"
	OSGeneric OSFamily = "generic"
)

// Step represents a single sysprep operation with a description.
type Step struct {
	Name    string
	Command string
}

// Result reports the outcome of a single sysprep step.
type Result struct {
	Step    string
	Status  string // "ok", "skipped", "failed"
	Detail  string
}

// Run detects the guest OS, selects the appropriate cleanup profile, and
// executes all sysprep steps over SSH. Each step runs independently — a
// failing step does not block subsequent steps. Returns all results and
// an error if any critical step failed.
func Run(ctx context.Context, sshCfg SSHConfig, opts Options, output io.Writer) ([]Result, error) {
	client, err := dialSSH(sshCfg)
	if err != nil {
		return nil, fmt.Errorf("SSH connect for sysprep: %w", err)
	}
	defer client.Close()

	// Detect OS family.
	osFamily, err := detectOS(client)
	if err != nil {
		fmt.Fprintf(output, "[sysprep] Warning: could not detect OS (%v), using generic profile\n", err)
		osFamily = OSGeneric
	}
	fmt.Fprintf(output, "[sysprep] Detected OS family: %s\n", osFamily)

	steps := buildProfile(osFamily, opts)

	// Emit Secure Boot guidance for Hyper-V Gen 2 exports so users aren't left
	// debugging "unsigned image's hash is not allowed" errors blind.
	if opts.TargetHyperV {
		switch osFamily {
		case OSAlpine:
			fmt.Fprintf(output, "[sysprep] NOTE: Alpine does not ship a Microsoft-signed shim. "+
				"Secure Boot MUST be disabled in the Hyper-V VM: "+
				"Settings → Security → uncheck Enable Secure Boot.\n")
		default:
			fmt.Fprintf(output, "[sysprep] NOTE: Hyper-V Gen 2 Secure Boot requires the "+
				"\"Microsoft UEFI Certificate Authority\" template (not \"Microsoft Windows\"). "+
				"Set this in VM Settings → Security, or disable Secure Boot entirely.\n")
		}
	}

	var results []Result
	var failures int

	for _, step := range steps {
		fmt.Fprintf(output, "[sysprep] %s... ", step.Name)

		err := execCommand(client, step.Command, 2*time.Minute)
		if err != nil {
			results = append(results, Result{Step: step.Name, Status: "failed", Detail: err.Error()})
			fmt.Fprintf(output, "FAILED (%v)\n", err)
			failures++
		} else {
			results = append(results, Result{Step: step.Name, Status: "ok"})
			fmt.Fprintf(output, "ok\n")
		}
	}

	if failures > 0 {
		return results, fmt.Errorf("%d sysprep step(s) failed", failures)
	}
	return results, nil
}

// detectOS reads /etc/os-release and returns the OS family.
func detectOS(client *ssh.Client) (OSFamily, error) {
	output, err := runSSH(client, "cat /etc/os-release", 10*time.Second)
	if err != nil {
		return "", fmt.Errorf("reading /etc/os-release: %w", err)
	}
	return ParseOSRelease(output), nil
}

// ParseOSRelease extracts the OS family from /etc/os-release content.
// Exported for testing.
func ParseOSRelease(content string) OSFamily {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ID=") {
			id := strings.TrimPrefix(line, "ID=")
			id = strings.Trim(id, "\"")
			switch id {
			case "ubuntu":
				return OSUbuntu
			case "debian":
				return OSDebian
			case "alpine":
				return OSAlpine
			case "fedora":
				return OSFedora
			default:
				return OSGeneric
			}
		}
	}
	return OSGeneric
}

// buildProfile returns the ordered list of sysprep steps for an OS family.
func buildProfile(family OSFamily, opts Options) []Step {
	var steps []Step

	// --- Universal steps ---
	steps = append(steps,
		Step{"Remove SSH host keys", "sudo rm -f /etc/ssh/ssh_host_*"},
		Step{"Remove Nova authorized keys", "sudo rm -f /home/nova/.ssh/authorized_keys /root/.ssh/authorized_keys"},
		Step{"Remove Nova sudoers/doas config", removeSudoersCommand(family)},
		Step{"Clear temp directories", "sudo rm -rf /tmp/* /var/tmp/*"},
		// Remove Nova multi-node networking artifacts.
		Step{"Remove Nova network config", removeNovaNetworkCommand(family)},
		Step{"Remove cloud-init NoCloud seed", "sudo rm -rf /var/lib/cloud/seed/nocloud* 2>/dev/null || true"},
		Step{"Remove Nova /etc/hosts entries", "sudo sed -i '/# nova-managed/d' /etc/hosts 2>/dev/null || true"},
		Step{"Flush ARP cache", "sudo ip neigh flush all 2>/dev/null || true"},
		// Remove nova shared-folder mounts (9p / virtiofs) from fstab — these
		// are nova-specific and will not exist on the target hypervisor.
		Step{"Remove shared-folder fstab entries", "sudo sed -i -E '/[[:space:]]+(9p|virtiofs)[[:space:]]/d' /etc/fstab 2>/dev/null || true"},
	)

	// --- OS-specific steps ---
	switch family {
	case OSUbuntu, OSDebian:
		steps = append(steps,
			Step{"Truncate machine-id", "sudo truncate -s 0 /etc/machine-id"},
			Step{"Remove D-Bus machine-id symlink", "sudo rm -f /var/lib/dbus/machine-id"},
			Step{"Reset cloud-init", "sudo cloud-init clean --logs 2>/dev/null || true"},
			Step{"Clean apt cache", "sudo apt-get clean && sudo rm -rf /var/lib/apt/lists/*"},
			Step{"Flush and vacuum journald", "sudo journalctl --flush --rotate --vacuum-time=1s 2>/dev/null || true"},
			Step{"Truncate log files", "sudo find /var/log -type f -exec truncate -s 0 {} \\;"},
			Step{"Remove netplan generated configs", "sudo rm -f /etc/netplan/50-cloud-init.yaml /etc/netplan/90-nova-static.yaml 2>/dev/null || true"},
			Step{"Remove DHCP leases", "sudo rm -f /var/lib/dhcp/* /var/lib/NetworkManager/*.lease 2>/dev/null || true"},
			Step{"Remove udev persistent rules", "sudo rm -f /etc/udev/rules.d/70-persistent-* 2>/dev/null || true"},
			Step{"Clear bash history", "sudo find /home /root -maxdepth 2 -name '.bash_history' -delete 2>/dev/null || true"},
		)

	case OSAlpine:
		steps = append(steps,
			// Alpine doesn't use systemd — write a sentinel so the init system regenerates.
			Step{"Reset machine-id", "echo 'uninitialized' | sudo tee /etc/machine-id > /dev/null"},
			Step{"Remove D-Bus machine-id", "sudo rm -f /var/lib/dbus/machine-id 2>/dev/null || true"},
			Step{"Reset cloud-init", "sudo cloud-init clean --logs 2>/dev/null || true"},
			Step{"Clean apk cache", "sudo rm -rf /var/cache/apk/*"},
			Step{"Truncate log files", "sudo find /var/log -type f -exec truncate -s 0 {} \\;"},
			Step{"Remove DHCP leases", "sudo rm -f /var/lib/dhcpcd/* /var/lib/udhcpc/* 2>/dev/null || true"},
			Step{"Remove udev persistent rules", "sudo rm -f /etc/udev/rules.d/70-persistent-* 2>/dev/null || true"},
			Step{"Clear ash history", "sudo find /home /root -maxdepth 2 -name '.ash_history' -delete 2>/dev/null || true"},
		)

	case OSFedora:
		steps = append(steps,
			Step{"Truncate machine-id", "sudo truncate -s 0 /etc/machine-id"},
			Step{"Remove D-Bus machine-id", "sudo rm -f /var/lib/dbus/machine-id 2>/dev/null || true"},
			Step{"Reset cloud-init", "sudo cloud-init clean --logs 2>/dev/null || true"},
			Step{"Clean dnf cache", "sudo dnf clean all 2>/dev/null || true"},
			Step{"Flush and vacuum journald", "sudo journalctl --flush --rotate --vacuum-time=1s 2>/dev/null || true"},
			Step{"Truncate log files", "sudo find /var/log -type f -exec truncate -s 0 {} \\;"},
			Step{"Remove DHCP leases", "sudo rm -f /var/lib/dhcp/* /var/lib/NetworkManager/*.lease 2>/dev/null || true"},
			Step{"Remove udev persistent rules", "sudo rm -f /etc/udev/rules.d/70-persistent-* 2>/dev/null || true"},
			Step{"Clear bash history", "sudo find /home /root -maxdepth 2 -name '.bash_history' -delete 2>/dev/null || true"},
		)

	default: // OSGeneric
		steps = append(steps,
			Step{"Truncate machine-id", "sudo truncate -s 0 /etc/machine-id"},
			Step{"Remove D-Bus machine-id", "sudo rm -f /var/lib/dbus/machine-id 2>/dev/null || true"},
			Step{"Reset cloud-init (if present)", "command -v cloud-init >/dev/null 2>&1 && sudo cloud-init clean --logs || echo 'cloud-init not found, skipping'"},
			Step{"Truncate log files", "sudo find /var/log -type f -exec truncate -s 0 {} \\;"},
			Step{"Remove DHCP leases (common paths)", "sudo rm -f /var/lib/dhcp/* /var/lib/dhcpcd/* /var/lib/NetworkManager/*.lease 2>/dev/null || true"},
			Step{"Remove udev persistent rules", "sudo rm -f /etc/udev/rules.d/70-persistent-* 2>/dev/null || true"},
			Step{"Clear shell history", "sudo find /home /root -maxdepth 2 \\( -name '.bash_history' -o -name '.ash_history' \\) -delete 2>/dev/null || true"},
		)
	}

	// --- Optional: Hyper-V initramfs injection ---
	// Add hv_vmbus, hv_storvsc, and hv_netvsc to the initramfs so the image
	// boots on Hyper-V Gen 2 and Azure (without these the kernel panics on
	// mount because virtio drivers are absent from the Hyper-V SCSI path).
	// Also regenerate GRUB so root= references use UUIDs, not device names
	// (device names change from /dev/vda under QEMU to /dev/sda under Hyper-V).
	if opts.TargetHyperV {
		switch family {
		case OSUbuntu, OSDebian:
			steps = append(steps,
				Step{"Inject Hyper-V drivers into initramfs",
					"printf 'hv_vmbus\\nhv_storvsc\\nhv_netvsc\\n' | sudo tee -a /etc/initramfs-tools/modules > /dev/null && sudo update-initramfs -u"},
				Step{"Update GRUB for Hyper-V",
					"sudo update-grub 2>/dev/null || true"},
			)
		case OSFedora:
			steps = append(steps,
				Step{"Inject Hyper-V drivers into initramfs",
					"sudo dracut --add-drivers 'hv_vmbus hv_storvsc hv_netvsc' --force"},
				Step{"Update GRUB for Hyper-V",
					"sudo grub2-mkconfig -o /boot/grub2/grub.cfg 2>/dev/null || sudo grub-mkconfig -o /boot/grub/grub.cfg 2>/dev/null || true"},
			)
		case OSAlpine:
			// Alpine uses mkinitfs instead of dracut/update-initramfs.
			// Write a features.d file with glob patterns covering all three HV drivers,
			// enable the feature in mkinitfs.conf, then rebuild the initramfs.
			steps = append(steps,
				Step{"Inject Hyper-V drivers into initramfs",
					"printf 'kernel/drivers/hv/*\\nkernel/drivers/scsi/hv_storvsc*\\nkernel/drivers/net/hyperv/*\\n' | sudo tee /etc/mkinitfs/features.d/hyperv.modules > /dev/null && " +
						"grep -q hyperv /etc/mkinitfs/mkinitfs.conf || sudo sed -i 's/features=\"\\(.*\\)\"/features=\"\\1 hyperv\"/' /etc/mkinitfs/mkinitfs.conf && " +
						"sudo mkinitfs $(uname -r)"},
			)
		}
	}

	// --- Optional: remove nova user ---
	if opts.RemoveNovaUser {
		// Use userdel on systemd distros, deluser on Alpine.
		rmCmd := "sudo userdel -r nova 2>/dev/null || sudo deluser --remove-home nova 2>/dev/null || true"
		steps = append(steps, Step{"Remove internal nova user", rmCmd})
	}

	// --- Optional: zero free space ---
	if opts.ZeroFreeSpace {
		steps = append(steps,
			Step{"Zero free space (this may take a while)", "sudo dd if=/dev/zero of=/tmp/zero.fill bs=1M 2>/dev/null; sudo rm -f /tmp/zero.fill; sudo sync"},
		)
	}

	return steps
}

func removeNovaNetworkCommand(family OSFamily) string {
	switch family {
	case OSAlpine:
		return "sudo rm -f /etc/network/interfaces.d/nova-* 2>/dev/null || true"
	default:
		// Ubuntu/Debian/Fedora: remove Nova-generated netplan configs.
		return "sudo rm -f /etc/netplan/90-nova-static.yaml 2>/dev/null || true"
	}
}

func removeSudoersCommand(family OSFamily) string {
	switch family {
	case OSAlpine:
		return "sudo rm -f /etc/doas.d/nova.conf /etc/sudoers.d/nova 2>/dev/null || true"
	default:
		return "sudo rm -f /etc/sudoers.d/nova 2>/dev/null || true"
	}
}

// --- SSH helpers ---

func dialSSH(cfg SSHConfig) (*ssh.Client, error) {
	signer, err := ssh.ParsePrivateKey(cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("parsing SSH key: %w", err)
	}

	clientCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	addr := net.JoinHostPort(cfg.Host, cfg.Port)
	return ssh.Dial("tcp", addr, clientCfg)
}

func execCommand(client *ssh.Client, command string, timeout time.Duration) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	defer session.Close()

	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()

	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		session.Signal(ssh.SIGTERM)
		return fmt.Errorf("timed out after %v", timeout)
	}
}

func runSSH(client *ssh.Client, command string, timeout time.Duration) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("creating session: %w", err)
	}
	defer session.Close()

	var stdout strings.Builder
	session.Stdout = &stdout

	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()

	select {
	case err := <-done:
		return stdout.String(), err
	case <-time.After(timeout):
		session.Signal(ssh.SIGTERM)
		return "", fmt.Errorf("timed out after %v", timeout)
	}
}
