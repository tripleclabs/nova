//go:build !linux

package hypervisor

import "fmt"

func newQEMUEngine() (Hypervisor, error) {
	return nil, fmt.Errorf("QEMU/KVM engine is only available on Linux")
}
