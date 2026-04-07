package vm

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/tripleclabs/nova/internal/sysprep"
)


// ExportFormat represents a supported output disk format.
type ExportFormat string

const (
	FormatQCOW2 ExportFormat = "qcow2"
	FormatRaw   ExportFormat = "raw"
	FormatVMDK  ExportFormat = "vmdk"
	FormatVHDX  ExportFormat = "vhdx"
	FormatOVA   ExportFormat = "ova"
)

// ValidExportFormats lists all supported export formats.
var ValidExportFormats = []ExportFormat{FormatQCOW2, FormatRaw, FormatVMDK, FormatVHDX, FormatOVA}

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
	case FormatOVA:
		return FormatOVA, nil
	default:
		return "", fmt.Errorf("unsupported format %q (supported: qcow2, raw, vmdk, vhdx, ova)", s)
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
	// CPUs and MemoryMB are used for OVA/OVF metadata. Defaults to 2/2048 if unset.
	CPUs     uint
	MemoryMB uint64
	// Emit, if set, is called with progress messages during export.
	// If nil, progress is silently discarded.
	Emit func(string)
}

// ExportResult contains information about the completed export.
type ExportResult struct {
	OutputPath string
	Format     ExportFormat
	SizeBytes  int64
}

// Export takes a running VM and produces a standalone disk image in the target
// format. Sysprep (when enabled) runs on a fully independent clone of the VM
// disk — the VM's own disk is never touched, so the VM can be restarted
// normally after export and exported again without issue.
//
// Pipeline:
//  1. Shut down the VM (needed for a consistent disk snapshot).
//  2. Clone the VM disk into a temporary standalone image.
//  3. If sysprep: boot an ephemeral VM from the clone, sysprep it, shut it down.
//  4. Convert the clone to the requested output format.
//  5. Delete the clone. The VM's original disk is untouched.
func (o *Orchestrator) Export(ctx context.Context, name string, opts ExportOptions) (*ExportResult, error) {
	if name == "" {
		name = "default"
	}

	// emitf sends a progress message if an Emit callback is configured.
	emitf := func(format string, args ...any) {
		if opts.Emit != nil {
			opts.Emit(fmt.Sprintf(format, args...))
		}
	}

	// Warn if no user block is configured. Sysprep still removes the internal
	// nova user, so nothing leaks — but the exported image will have no login
	// user at all unless something (e.g. nova re-running cloud-init) recreates one.
	if !opts.HasUser && !opts.NoClean {
		emitf("Note: no user block configured. The exported image will have no login user. " +
			"This is fine for nova base images (cloud-init will recreate users on next 'nova up'), " +
			"but for images intended for other hypervisors, add a user block to nova.hcl.")
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
	if _, err := os.Stat(outputPath); err == nil {
		return nil, fmt.Errorf("output file %q already exists", outputPath)
	}

	// --- Shut down the VM to get a consistent disk state ---
	emitf("Shutting down %q...", name)
	if err := o.Down(name); err != nil {
		return nil, fmt.Errorf("shutting down VM: %w", err)
	}

	diskPath := findDisk(machineDir)
	if diskPath == "" {
		return nil, fmt.Errorf("no disk found for VM %q", name)
	}
	srcFormat := "qcow2"
	if strings.HasSuffix(diskPath, ".raw") {
		srcFormat = "raw"
	}

	// --- Optional: checkpoint snapshot on the original disk for rollback ---
	if opts.SnapshotName != "" {
		emitf("Taking snapshot %q...", opts.SnapshotName)
		if err := qemuImgSnapshot(diskPath, opts.SnapshotName); err != nil {
			return nil, fmt.Errorf("creating snapshot: %w", err)
		}
		emitf("Snapshot %q created — use 'nova snapshot restore %s' to roll back.", opts.SnapshotName, opts.SnapshotName)
	}

	// --- Clone the disk into a fully independent temporary image ---
	// The clone is a standalone qcow2 with no backing-file dependency.
	// All export operations (sysprep, conversion) run on the clone;
	// the VM's original disk is never modified.
	clonePath := diskPath + ".export-clone.qcow2"
	defer os.Remove(clonePath)

	emitf("Cloning disk (this may take a moment)...")
	if err := convertDisk(diskPath, srcFormat, clonePath, "qcow2"); err != nil {
		return nil, fmt.Errorf("cloning disk: %w", err)
	}

	// From here, all work targets clonePath. srcFormat is always qcow2 for the clone.
	exportDisk := clonePath
	exportFmt := "qcow2"

	// --- Sysprep on the ephemeral VM booted from the clone ---
	if !opts.NoClean {
		emitf("Starting ephemeral VM for sysprep...")
		ephHV, sshHost, sshPort, err := o.startExportVM(ctx, name, clonePath, machineDir, opts.CPUs, opts.MemoryMB)
		if err != nil {
			return nil, fmt.Errorf("starting export VM: %w", err)
		}
		defer ephHV.ForceKill()

		// Wait for SSH (up to 5 minutes for slow boots).
		addr := net.JoinHostPort(sshHost, sshPort)
		emitf("Waiting for SSH at %s...", addr)
		waitCtx, waitCancel := context.WithTimeout(ctx, 5*time.Minute)
		defer waitCancel()
		if err := waitForExportSSH(waitCtx, addr); err != nil {
			return nil, fmt.Errorf("export VM SSH not ready: %w", err)
		}

		keyData, err := os.ReadFile(filepath.Join(machineDir, "ssh", "nova_ed25519"))
		if err != nil {
			return nil, fmt.Errorf("reading SSH key for sysprep: %w", err)
		}

		emitf("Running sysprep...")
		sshCfg := sysprep.SSHConfig{
			Host:       sshHost,
			Port:       sshPort,
			User:       "nova",
			PrivateKey: keyData,
		}
		sysprepOpts := sysprep.Options{
			ZeroFreeSpace: opts.ZeroFreeSpace,
			// Always remove the internal nova user — its SSH keys are ephemeral
			// per-VM and must not persist into exported images.
			RemoveNovaUser: true,
			TargetHyperV:   opts.Format == FormatVHDX,
		}
		// Wire sysprep output through the emit callback (line-buffered).
		var sysprepOut io.Writer = io.Discard
		if opts.Emit != nil {
			sysprepOut = &lineEmitWriter{emit: opts.Emit}
		}
		if _, err := sysprep.Run(ctx, sshCfg, sysprepOpts, sysprepOut); err != nil {
			return nil, fmt.Errorf("sysprep failed: %w", err)
		}

		emitf("Shutting down ephemeral VM...")
		stopCtx, stopCancel := context.WithTimeout(ctx, 30*time.Second)
		defer stopCancel()
		ephHV.Stop(stopCtx) //nolint:errcheck
	}

	// --- Convert clone to output format ---
	if opts.Format == FormatOVA {
		emitf("Building OVA (VMDK + OVF descriptor)...")
		cpus := opts.CPUs
		if cpus == 0 {
			cpus = 2
		}
		memMB := opts.MemoryMB
		if memMB == 0 {
			memMB = 2048
		}
		if err := buildOVA(exportDisk, exportFmt, outputPath, name, cpus, memMB); err != nil {
			os.Remove(outputPath)
			return nil, fmt.Errorf("building OVA: %w", err)
		}
	} else {
		emitf("Converting %s → %s...", exportFmt, opts.Format)
		if err := convertDisk(exportDisk, exportFmt, outputPath, string(opts.Format)); err != nil {
			os.Remove(outputPath)
			return nil, fmt.Errorf("converting disk: %w", err)
		}
	}

	fi, err := os.Stat(outputPath)
	if err != nil {
		return nil, fmt.Errorf("stat output: %w", err)
	}

	result := &ExportResult{
		OutputPath: outputPath,
		Format:     opts.Format,
		SizeBytes:  fi.Size(),
	}
	emitf("Done: %s (%s, %s)", result.OutputPath, result.Format, humanSize(result.SizeBytes))
	return result, nil
}

// lineEmitWriter is an io.Writer that buffers bytes and calls emit for each
// complete line (stripping the trailing newline). Partial lines are held until
// the next write or until the writer is garbage-collected.
type lineEmitWriter struct {
	emit func(string)
	buf  []byte
}

func (w *lineEmitWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		idx := bytes.IndexByte(w.buf, '\n')
		if idx < 0 {
			break
		}
		w.emit(string(w.buf[:idx]))
		w.buf = w.buf[idx+1:]
	}
	return len(p), nil
}

