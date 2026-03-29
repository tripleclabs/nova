//go:build linux

package network

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// L2Switch is a userspace virtual Ethernet switch.
// Each VM connects via a socketpair; the host connects via a TAP device.
type L2Switch struct {
	mu      sync.RWMutex
	ports   map[string]*switchPort // nodeName → port
	macTable map[[6]byte]string    // MAC → nodeName
	cond     *Conditioner
	tapFile  *os.File
	tapName  string
	tapMAC   net.HardwareAddr
	ctx      context.Context
	cancel   context.CancelFunc
}

type switchPort struct {
	name     string
	daemonFD *os.File // daemon side of the socketpair; switch reads from this
}

// NewL2Switch creates a new L2Switch backed by a TAP device named tapName
// with address 10.0.0.1/24.  Returns an error if the TAP device cannot be
// opened (e.g. missing CAP_NET_ADMIN).
func NewL2Switch(cond *Conditioner, tapName string) (*L2Switch, error) {
	tapFile, tapMAC, err := openTAP(tapName, "10.0.0.1", "10.0.0.0/24")
	if err != nil {
		return nil, fmt.Errorf("opening nova0 TAP: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sw := &L2Switch{
		ports:    make(map[string]*switchPort),
		macTable: make(map[[6]byte]string),
		cond:     cond,
		tapFile:  tapFile,
		tapName:  tapName,
		tapMAC:   tapMAC,
		ctx:      ctx,
		cancel:   cancel,
	}

	if err := enableNAT("10.0.0.0/24", tapName); err != nil {
		tapFile.Close()
		cancel()
		return nil, fmt.Errorf("enabling NAT: %w", err)
	}

	go sw.readTAP()
	return sw, nil
}

// NewL2SwitchForCluster creates an L2Switch for multi-node clusters.
// On Linux this is identical to NewL2Switch (TAP-backed).
func NewL2SwitchForCluster(cond *Conditioner, tapName string) (*L2Switch, error) {
	return NewL2Switch(cond, tapName)
}

// NewPort allocates a socketpair for a new VM.  The QEMU-side *os.File is
// returned; the caller must add it to cmd.ExtraFiles and then close it in the
// parent after cmd.Start().
func (sw *L2Switch) NewPort(nodeName string) (*os.File, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("socketpair: %w", err)
	}

	daemonFile := os.NewFile(uintptr(fds[0]), nodeName+"-daemon")
	qemuFile := os.NewFile(uintptr(fds[1]), nodeName+"-qemu")

	port := &switchPort{name: nodeName, daemonFD: daemonFile}

	sw.mu.Lock()
	sw.ports[nodeName] = port
	sw.mu.Unlock()

	go sw.readPort(port)
	return qemuFile, nil
}

