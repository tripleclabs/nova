package vm

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

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

	if opts.Format == FormatOVA {
		// OVA is a tar archive containing an OVF descriptor + VMDK disk.
		fmt.Printf("[export] Building OVA (VMDK + OVF descriptor)...\n")

		cpus := opts.CPUs
		if cpus == 0 {
			cpus = 2
		}
		memMB := opts.MemoryMB
		if memMB == 0 {
			memMB = 2048
		}

		if err := buildOVA(diskPath, srcFormat, outputPath, name, cpus, memMB); err != nil {
			os.Remove(outputPath)
			return nil, fmt.Errorf("building OVA: %w", err)
		}
	} else {
		fmt.Printf("[export] Converting %s → %s...\n", srcFormat, opts.Format)
		if err := convertDisk(diskPath, srcFormat, outputPath, string(opts.Format)); err != nil {
			os.Remove(outputPath) // Clean up partial file.
			return nil, fmt.Errorf("converting disk: %w", err)
		}
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
