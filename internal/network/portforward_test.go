package network

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

func TestForwarder_TCP(t *testing.T) {
	// Start a fake "guest" TCP server.
	guestLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer guestLn.Close()

	guestPort := guestLn.Addr().(*net.TCPAddr).Port

	go func() {
		for {
			conn, err := guestLn.Accept()
			if err != nil {
				return
			}
			conn.Write([]byte("hello from guest"))
			conn.Close()
		}
	}()

	// Pick a free host port.
	hostLn, _ := net.Listen("tcp", "127.0.0.1:0")
	hostPort := hostLn.Addr().(*net.TCPAddr).Port
	hostLn.Close()

	fwd := NewForwarder([]PortForwardRule{
		{HostPort: hostPort, GuestIP: "127.0.0.1", GuestPort: guestPort, Protocol: "tcp"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := fwd.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer fwd.Stop()

	// Give listener a moment to start.
	time.Sleep(50 * time.Millisecond)

	// Connect through the forwarder.
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", hostPort))
	if err != nil {
		t.Fatalf("dial forwarder: %v", err)
	}
	defer conn.Close()

	buf, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "hello from guest" {
		t.Errorf("got %q, want %q", buf, "hello from guest")
	}
}

func TestForwarder_UDP(t *testing.T) {
	// Start a fake "guest" UDP server.
	guestPC, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer guestPC.Close()

	guestPort := guestPC.LocalAddr().(*net.UDPAddr).Port

	go func() {
		buf := make([]byte, 1024)
		for {
			n, addr, err := guestPC.ReadFrom(buf)
			if err != nil {
				return
			}
			guestPC.WriteTo(append([]byte("echo:"), buf[:n]...), addr)
		}
	}()

	// Pick a free host port.
	hostPC, _ := net.ListenPacket("udp", "127.0.0.1:0")
	hostPort := hostPC.LocalAddr().(*net.UDPAddr).Port
	hostPC.Close()

	fwd := NewForwarder([]PortForwardRule{
		{HostPort: hostPort, GuestIP: "127.0.0.1", GuestPort: guestPort, Protocol: "udp"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := fwd.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer fwd.Stop()

	time.Sleep(50 * time.Millisecond)

	// Send a UDP packet through the forwarder.
	conn, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", hostPort))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	conn.Write([]byte("ping"))
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf[:n]) != "echo:ping" {
		t.Errorf("got %q, want %q", buf[:n], "echo:ping")
	}
}

func TestForwarder_Stop(t *testing.T) {
	hostLn, _ := net.Listen("tcp", "127.0.0.1:0")
	hostPort := hostLn.Addr().(*net.TCPAddr).Port
	hostLn.Close()

	fwd := NewForwarder([]PortForwardRule{
		{HostPort: hostPort, GuestIP: "127.0.0.1", GuestPort: 9999, Protocol: "tcp"},
	})

	ctx := context.Background()
	if err := fwd.Start(ctx); err != nil {
		t.Fatal(err)
	}

	fwd.Stop()

	// Port should be free again after stop.
	time.Sleep(50 * time.Millisecond)
	if err := CheckPortAvailable(hostPort); err != nil {
		t.Errorf("port should be free after Stop: %v", err)
	}
}

func TestForwarder_PortConflict(t *testing.T) {
	// Occupy a port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	fwd := NewForwarder([]PortForwardRule{
		{HostPort: port, GuestIP: "127.0.0.1", GuestPort: 80, Protocol: "tcp"},
	})

	err = fwd.Start(context.Background())
	if err == nil {
		fwd.Stop()
		t.Fatal("expected error for port conflict")
	}
}
