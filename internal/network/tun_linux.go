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

	_, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(),
		unix.TUNSETIFF, uintptr(unsafe.Pointer(&req)))
	if errno == unix.EBUSY {
		// A stale device from a previous daemon is still registered (e.g. two
		// daemons started back-to-back before the first fully exited). Delete it
		// and retry once.
		f.Close()
		if delErr := deleteInterface(name); delErr == nil {
			f, err = os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
			if err != nil {
				return nil, nil, fmt.Errorf("opening /dev/net/tun after cleanup: %w", err)
			}
			var req2 tunSetIFFReq
			copy(req2.Name[:], name)
			req2.Flags = unix.IFF_TAP | unix.IFF_NO_PI
			_, _, errno = unix.Syscall(unix.SYS_IOCTL, f.Fd(),
				unix.TUNSETIFF, uintptr(unsafe.Pointer(&req2)))
		} else {
			return nil, nil, fmt.Errorf("TUNSETIFF: device busy and cleanup failed: %w", delErr)
		}
	}
	if errno != 0 {
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

// deleteInterface removes a network interface by name using SIOCSIFFLAGS to
// bring it down then SIOCGIFINDEX + RTM_DELLINK via netlink to delete it.
// This is used to clean up a stale TAP device left by a previous daemon run.
func deleteInterface(name string) error {
	sock, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer unix.Close(sock)

	// Bring the interface down first.
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return err
	}
	_ = unix.IoctlIfreq(sock, unix.SIOCGIFFLAGS, ifr)
	flags := ifr.Uint16() &^ uint16(unix.IFF_UP)
	ifr.SetUint16(flags)
	_ = unix.IoctlIfreq(sock, unix.SIOCSIFFLAGS, ifr)

	// Delete via netlink RTM_DELLINK.
	nlSock, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer unix.Close(nlSock)

	ifrIdx, err := unix.NewIfreq(name)
	if err != nil {
		return err
	}
	nlSock2, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer unix.Close(nlSock2)
	if err := unix.IoctlIfreq(nlSock2, unix.SIOCGIFINDEX, ifrIdx); err != nil {
		return err
	}
	ifIndex := ifrIdx.Uint32()

	// Build a minimal RTM_DELLINK message.
	msg := unix.RtAttr{}
	_ = msg
	nlmsg := &unix.NlMsghdr{
		Type:  unix.RTM_DELLINK,
		Flags: unix.NLM_F_REQUEST | unix.NLM_F_ACK,
		Seq:   1,
	}
	ifinfo := unix.IfInfomsg{Index: int32(ifIndex)}
	nlmsgBytes := (*[unix.SizeofNlMsghdr]byte)(unsafe.Pointer(nlmsg))[:]
	ifinfoBytes := (*[unix.SizeofIfInfomsg]byte)(unsafe.Pointer(&ifinfo))[:]
	totalLen := unix.SizeofNlMsghdr + unix.SizeofIfInfomsg
	nlmsg.Len = uint32(totalLen)
	buf := make([]byte, totalLen)
	copy(buf[:unix.SizeofNlMsghdr], nlmsgBytes)
	copy(buf[unix.SizeofNlMsghdr:], ifinfoBytes)

	sa := &unix.SockaddrNetlink{Family: unix.AF_NETLINK}
	return unix.Sendto(nlSock, buf, 0, sa)
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
