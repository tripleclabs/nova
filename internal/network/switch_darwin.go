//go:build darwin

package network

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// L2Switch is a userspace virtual Ethernet switch for macOS.
// Each VM connects via a SOCK_DGRAM socketpair; one end goes to VZ's
// FileHandleNetworkDeviceAttachment, the other is read by the switch.
// Unlike Linux, there is no TAP device — the switch only connects VMs
// to each other. Internet access comes from each VM's separate NAT NIC.
type L2Switch struct {
	mu       sync.RWMutex
	ports    map[string]*switchPort // nodeName → port
	macTable map[[6]byte]string     // MAC → nodeName
	cond     *Conditioner
	ctx      context.Context
	cancel   context.CancelFunc
}

type switchPort struct {
	name     string
	daemonFD *os.File // daemon side of the socketpair; switch reads/writes this
}

// NewL2Switch returns nil on macOS — the switch is created lazily by the
// orchestrator only when a multi-node cluster is booted. This avoids adding
// a second NIC to single-VM configs.
func NewL2Switch(cond *Conditioner) (*L2Switch, error) {
	return nil, nil
}

// NewL2SwitchForCluster creates a new L2Switch for macOS multi-node clusters.
// No TAP device or NAT is needed — VMs get internet via their NAT NIC.
func NewL2SwitchForCluster(cond *Conditioner) (*L2Switch, error) {
	ctx, cancel := context.WithCancel(context.Background())
	return &L2Switch{
		ports:    make(map[string]*switchPort),
		macTable: make(map[[6]byte]string),
		cond:     cond,
		ctx:      ctx,
		cancel:   cancel,
	}, nil
}

// NewPort allocates a SOCK_DGRAM socketpair for a new VM.
// Returns the VZ-side *os.File (pass to FileHandleNetworkDeviceAttachment).
// The switch keeps the other end for frame relay.
func (sw *L2Switch) NewPort(nodeName string) (*os.File, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		return nil, err
	}

	// Increase socket buffer sizes for reliable frame delivery.
	// VZ recommends SO_RCVBUF >= 4 * SO_SNDBUF.
	for _, fd := range fds {
		unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_SNDBUF, 1024*1024)
		unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, 4*1024*1024)
	}

	daemonFile := os.NewFile(uintptr(fds[0]), nodeName+"-switch")
	vzFile := os.NewFile(uintptr(fds[1]), nodeName+"-vz")

	port := &switchPort{name: nodeName, daemonFD: daemonFile}

	sw.mu.Lock()
	sw.ports[nodeName] = port
	sw.mu.Unlock()

	go sw.readPort(port)
	slog.Info("switch port added", "node", nodeName)
	return vzFile, nil
}

// RemovePort closes the daemon side of a port and removes it from the switch.
func (sw *L2Switch) RemovePort(nodeName string) {
	sw.mu.Lock()
	port, ok := sw.ports[nodeName]
	if ok {
		delete(sw.ports, nodeName)
		for mac, name := range sw.macTable {
			if name == nodeName {
				delete(sw.macTable, mac)
			}
		}
	}
	sw.mu.Unlock()

	if ok && port.daemonFD != nil {
		port.daemonFD.Close()
	}
}

// Close shuts down the switch.
func (sw *L2Switch) Close() error {
	sw.cancel()
	sw.mu.Lock()
	for _, port := range sw.ports {
		port.daemonFD.Close()
	}
	sw.ports = make(map[string]*switchPort)
	sw.mu.Unlock()
	return nil
}

// readPort reads DGRAM frames from a VM's socket and dispatches them.
// Each recv() returns exactly one Ethernet frame (DGRAM boundary).
func (sw *L2Switch) readPort(port *switchPort) {
	buf := make([]byte, 65536)
	for {
		n, err := port.daemonFD.Read(buf)
		if err != nil {
			select {
			case <-sw.ctx.Done():
				return
			default:
				slog.Debug("switch port read error", "node", port.name, "err", err)
				return
			}
		}
		if n < 14 {
			continue // too short for Ethernet
		}
		frame := make([]byte, n)
		copy(frame, buf[:n])
		sw.dispatch(port.name, frame)
	}
}

// dispatch routes a frame from srcName to its destination(s).
func (sw *L2Switch) dispatch(srcName string, data []byte) {
	// Learn the source MAC.
	var srcMAC [6]byte
	copy(srcMAC[:], data[6:12])

	sw.mu.Lock()
	sw.macTable[srcMAC] = srcName

	var dstMAC [6]byte
	copy(dstMAC[:], data[0:6])
	broadcast := dstMAC == [6]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

	// Multicast: also flood (important for ARP, DHCP, etc.)
	multicast := data[0]&0x01 != 0

	dstName := ""
	if !broadcast && !multicast {
		dstName = sw.macTable[dstMAC]
	}

	type dest struct {
		name string
		f    *os.File
	}
	var dests []dest

	if broadcast || multicast || dstName == "" {
		// Flood to all ports except source.
		for name, port := range sw.ports {
			if name == srcName {
				continue
			}
			dests = append(dests, dest{name: name, f: port.daemonFD})
		}
	} else {
		if port, ok := sw.ports[dstName]; ok {
			dests = append(dests, dest{name: dstName, f: port.daemonFD})
		}
	}
	sw.mu.Unlock()

	for _, d := range dests {
		if sw.cond != nil && sw.cond.ShouldDrop(srcName, d.name) {
			continue
		}

		frameCopy := make([]byte, len(data))
		copy(frameCopy, data)

		delay := time.Duration(0)
		if sw.cond != nil {
			delay = sw.cond.Delay(srcName, d.name)
		}

		if delay > 0 {
			d := d
			time.AfterFunc(delay, func() {
				d.f.Write(frameCopy)
			})
		} else {
			d.f.Write(frameCopy)
		}
	}
}

