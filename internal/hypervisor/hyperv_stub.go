//go:build !windows

package hypervisor

import "fmt"

func newHyperVEngine() (Hypervisor, error) {
	return nil, fmt.Errorf("Hyper-V engine is only available on Windows")
}
