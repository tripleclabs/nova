//go:build !darwin

package hypervisor

import "fmt"

func newVZEngine() (Hypervisor, error) {
	return nil, fmt.Errorf("Apple Virtualization.framework is only available on macOS")
}