// RemovePort closes the daemon side of a port and removes it from the switch.
func (sw *L2Switch) RemovePort(nodeName string) {
	sw.mu.Lock()
	port, ok := sw.ports[nodeName]
	if ok {
		delete(sw.ports, nodeName)
		// Remove stale MAC entries pointing to this node.
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

// Close shuts down the switch (cancels reader goroutines, closes TAP, removes NAT rules).
func (sw *L2Switch) Close() error {
	sw.cancel()
	disableNAT("10.0.0.0/24", sw.tapName)
	return sw.tapFile.Close()
}

const nftTableName = "nova_nat"

// enableNAT sets up IP forwarding and an nftables masquerade rule so VMs can
// reach the internet via the host. Uses netlink in-process (CAP_NET_ADMIN only,
// no root required).
func enableNAT(subnet, tapIface string) error {
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0644); err != nil {
		return fmt.Errorf("enabling ip_forward: %w", err)
	}

	c, err := nftables.New()
	if err != nil {
		return fmt.Errorf("nftables client: %w", err)
	}

	// Remove any stale table from a previous run before recreating.
	c.DelTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: nftTableName})
	_ = c.Flush()

	_, ipNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return fmt.Errorf("parsing subnet: %w", err)
	}
	ipv4 := ipNet.IP.To4()

	t := c.AddTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: nftTableName})

	// Forward chain: accept traffic to/from the nova subnet before firewalld
	// (priority -1 runs ahead of firewalld's priority-0 filter chains).
	fwdChain := c.AddChain(&nftables.Chain{
		Name:     "forward",
		Table:    t,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookForward,
		Priority: nftables.ChainPriorityRef(-1),
	})
	for _, offset := range []uint32{12, 16} { // src=12, dst=16 in IPv4 header
		c.AddRule(&nftables.Rule{
			Table: t,
			Chain: fwdChain,
			Exprs: []expr.Any{
				&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: 4},
				&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: []byte(ipNet.Mask), Xor: []byte{0, 0, 0, 0}},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ipv4},
				&expr.Verdict{Kind: expr.VerdictAccept},
			},
		})
	}

	// NAT chain: masquerade outbound traffic from the subnet.
	natChain := c.AddChain(&nftables.Chain{
		Name:     "postrouting",
		Table:    t,
		Type:     nftables.ChainTypeNAT,
		Hooknum:  nftables.ChainHookPostrouting,
		Priority: nftables.ChainPriorityNATSource,
	})
	// Rule: ip saddr <subnet> oifname != <tapIface> masquerade
	c.AddRule(&nftables.Rule{
		Table: t,
		Chain: natChain,
		Exprs: []expr.Any{
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4},
			&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: []byte(ipNet.Mask), Xor: []byte{0, 0, 0, 0}},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ipv4},
			&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
			&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: ifnamePad(tapIface)},
			&expr.Masq{},
		},
	})

	if err := c.Flush(); err != nil {
		return fmt.Errorf("nftables flush: %w", err)
	}

	// If Docker is running it installs a FORWARD chain at priority 0 with
	// policy drop.  Our priority -1 accept doesn't prevent that chain from
	// also running (accept in a lower-priority chain is not globally terminal
	// in nftables).  Docker provides DOCKER-USER for exactly this purpose:
	// rules added there are consulted before Docker's own drop policy.
	injectDockerUserRules(c, ipNet)

	slog.Info("NAT enabled", "subnet", subnet)
	return nil
}

// injectDockerUserRules adds accept rules for the nova subnet to Docker's
// DOCKER-USER chain so Docker's FORWARD drop policy doesn't block VM traffic.
// It is a best-effort call: any error is logged at debug level and ignored.
func injectDockerUserRules(c *nftables.Conn, ipNet *net.IPNet) {
	filterTable, err := c.ListTableOfFamily("filter", nftables.TableFamilyIPv4)
	if err != nil || filterTable == nil {
		return // no iptables-compat filter table → Docker not present
	}

	dockerUser, err := c.ListChain(filterTable, "DOCKER-USER")
	if err != nil || dockerUser == nil {
		return // DOCKER-USER chain not present
	}

	ipv4 := ipNet.IP.To4()
	// Insert at the head of DOCKER-USER: accept src or dst in nova subnet.
	for _, offset := range []uint32{12, 16} { // src=12, dst=16 in IPv4 header
		c.InsertRule(&nftables.Rule{
			Table: filterTable,
			Chain: dockerUser,
			Exprs: []expr.Any{
				&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: 4},
				&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: []byte(ipNet.Mask), Xor: []byte{0, 0, 0, 0}},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ipv4},
				&expr.Verdict{Kind: expr.VerdictAccept},
			},
		})
	}
	if err := c.Flush(); err != nil {
		slog.Debug("could not inject DOCKER-USER rules", "err", err)
	} else {
		slog.Info("injected nova accept rules into DOCKER-USER chain")
	}
}

// disableNAT removes the nftables table created by enableNAT.
func disableNAT(subnet, tapIface string) {
	c, err := nftables.New()
	if err != nil {
		return
	}
	c.DelTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: nftTableName})
	_ = c.Flush()
}

// ifnamePad returns a 16-byte zero-padded interface name for nftables comparisons.
func ifnamePad(name string) []byte {
	b := make([]byte, 16)
	copy(b, name)
	return b
}

// readTAP continuously reads Ethernet frames from the TAP device and dispatches them.
func (sw *L2Switch) readTAP() {
	const tapName = "__tap__"
	for {
		frame, err := readFrame(sw.tapFile, true)
		if err != nil {
			select {
			case <-sw.ctx.Done():
				return
			default:
				slog.Warn("TAP read error", "err", err)
				time.Sleep(10 * time.Millisecond)
				continue
			}
		}
		sw.dispatch(tapName, frame)
	}
}

