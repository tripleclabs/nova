package network

import (
	"fmt"
	"net"
)

// CheckPortAvailable attempts to listen on the given TCP port to verify
// it is not already in use. Returns an error if the port is occupied.
func CheckPortAvailable(port int) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("host port %d is already in use: %w", port, err)
	}
	ln.Close()
	return nil
}

// CheckPortsAvailable verifies a list of ports are all free.
func CheckPortsAvailable(ports []int) error {
	for _, p := range ports {
		if err := CheckPortAvailable(p); err != nil {
			return err
		}
	}
	return nil
}
