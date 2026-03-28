package cmd

import (
	"context"
	"fmt"

	pb "github.com/3clabs/nova/pkg/novapb/nova/v1"
	"github.com/spf13/cobra"
)

var (
	exportFormat   string
	exportOutput   string
	exportNoClean  bool
	exportZero     bool
	exportSnapshot string
)

func newExportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export [name]",
		Short: "Export a running VM as a standalone disk image",
		Long: `Syspreps, shuts down, and exports a running VM's disk as a standalone
image suitable for use on hypervisors (KVM, VMware, Hyper-V).

Supported formats: qcow2 (default), raw, vmdk, vhdx

The export pipeline:
  1. Run sysprep to remove machine-specific state (unless --no-clean)
  2. Gracefully shut down the VM
  3. Flatten the CoW overlay into a standalone image
  4. Convert to the target format

Examples:
  nova export                                    # Export "default" VM as qcow2
  nova export myvm --format vmdk -o golden.vmdk  # Export as VMware image
  nova export myvm --snapshot pre-export         # Snapshot before sysprep for safety
  nova export myvm --no-clean                    # Skip sysprep (debug/custom cleanup)`,
		RunE: runExport,
	}

	cmd.Flags().StringVar(&exportFormat, "format", "qcow2", "output format: qcow2, raw, vmdk, vhdx")
	cmd.Flags().StringVarP(&exportOutput, "output", "o", "", "output file path (default: ./<name>.<format>)")
	cmd.Flags().BoolVar(&exportNoClean, "no-clean", false, "skip sysprep (image hygiene step)")
	cmd.Flags().BoolVar(&exportZero, "zero", false, "zero free space before export (better compression, slower)")
	cmd.Flags().StringVar(&exportSnapshot, "snapshot", "", "take a pre-sysprep snapshot for rollback safety")

	return cmd
}

func runExport(cmd *cobra.Command, args []string) error {
	name := "default"
	if len(args) > 0 {
		name = args[0]
	}

	return withDaemon(func(ctx context.Context, client pb.NovaClient) error {
		resp, err := client.Export(ctx, &pb.ExportRequest{
			Name:          name,
			Format:        exportFormat,
			OutputPath:    exportOutput,
			NoClean:       exportNoClean,
			ZeroFreeSpace: exportZero,
			SnapshotName:  exportSnapshot,
		})
		if err != nil {
			return fmt.Errorf("export: %w", err)
		}

		fmt.Printf("Exported: %s (%s, %s)\n", resp.OutputPath, resp.Format, humanBytes(resp.SizeBytes))
		return nil
	})
}
