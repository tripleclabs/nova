//go:build !linux

package hypervisor

import (
	"context"
	"fmt"
)

func newQEMUEngine() (Hypervisor, error) {
	return nil, fmt.Errorf("QEMU/KVM engine is only available on Linux")
}

func attachQEMUEngine(_ context.Context, _ VMConfig) (Hypervisor, error) {
	return nil, fmt.Errorf("QEMU reattach is only available on Linux")
}
