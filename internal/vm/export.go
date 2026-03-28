package vm

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/3clabs/nova/internal/sysprep"
)

// ExportFormat represents a supported output disk format.
type ExportFormat string

const (
	FormatQCOW2 ExportFormat = "qcow2"
	FormatRaw   ExportFormat = "raw"
	FormatVMDK  ExportFormat = "vmdk"
	FormatVHDX  ExportFormat = "vhdx"
)

// ValidExportFormats lists all supported export formats.
var ValidExportFormats = []ExportFormat{FormatQCOW2, FormatRaw, FormatVMDK, FormatVHDX}

// ParseExportFormat validates and returns an ExportFormat from a string.
func ParseExportFormat(s string) (ExportFormat, error) {
	switch ExportFormat(strings.ToLower(s)) {
	case FormatQCOW2:
		return FormatQCOW2, nil
	case FormatRaw:
		return FormatRaw, nil
	case FormatVMDK:
		return FormatVMDK, nil
	case FormatVHDX:
		return FormatVHDX, nil
	default:
		return "", fmt.Errorf("unsupported format %q (supported: qcow2, raw, vmdk, vhdx)", s)
	}
}

// ExportExtension returns the conventional file extension for a format.
func (f ExportFormat) ExportExtension() string {
	switch f {
	case FormatRaw:
		return ".img"
	default:
		return "." + string(f)
	}
}

// ExportOptions controls the export pipeline.
type ExportOptions struct {
	// Format is the output disk format.
	Format ExportFormat
	// OutputPath is the destination file. If empty, defaults to ./<name>.<ext>.
	OutputPath string
	// NoClean skips the sysprep step.
	NoClean bool
	// ZeroFreeSpace zeros free space before export for better compression.
	ZeroFreeSpace bool
	// SnapshotName, if set, takes a pre-sysprep snapshot for rollback safety.
	SnapshotName string
	// HasUser indicates a user block was configured. Export refuses to proceed
	// without one to prevent leaking the internal nova user into production images.
	HasUser bool
}

// ExportResult contains information about the completed export.
type ExportResult struct {
	OutputPath string
	Format     ExportFormat
	SizeBytes  int64
}

