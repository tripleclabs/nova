//go:build !linux

package network

import "os"

// L2Switch is a no-op stub on non-Linux platforms.
type L2Switch struct{}

// NewL2Switch always returns nil on non-Linux platforms.
func NewL2Switch(cond *Conditioner) (*L2Switch, error) {
	return nil, nil
}

// NewPort is a no-op stub.
func (sw *L2Switch) NewPort(nodeName string) (*os.File, error) {
	return nil, nil
}

// RemovePort is a no-op stub.
func (sw *L2Switch) RemovePort(nodeName string) {}

// Close is a no-op stub.
func (sw *L2Switch) Close() error { return nil }
