//go:build darwin

package hypervisor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/Code-Hex/vz/v3"
)

// vzEngine implements the Hypervisor interface using Apple's Virtualization.framework.
type vzEngine struct {
	mu    sync.Mutex
	vm    *vz.VirtualMachine
	state State
	cfg   VMConfig
}

func newVZEngine() (Hypervisor, error) {
	return &vzEngine{state: StateStopped}, nil
}

func (e *vzEngine) Start(ctx context.Context, cfg VMConfig) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.cfg = cfg
	e.state = StateStarting
	slog.Info("configuring VM", "name", cfg.Name, "cpus", cfg.CPUs, "memory_mb", cfg.MemoryMB)

	vzCfg, err := e.buildVZConfig(cfg)
	if err != nil {
		e.state = StateError
		return fmt.Errorf("building VM config: %w", err)
	}

	valid, err := vzCfg.Validate()
	if !valid || err != nil {
		e.state = StateError
		return fmt.Errorf("invalid VM config: %w", err)
	}

	vm, err := vz.NewVirtualMachine(vzCfg)
	if err != nil {
		e.state = StateError
		return fmt.Errorf("creating VM: %w", err)
	}
	e.vm = vm

	slog.Info("starting VM", "name", cfg.Name)
	if err := vm.Start(); err != nil {
		e.state = StateError
		return fmt.Errorf("starting VM: %w", err)
	}

	// Wait for the VM to reach the running state.
	e.state = StateRunning
	slog.Info("VM started", "name", cfg.Name)

	// Monitor state changes in background.
	go e.watchState()

	return nil
}

func (e *vzEngine) Stop(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.vm == nil {
		return fmt.Errorf("VM not started")
	}

	if !e.vm.CanRequestStop() {
		return e.forceKillLocked()
	}

	stopped, err := e.vm.RequestStop()
	if err != nil {
		return fmt.Errorf("requesting stop: %w", err)
	}
	if !stopped {
		slog.Warn("graceful stop request returned false, forcing kill")
		return e.forceKillLocked()
	}

	e.state = StateStopped
	slog.Info("VM stopped gracefully", "name", e.cfg.Name)
	return nil
}

func (e *vzEngine) ForceKill() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.forceKillLocked()
}

func (e *vzEngine) forceKillLocked() error {
	if e.vm == nil {
		return fmt.Errorf("VM not started")
	}
	if err := e.vm.Stop(); err != nil {
		return fmt.Errorf("force stopping VM: %w", err)
	}
	e.state = StateStopped
	slog.Info("VM force killed", "name", e.cfg.Name)
	return nil
}

func (e *vzEngine) GetState() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

func (e *vzEngine) GuestIP() (string, error) {
	// VZ framework doesn't expose the guest IP directly.
	// We'll rely on DHCP and ARP scanning, or cloud-init phone-home.
	// For now return the common NAT gateway guest IP.
	return "", fmt.Errorf("guest IP discovery not yet implemented for VZ engine")
}

func (e *vzEngine) buildVZConfig(cfg VMConfig) (*vz.VirtualMachineConfiguration, error) {
	// Boot loader — EFI for full UEFI disk boot.
	efi, err := vz.NewEFIBootLoader()
	if err != nil {
		return nil, fmt.Errorf("creating EFI boot loader: %w", err)
	}

	vzCfg, err := vz.NewVirtualMachineConfiguration(efi, cfg.CPUs, cfg.MemoryMB*1024*1024)
	if err != nil {
		return nil, fmt.Errorf("creating VM configuration: %w", err)
	}

	// Storage — main disk.
	diskAttachment, err := vz.NewDiskImageStorageDeviceAttachmentWithCacheAndSync(
		cfg.DiskPath, false, vz.DiskImageCachingModeAutomatic, vz.DiskImageSynchronizationModeFsync,
	)
	if err != nil {
		return nil, fmt.Errorf("attaching disk %s: %w", cfg.DiskPath, err)
	}
	blockDev, err := vz.NewVirtioBlockDeviceConfiguration(diskAttachment)
	if err != nil {
		return nil, fmt.Errorf("creating block device: %w", err)
	}
	storageDevices := []vz.StorageDeviceConfiguration{blockDev}

	// Cloud-init CIDATA ISO (optional).
	if cfg.CIDATAPath != "" {
		cidataAttachment, err := vz.NewDiskImageStorageDeviceAttachment(cfg.CIDATAPath, true)
		if err != nil {
			return nil, fmt.Errorf("attaching CIDATA ISO %s: %w", cfg.CIDATAPath, err)
		}
		cidataDev, err := vz.NewVirtioBlockDeviceConfiguration(cidataAttachment)
		if err != nil {
			return nil, fmt.Errorf("creating CIDATA block device: %w", err)
		}
		storageDevices = append(storageDevices, cidataDev)
	}
	vzCfg.SetStorageDevicesVirtualMachineConfiguration(storageDevices)

	// Network — NAT.
	natAttachment, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, fmt.Errorf("creating NAT attachment: %w", err)
	}
	netDev, err := vz.NewVirtioNetworkDeviceConfiguration(natAttachment)
	if err != nil {
		return nil, fmt.Errorf("creating network device: %w", err)
	}
	vzCfg.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{netDev})

	// Entropy — for /dev/random in guest.
	entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("creating entropy device: %w", err)
	}
	vzCfg.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})

	// Serial console — log to file.
	if cfg.LogPath != "" {
		serialLog, err := os.Create(cfg.LogPath)
		if err != nil {
			return nil, fmt.Errorf("creating serial log file: %w", err)
		}
		serialAttachment, err := vz.NewFileHandleSerialPortAttachment(os.Stdin, serialLog)
		if err != nil {
			return nil, fmt.Errorf("creating serial port attachment: %w", err)
		}
		serialPort, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serialAttachment)
		if err != nil {
			return nil, fmt.Errorf("creating serial port: %w", err)
		}
		vzCfg.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{serialPort})
	}

	// Shared folders via VirtioFS.
	if len(cfg.Shares) > 0 {
		var fsDirs []vz.DirectorySharingDeviceConfiguration
		for _, share := range cfg.Shares {
			sharedDir, err := vz.NewSharedDirectory(share.HostPath, share.ReadOnly)
			if err != nil {
				return nil, fmt.Errorf("creating shared directory for %s: %w", share.HostPath, err)
			}
			singleShare, err := vz.NewSingleDirectoryShare(sharedDir)
			if err != nil {
				return nil, fmt.Errorf("creating directory share: %w", err)
			}
			fsConfig, err := vz.NewVirtioFileSystemDeviceConfiguration(share.Tag)
			if err != nil {
				return nil, fmt.Errorf("creating VirtioFS device for tag %s: %w", share.Tag, err)
			}
			fsConfig.SetDirectoryShare(singleShare)
			fsDirs = append(fsDirs, fsConfig)
		}
		vzCfg.SetDirectorySharingDevicesVirtualMachineConfiguration(fsDirs)
	}

	return vzCfg, nil
}

func (e *vzEngine) watchState() {
	if e.vm == nil {
		return
	}
	ch := e.vm.StateChangedNotify()
	for state := range ch {
		e.mu.Lock()
		switch state {
		case vz.VirtualMachineStateRunning:
			e.state = StateRunning
		case vz.VirtualMachineStateStopped:
			e.state = StateStopped
		case vz.VirtualMachineStateError:
			e.state = StateError
			slog.Error("VM entered error state", "name", e.cfg.Name)
		}
		e.mu.Unlock()
	}
}