// Export takes a running VM, optionally snapshots it, syspreps it, shuts it
// down, and flattens its disk into a standalone image in the target format.
func (o *Orchestrator) Export(ctx context.Context, name string, opts ExportOptions) (*ExportResult, error) {
	if name == "" {
		name = "default"
	}

	// Refuse export without a user block — prevents leaking the internal nova user.
	if !opts.HasUser && !opts.NoClean {
		return nil, fmt.Errorf("export requires a user block in nova.hcl — exported images must not contain the internal nova user. Add a user block or use --no-clean to skip sysprep")
	}

	// Validate VM exists and is running.
	machine, err := o.store.Get(name)
	if err != nil {
		return nil, fmt.Errorf("VM %q not found", name)
	}
	if machine.State != "running" {
		return nil, fmt.Errorf("VM %q is not running (state: %s) — export requires a running VM", name, machine.State)
	}

	machineDir := o.store.MachineDir(name)

	// Resolve output path.
	outputPath := opts.OutputPath
	if outputPath == "" {
		outputPath = name + opts.Format.ExportExtension()
	}

	// Check output doesn't already exist.
	if _, err := os.Stat(outputPath); err == nil {
		return nil, fmt.Errorf("output file %q already exists", outputPath)
	}

	// --- Optional: pre-sysprep snapshot for rollback ---
	if opts.SnapshotName != "" {
		fmt.Printf("[export] Taking snapshot %q before sysprep...\n", opts.SnapshotName)
		diskPath := findDisk(machineDir)
		if diskPath == "" {
			return nil, fmt.Errorf("no disk found for VM %q", name)
		}
		if err := qemuImgSnapshot(diskPath, opts.SnapshotName); err != nil {
			return nil, fmt.Errorf("creating pre-sysprep snapshot: %w", err)
		}
		fmt.Printf("[export] Snapshot %q created — use 'nova snapshot restore %s' to roll back.\n", opts.SnapshotName, opts.SnapshotName)
	}

	// --- Sysprep ---
	if !opts.NoClean {
		fmt.Printf("[export] Running sysprep on %q...\n", name)

		keyData, err := os.ReadFile(filepath.Join(machineDir, "ssh", "nova_ed25519"))
		if err != nil {
			return nil, fmt.Errorf("reading SSH key for sysprep: %w", err)
		}

		o.mu.RLock()
		hv := o.hypervisors[name]
		o.mu.RUnlock()
		if hv == nil {
			return nil, fmt.Errorf("no active hypervisor handle for %q", name)
		}

		guestIP, err := hv.GuestIP()
		if err != nil {
			return nil, fmt.Errorf("getting guest IP for sysprep: %w", err)
		}

		sshCfg := sysprep.SSHConfig{
			Host:       guestIP,
			Port:       "22",
			User:       "nova",
			PrivateKey: keyData,
		}

		sysprepOpts := sysprep.Options{
			ZeroFreeSpace:  opts.ZeroFreeSpace,
			RemoveNovaUser: opts.HasUser,
		}

		results, err := sysprep.Run(ctx, sshCfg, sysprepOpts, os.Stdout)
		if err != nil {
			// Report which steps failed but don't necessarily abort — the
			// error from sysprep.Run is informational (counts failures).
			fmt.Printf("[export] Warning: %v\n", err)
			fmt.Printf("[export] Sysprep completed with errors. Proceeding with export.\n")
		}
		_ = results
	}

	// --- Graceful shutdown ---
	fmt.Printf("[export] Shutting down %q...\n", name)
	if err := o.Down(name); err != nil {
		return nil, fmt.Errorf("shutting down VM: %w", err)
	}

	// --- Flatten disk ---
	diskPath := findDisk(machineDir)
	if diskPath == "" {
		return nil, fmt.Errorf("no disk found for VM %q after shutdown", name)
	}

	// Detect the source format for qemu-img.
	srcFormat := "qcow2"
	if strings.HasSuffix(diskPath, ".raw") {
		srcFormat = "raw"
	}

	fmt.Printf("[export] Converting %s → %s...\n", srcFormat, opts.Format)
	if err := convertDisk(diskPath, srcFormat, outputPath, string(opts.Format)); err != nil {
		os.Remove(outputPath) // Clean up partial file.
		return nil, fmt.Errorf("converting disk: %w", err)
	}

	// Get output file size.
	fi, err := os.Stat(outputPath)
	if err != nil {
		return nil, fmt.Errorf("stat output: %w", err)
	}

	result := &ExportResult{
		OutputPath: outputPath,
		Format:     opts.Format,
		SizeBytes:  fi.Size(),
	}

	fmt.Printf("[export] Done: %s (%s, %s)\n", result.OutputPath, result.Format, humanSize(result.SizeBytes))
	return result, nil
}

// findDisk returns the path to the VM's disk image (qcow2 or raw).
func findDisk(machineDir string) string {
	// Prefer qcow2 (Linux), fall back to raw (macOS VZ).
	for _, name := range []string{"disk.qcow2", "disk.raw"} {
		p := filepath.Join(machineDir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// convertDisk uses qemu-img convert to flatten an overlay into a standalone image.
func convertDisk(src, srcFormat, dst, dstFormat string) error {
	cmd := exec.Command("qemu-img", "convert",
		"-f", srcFormat,
		"-O", dstFormat,
		src,
		dst,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("qemu-img convert: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

// qemuImgSnapshot creates an internal qcow2 snapshot.
func qemuImgSnapshot(diskPath, snapName string) error {
	cmd := exec.Command("qemu-img", "snapshot", "-c", snapName, diskPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("qemu-img snapshot: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
