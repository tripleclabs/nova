package network

import (
	"net"
	"testing"
)

func TestCheckPortAvailable_Free(t *testing.T) {
	// Find a free port.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	if err := CheckPortAvailable(port); err != nil {
		t.Errorf("free port should be available: %v", err)
	}
}

func TestCheckPortAvailable_Occupied(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	if err := CheckPortAvailable(port); err == nil {
		t.Error("occupied port should not be available")
	}
}

func TestCheckPortsAvailable(t *testing.T) {
	// Two free ports.
	ln1, _ := net.Listen("tcp", "127.0.0.1:0")
	p1 := ln1.Addr().(*net.TCPAddr).Port
	ln1.Close()

	ln2, _ := net.Listen("tcp", "127.0.0.1:0")
	p2 := ln2.Addr().(*net.TCPAddr).Port
	ln2.Close()

	if err := CheckPortsAvailable([]int{p1, p2}); err != nil {
		t.Errorf("both ports should be free: %v", err)
	}
}

func TestCheckPortsAvailable_OneOccupied(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	occupied := ln.Addr().(*net.TCPAddr).Port

	ln2, _ := net.Listen("tcp", "127.0.0.1:0")
	free := ln2.Addr().(*net.TCPAddr).Port
	ln2.Close()

	if err := CheckPortsAvailable([]int{free, occupied}); err == nil {
		t.Error("should fail when one port is occupied")
	}
}