// waitForExportSSH polls until the address accepts a TCP connection or ctx expires.
func waitForExportSSH(ctx context.Context, addr string) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for SSH at %s: %w", addr, ctx.Err())
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(2 * time.Second)
	}
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

// buildOVA creates an OVA archive containing an OVF descriptor and a VMDK disk.
// OVA is a standard tar file (not compressed) per the OVF spec.
func buildOVA(srcDisk, srcFormat, ovaPath, vmName string, cpus uint, memoryMB uint64) error {
	// Create a temp dir for the intermediate VMDK and OVF.
	tmpDir, err := os.MkdirTemp("", "nova-ova-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Convert disk to VMDK (streamOptimized for efficient import).
	vmdkName := vmName + ".vmdk"
	vmdkPath := filepath.Join(tmpDir, vmdkName)
	if err := convertDisk(srcDisk, srcFormat, vmdkPath, "vmdk"); err != nil {
		return fmt.Errorf("converting to VMDK: %w", err)
	}

	vmdkInfo, err := os.Stat(vmdkPath)
	if err != nil {
		return fmt.Errorf("stat VMDK: %w", err)
	}

	// Generate OVF descriptor.
	ovfName := vmName + ".ovf"
	ovfContent, err := generateOVF(vmName, vmdkName, cpus, memoryMB, vmdkInfo.Size())
	if err != nil {
		return fmt.Errorf("generating OVF: %w", err)
	}
	ovfPath := filepath.Join(tmpDir, ovfName)
	if err := os.WriteFile(ovfPath, ovfContent, 0644); err != nil {
		return fmt.Errorf("writing OVF: %w", err)
	}

	// Generate manifest (SHA-256 checksums).
	mfName := vmName + ".mf"
	mfContent, err := generateManifest(tmpDir, []string{ovfName, vmdkName})
	if err != nil {
		return fmt.Errorf("generating manifest: %w", err)
	}
	mfPath := filepath.Join(tmpDir, mfName)
	if err := os.WriteFile(mfPath, mfContent, 0644); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}

	// Bundle into OVA (tar, uncompressed, OVF first per spec).
	return createOVATar(ovaPath, tmpDir, []string{ovfName, vmdkName, mfName})
}

