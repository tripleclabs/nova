package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/protobuf/types/known/emptypb"
	pb "github.com/tripleclabs/nova/pkg/novapb/nova/v1"
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

Supported formats: qcow2 (default), raw, vmdk, vhdx, ova

The export pipeline:
  1. Run sysprep to remove machine-specific state (unless --no-clean)
  2. Gracefully shut down the VM
  3. Flatten the CoW overlay into a standalone image
  4. Convert to the target format

Sysprep always removes the internal nova user. If nova.hcl has no user block,
the exported image will have no login user — this is fine for nova base images
(cloud-init recreates users on next 'nova up'), but for images used on other
hypervisors, add a user block to nova.hcl first.

Examples:
  nova export                                    # Export "default" VM as qcow2
  nova export myvm --format vmdk -o golden.vmdk  # Export as VMware image
  nova export myvm --snapshot pre-export         # Snapshot before sysprep for safety
  nova export myvm --no-clean                    # Skip sysprep (debug/custom cleanup)`,
		RunE: runExport,
	}

	cmd.Flags().StringVar(&exportFormat, "format", "qcow2", "output format: qcow2, raw, vmdk, vhdx, ova")
	cmd.Flags().StringVarP(&exportOutput, "output", "o", "", "output file path (default: ./<name>.<format>)")
	cmd.Flags().BoolVar(&exportNoClean, "no-clean", false, "skip sysprep (image hygiene step)")
	cmd.Flags().BoolVar(&exportZero, "zero", false, "zero free space before export (better compression, slower)")
	cmd.Flags().StringVar(&exportSnapshot, "snapshot", "", "take a pre-sysprep snapshot for rollback safety")

	return cmd
}

func runExport(cmd *cobra.Command, args []string) error {
	return withDaemon(func(ctx context.Context, client pb.NovaClient) error {
		name := ""
		if len(args) > 0 {
			name = args[0]
		} else {
			var err error
			if name, err = resolveVMName(ctx, client); err != nil {
				return err
			}
		}

		// Resolve the output path against the client's cwd — the daemon runs in
		// its own working directory, so relative paths would otherwise land
		// wherever the daemon was launched from, not where the user ran `nova export`.
		outputPath := exportOutput
		if outputPath == "" {
			ext := "." + exportFormat
			if exportFormat == "ova" {
				ext = ".ova"
			}
			outputPath = name + ext
		}
		if !filepath.IsAbs(outputPath) {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolving output path: %w", err)
			}
			outputPath = filepath.Join(cwd, outputPath)
		}

		// Subscribe to events BEFORE calling Export so no progress is missed.
		evtCtx, evtCancel := context.WithCancel(ctx)
		defer evtCancel()

		stream, err := client.StreamEvents(evtCtx, &emptypb.Empty{})
		if err != nil {
			return fmt.Errorf("streaming events: %w", err)
		}

		// Forward export log events to stdout in a background goroutine.
		evtDone := make(chan struct{})
		go func() {
			defer close(evtDone)
			for {
				evt, err := stream.Recv()
				if err != nil {
					return
				}
				if evt.Type == "log" && (evt.Node == name || evt.Node == "default") {
					fmt.Printf("[export] %s\n", evt.Detail)
				}
			}
		}()

		resp, exportErr := client.Export(ctx, &pb.ExportRequest{
			Name:          name,
			Format:        exportFormat,
			OutputPath:    outputPath,
			NoClean:       exportNoClean,
			ZeroFreeSpace: exportZero,
			SnapshotName:  exportSnapshot,
		})

		// Cancel the event stream and wait for the goroutine to drain.
		evtCancel()
		<-evtDone

		if exportErr != nil {
			return fmt.Errorf("export: %w", exportErr)
		}

		fmt.Printf("Exported: %s (%s, %s)\n", resp.OutputPath, resp.Format, humanBytes(resp.SizeBytes))
		return nil
	})
}