// readPort continuously reads length-prefixed frames from a socket port and dispatches them.
func (sw *L2Switch) readPort(port *switchPort) {
	for {
		frame, err := readFrame(port.daemonFD, false)
		if err != nil {
			select {
			case <-sw.ctx.Done():
				return
			default:
				if err != io.EOF && err != io.ErrClosedPipe {
					slog.Debug("port read error", "node", port.name, "err", err)
				}
				return
			}
		}
		sw.dispatch(port.name, frame)
	}
}

// dispatch routes a single Ethernet frame from srcName to its destination(s).
func (sw *L2Switch) dispatch(srcName string, data []byte) {
	if len(data) < 14 {
		return // too short to be a valid Ethernet frame
	}

	// Learn the source MAC.
	var srcMAC [6]byte
	copy(srcMAC[:], data[6:12])
	sw.mu.Lock()
	if srcName != "__tap__" {
		sw.macTable[srcMAC] = srcName
	}

	// Determine destinations.
	var dstMAC [6]byte
	copy(dstMAC[:], data[0:6])

	broadcast := dstMAC == [6]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	dstName := ""
	if !broadcast {
		dstName = sw.macTable[dstMAC]
	}

	// Collect destinations: snapshot the relevant ports while holding the lock.
	type dest struct {
		name string
		f    *os.File
		isTAP bool
	}
	var dests []dest

	if broadcast || dstName == "" {
		// Flood: all ports except source, plus TAP (unless source is TAP).
		for name, port := range sw.ports {
			if name == srcName {
				continue
			}
			dests = append(dests, dest{name: name, f: port.daemonFD, isTAP: false})
		}
		if srcName != "__tap__" {
			dests = append(dests, dest{name: "__tap__", f: sw.tapFile, isTAP: true})
		}
	} else {
		// Unicast.
		if dstName == "__tap__" {
			dests = append(dests, dest{name: "__tap__", f: sw.tapFile, isTAP: true})
		} else if port, ok := sw.ports[dstName]; ok {
			dests = append(dests, dest{name: dstName, f: port.daemonFD, isTAP: false})
		}
	}
	sw.mu.Unlock()

	// Forward to each destination, consulting the conditioner.
	for _, d := range dests {
		d := d // capture for closure
		if sw.cond != nil && sw.cond.ShouldDrop(srcName, d.name) {
			continue
		}

		// Copy data before any async send to avoid slice reuse bugs.
		frameCopy := make([]byte, len(data))
		copy(frameCopy, data)

		delay := time.Duration(0)
		if sw.cond != nil {
			delay = sw.cond.Delay(srcName, d.name)
		}

		if delay > 0 {
			time.AfterFunc(delay, func() {
				if err := writeFrame(d.f, frameCopy, d.isTAP); err != nil {
					slog.Debug("frame write error (delayed)", "dst", d.name, "err", err)
				}
			})
		} else {
			if err := writeFrame(d.f, frameCopy, d.isTAP); err != nil {
				slog.Debug("frame write error", "dst", d.name, "err", err)
			}
		}
	}
}

// writeFrame writes an Ethernet frame to f.
// For TAP devices: write the raw frame (no length prefix).
// For socket ports: write a 4-byte big-endian length prefix followed by the frame.
func writeFrame(f *os.File, data []byte, isTAP bool) error {
	if isTAP {
		_, err := f.Write(data)
		return err
	}
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(data)))
	if _, err := f.Write(hdr); err != nil {
		return err
	}
	_, err := f.Write(data)
	return err
}

// readFrame reads a single Ethernet frame from f.
// For TAP devices: one Read call returns exactly one frame.
// For socket ports: read 4-byte BE length, then read that many bytes.
func readFrame(f *os.File, isTAP bool) ([]byte, error) {
	if isTAP {
		buf := make([]byte, 65536)
		n, err := f.Read(buf)
		if err != nil {
			return nil, err
		}
		return buf[:n], nil
	}

	// Read 4-byte length prefix.
	var lenBuf [4]byte
	if _, err := io.ReadFull(f, lenBuf[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(lenBuf[:])
	if length == 0 || length > 65536 {
		return nil, fmt.Errorf("invalid frame length: %d", length)
	}
	frame := make([]byte, length)
	if _, err := io.ReadFull(f, frame); err != nil {
		return nil, err
	}
	return frame, nil
}