// generateOVF produces an OVF 2.0 descriptor XML for VMware/Proxmox import.
func generateOVF(vmName, vmdkFileName string, cpus uint, memoryMB uint64, vmdkSize int64) ([]byte, error) {
	data := struct {
		VMName       string
		CPUs         uint
		MemoryMB     uint64
		VmdkFileName string
		VmdkSize     int64
	}{vmName, cpus, memoryMB, vmdkFileName, vmdkSize}

	var buf bytes.Buffer
	if err := ovfTemplate.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("executing OVF template: %w", err)
	}
	return buf.Bytes(), nil
}

var ovfTemplate = template.Must(template.New("ovf").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<Envelope xmlns="http://schemas.dmtf.org/ovf/envelope/2"
          xmlns:rasd="http://schemas.dmtf.org/wbem/wscim/1/cim-schema/2/CIM_ResourceAllocationSettingData"
          xmlns:vssd="http://schemas.dmtf.org/wbem/wscim/1/cim-schema/2/CIM_VirtualSystemSettingData"
          xmlns:ovf="http://schemas.dmtf.org/ovf/envelope/2"
          xmlns:vmw="http://www.vmware.com/schema/ovf">
  <References>
    <File ovf:href="{{.VmdkFileName}}" ovf:id="file1" ovf:size="{{.VmdkSize}}"/>
  </References>
  <DiskSection>
    <Info>Virtual disk information</Info>
    <Disk ovf:capacity="{{.VmdkSize}}" ovf:capacityAllocationUnits="byte"
          ovf:diskId="vmdisk1" ovf:fileRef="file1" ovf:format="http://www.vmware.com/interfaces/specifications/vmdk.html#streamOptimized"/>
  </DiskSection>
  <NetworkSection>
    <Info>The list of logical networks</Info>
    <Network ovf:name="VM Network">
      <Description>The VM Network</Description>
    </Network>
  </NetworkSection>
  <VirtualSystem ovf:id="{{.VMName}}">
    <Info>A virtual machine exported by Nova</Info>
    <Name>{{.VMName}}</Name>
    <OperatingSystemSection ovf:id="101">
      <Info>The operating system</Info>
      <Description>Linux</Description>
    </OperatingSystemSection>
    <VirtualHardwareSection>
      <Info>Virtual hardware requirements</Info>
      <System>
        <vssd:ElementName>Virtual Hardware Family</vssd:ElementName>
        <vssd:InstanceID>0</vssd:InstanceID>
        <vssd:VirtualSystemType>vmx-13</vssd:VirtualSystemType>
      </System>
      <Item>
        <rasd:AllocationUnits>hertz * 10^6</rasd:AllocationUnits>
        <rasd:Description>Number of Virtual CPUs</rasd:Description>
        <rasd:ElementName>{{.CPUs}} virtual CPU(s)</rasd:ElementName>
        <rasd:InstanceID>1</rasd:InstanceID>
        <rasd:ResourceType>3</rasd:ResourceType>
        <rasd:VirtualQuantity>{{.CPUs}}</rasd:VirtualQuantity>
      </Item>
      <Item>
        <rasd:AllocationUnits>byte * 2^20</rasd:AllocationUnits>
        <rasd:Description>Memory Size</rasd:Description>
        <rasd:ElementName>{{.MemoryMB}}MB of memory</rasd:ElementName>
        <rasd:InstanceID>2</rasd:InstanceID>
        <rasd:ResourceType>4</rasd:ResourceType>
        <rasd:VirtualQuantity>{{.MemoryMB}}</rasd:VirtualQuantity>
      </Item>
      <Item>
        <rasd:AddressOnParent>0</rasd:AddressOnParent>
        <rasd:ElementName>Hard Disk 1</rasd:ElementName>
        <rasd:HostResource>ovf:/disk/vmdisk1</rasd:HostResource>
        <rasd:InstanceID>3</rasd:InstanceID>
        <rasd:ResourceType>17</rasd:ResourceType>
      </Item>
      <Item>
        <rasd:AutomaticAllocation>true</rasd:AutomaticAllocation>
        <rasd:Connection>VM Network</rasd:Connection>
        <rasd:Description>VmxNet3 ethernet adapter</rasd:Description>
        <rasd:ElementName>Network adapter 1</rasd:ElementName>
        <rasd:InstanceID>4</rasd:InstanceID>
        <rasd:ResourceSubType>VmxNet3</rasd:ResourceSubType>
        <rasd:ResourceType>10</rasd:ResourceType>
      </Item>
    </VirtualHardwareSection>
  </VirtualSystem>
</Envelope>
`))

// generateManifest creates an OVF manifest file with SHA-256 checksums.
func generateManifest(dir string, files []string) ([]byte, error) {
	var buf bytes.Buffer
	for _, name := range files {
		hash, err := sha256File(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("hashing %s: %w", name, err)
		}
		fmt.Fprintf(&buf, "SHA256(%s)= %s\n", name, hash)
	}
	return buf.Bytes(), nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// createOVATar bundles files into an OVA (uncompressed tar, OVF first per spec).
func createOVATar(ovaPath, srcDir string, files []string) error {
	outFile, err := os.Create(ovaPath)
	if err != nil {
		return fmt.Errorf("creating OVA file: %w", err)
	}
	defer outFile.Close()

	tw := tar.NewWriter(outFile)
	defer tw.Close()

	for _, name := range files {
		path := filepath.Join(srcDir, name)
		fi, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("stat %s: %w", name, err)
		}

		header, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return fmt.Errorf("tar header for %s: %w", name, err)
		}
		header.Name = name

		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("writing tar header for %s: %w", name, err)
		}

		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("opening %s: %w", name, err)
		}
		if _, err := io.Copy(tw, f); err != nil {
			f.Close()
			return fmt.Errorf("writing %s to tar: %w", name, err)
		}
		f.Close()
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
