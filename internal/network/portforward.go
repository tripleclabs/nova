// Package network provides user-space port forwarding and network utilities.
package network

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

// PortForwardRule describes a single host->guest port mapping.
type PortForwardRule struct {
	HostPort  int
	GuestIP   string
	GuestPort int
	Protocol  string // "tcp" or "udp"
}

// Forwarder manages a set of user-space port forwards.
type Forwarder struct {
	mu        sync.Mutex
	rules     []PortForwardRule
	listeners []io.Closer
	cancel    context.CancelFunc
}

// NewForwarder creates a Forwarder for the given rules.
func NewForwarder(rules []PortForwardRule) *Forwarder {
	return &Forwarder{rules: rules}
}

// Start begins listening on all configured host ports and proxying to the guest.
func (f *Forwarder) Start(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	ctx, f.cancel = context.WithCancel(ctx)

	for _, rule := range f.rules {
		switch rule.Protocol {
		case "tcp", "":
			if err := f.startTCP(ctx, rule); err != nil {
				f.stopLocked()
				return err
			}
		case "udp":
			if err := f.startUDP(ctx, rule); err != nil {
				f.stopLocked()
				return err
			}
		default:
			return fmt.Errorf("unsupported protocol: %s", rule.Protocol)
		}
	}

	return nil
}

// Stop closes all listeners and terminates forwarding.
func (f *Forwarder) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopLocked()
}

func (f *Forwarder) stopLocked() {
	if f.cancel != nil {
		f.cancel()
	}
	for _, l := range f.listeners {
		l.Close()
	}
	f.listeners = nil
}

func (f *Forwarder) startTCP(ctx context.Context, rule PortForwardRule) error {
	addr := fmt.Sprintf("127.0.0.1:%d", rule.HostPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}
	f.listeners = append(f.listeners, ln)

	guestAddr := fmt.Sprintf("%s:%d", rule.GuestIP, rule.GuestPort)
	slog.Info("TCP port forward", "host", addr, "guest", guestAddr)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					slog.Error("accept failed", "addr", addr, "error", err)
					return
				}
			}
			go proxyTCP(ctx, conn, guestAddr)
		}
	}()

	return nil
}

func proxyTCP(ctx context.Context, client net.Conn, guestAddr string) {
	defer client.Close()

	dialer := net.Dialer{Timeout: 5 * time.Second}
	guest, err := dialer.DialContext(ctx, "tcp", guestAddr)
	if err != nil {
		slog.Debug("dial guest failed", "addr", guestAddr, "error", err)
		return
	}
	defer guest.Close()

	done := make(chan struct{})
	go func() {
		io.Copy(guest, client)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(client, guest)
		done <- struct{}{}
	}()

	// Wait for either direction to finish or context to cancel.
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (f *Forwarder) startUDP(ctx context.Context, rule PortForwardRule) error {
	addr := fmt.Sprintf("127.0.0.1:%d", rule.HostPort)
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("listening UDP on %s: %w", addr, err)
	}
	f.listeners = append(f.listeners, pc)

	guestAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", rule.GuestIP, rule.GuestPort))
	if err != nil {
		return fmt.Errorf("resolving guest UDP addr: %w", err)
	}
	slog.Info("UDP port forward", "host", addr, "guest", guestAddr)

	go func() {
		buf := make([]byte, 65535)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			n, clientAddr, err := pc.ReadFrom(buf)
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					slog.Debug("UDP read error", "error", err)
					continue
				}
			}

			// Forward to guest.
			guestConn, err := net.DialUDP("udp", nil, guestAddr)
			if err != nil {
				slog.Debug("dial guest UDP failed", "error", err)
				continue
			}

			guestConn.Write(buf[:n])

			// Read reply with timeout.
			guestConn.SetReadDeadline(time.Now().Add(5 * time.Second))
			rn, err := guestConn.Read(buf)
			guestConn.Close()
			if err != nil {
				continue
			}

			// Send reply back to client.
			pc.WriteTo(buf[:rn], clientAddr)
		}
	}()

	return nil
}
