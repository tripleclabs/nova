//go:build linux

package network

import (
	"fmt"
	"net"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// tunSetIFFReq is the ifreq layout for TUNSETIFF: name + flags.
type tunSetIFFReq struct {
	Name  [unix.IFNAMSIZ]byte
	Flags uint16
	_     [22]byte // pad to 40 bytes (ifreq size)
}

// openTAP opens or creates a TAP device named `name`, assigns `ip` within
// `cidr` (e.g. "10.0.0.1", "10.0.0.0/24"), brings the interface up, and
// returns the TAP file handle, the interface MAC, and any error.
func openTAP(name, ip, cidr string) (*os.File, net.HardwareAddr, error) {
	// Open the TUN/TAP clone device.
	f, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("opening /dev/net/tun: %w", err)
	}

	// TUNSETIFF — create the TAP interface.
	var req tunSetIFFReq
	copy(req.Name[:], name)
	req.Flags = unix.IFF_TAP | unix.IFF_NO_PI

	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(),
		unix.TUNSETIFF, uintptr(unsafe.Pointer(&req))); errno != 0 {
		f.Close()
		return nil, nil, fmt.Errorf("TUNSETIFF: %w", errno)
	}

	// Open a control socket for the remaining interface ioctls.
	sock, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("opening control socket: %w", err)
	}
	defer unix.Close(sock)

	// SIOCSIFADDR — assign the IP address.
	parsedIP := net.ParseIP(ip).To4()
	if parsedIP == nil {
		f.Close()
		return nil, nil, fmt.Errorf("invalid IP address: %q", ip)
	}

	ifr, err := unix.NewIfreq(name)
	if err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("building ifreq for %q: %w", name, err)
	}

	ifr.SetInet4Addr(parsedIP)

	if err := ioctlIfreq(sock, unix.SIOCSIFADDR, ifr); err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("SIOCSIFADDR: %w", err)
	}

	// SIOCSIFNETMASK — assign the subnet mask derived from cidr.
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("parsing CIDR %q: %w", cidr, err)
	}
	mask := net.IP(ipNet.Mask).To4()
	ifr2, _ := unix.NewIfreq(name)
	ifr2.SetInet4Addr(mask)
	if err := ioctlIfreq(sock, unix.SIOCSIFNETMASK, ifr2); err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("SIOCSIFNETMASK: %w", err)
	}

	// SIOCGIFFLAGS — read current flags.
	ifr3, _ := unix.NewIfreq(name)
	if err := ioctlIfreq(sock, unix.SIOCGIFFLAGS, ifr3); err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("SIOCGIFFLAGS: %w", err)
	}
	flags := ifr3.Uint16()

	// SIOCSIFFLAGS — set IFF_UP | IFF_RUNNING.
	ifr4, _ := unix.NewIfreq(name)
	ifr4.SetUint16(flags | unix.IFF_UP | unix.IFF_RUNNING)
	if err := ioctlIfreq(sock, unix.SIOCSIFFLAGS, ifr4); err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("SIOCSIFFLAGS: %w", err)
	}

	// Read MAC via a raw ifreq (unix.SIOCGIFHWADDR stores the hardware address at
	// bytes [18:24] of the ifreq, i.e. sa_data[0:6] of the embedded sockaddr).
	mac, err := readMAC(sock, name)
	if err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("reading MAC: %w", err)
	}

	return f, mac, nil
}

// ioctlIfreq issues an ioctl with the given ifreq on the socket fd.
func ioctlIfreq(sock int, req uint, ifr *unix.Ifreq) error {
	return unix.IoctlIfreq(sock, req, ifr)
}

// rawIfreq is a 40-byte ifreq for manual MAC reads.
type rawIfreq struct {
	Name [unix.IFNAMSIZ]byte
	Data [24]byte
}

// readMAC uses a raw syscall to read SIOCGIFHWADDR into a rawIfreq and
// extracts the 6 MAC bytes from the ifr_hwaddr.sa_data field.
func readMAC(sock int, name string) (net.HardwareAddr, error) {
	var ifr rawIfreq
	copy(ifr.Name[:], name)
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(sock),
		unix.SIOCGIFHWADDR, uintptr(unsafe.Pointer(&ifr))); errno != 0 {
		return nil, errno
	}
	// ifr_hwaddr: sa_family(2 bytes) + sa_data[0:6] = MAC
	mac := make(net.HardwareAddr, 6)
	copy(mac, ifr.Data[2:8])
	return mac, nil
}
