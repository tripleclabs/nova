//go:build !linux && !darwin

package network

import "os"

// L2Switch is a no-op stub on non-Linux platforms.
type L2Switch struct{}

// NewL2Switch always returns nil on non-Linux platforms.
func NewL2Switch(cond *Conditioner, tapName, cidr, gateway string) (*L2Switch, error) {
	return nil, nil
}

// Subnet returns the stub's CIDR (empty on unsupported platforms).
func (sw *L2Switch) Subnet() string { return "" }

// Gateway returns the stub's gateway (empty on unsupported platforms).
func (sw *L2Switch) Gateway() string { return "" }

// NewPort is a no-op stub.
func (sw *L2Switch) NewPort(nodeName string) (*os.File, error) {
	return nil, nil
}

// RemovePort is a no-op stub.
func (sw *L2Switch) RemovePort(nodeName string) {}

// Close is a no-op stub.
func (sw *L2Switch) Close() error { return nil }

// NewL2SwitchForCluster is a no-op stub on unsupported platforms.
func NewL2SwitchForCluster(cond *Conditioner, tapName, cidr, gateway string) (*L2Switch, error) {
	return nil, nil
}
