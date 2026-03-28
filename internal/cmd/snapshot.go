package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/3clabs/nova/internal/image"
	"github.com/3clabs/nova/internal/snapshot"
	"github.com/3clabs/nova/internal/state"
	"github.com/spf13/cobra"
)

func newSnapshotCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Manage cluster snapshots (time travel)",
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "save <name>",
			Short: "Save a snapshot of all running machines",
			Args:  cobra.ExactArgs(1),
			RunE:  runSnapshotSave,
		},
		&cobra.Command{
			Use:   "restore <name>",
			Short: "Restore machines to a saved snapshot",
			Args:  cobra.ExactArgs(1),
			RunE:  runSnapshotRestore,
		},
		&cobra.Command{
			Use:   "list",
			Short: "List all saved snapshots",
			RunE:  runSnapshotList,
		},
		&cobra.Command{
			Use:   "delete <name>",
			Short: "Delete a saved snapshot",
			Args:  cobra.ExactArgs(1),
			RunE:  runSnapshotDelete,
		},
		newSnapshotExportCmd(),
		newSnapshotImportCmd(),
		&cobra.Command{
			Use:   "push <name> <ref>",
			Short: "Push a snapshot to an OCI registry",
			Args:  cobra.ExactArgs(2),
			RunE:  runSnapshotPush,
		},
		&cobra.Command{
			Use:   "pull <ref>",
			Short: "Pull a snapshot from an OCI registry",
			Args:  cobra.ExactArgs(1),
			RunE:  runSnapshotPull,
		},
	)

	return cmd
}

func snapManager() (*snapshot.Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	novaDir := filepath.Join(home, ".nova")
	store, err := state.NewStore(novaDir)
	if err != nil {
		return nil, err
	}
	return snapshot.NewManager(store, novaDir)
}

func runSnapshotSave(cmd *cobra.Command, args []string) error {
	mgr, err := snapManager()
	if err != nil {
		return err
	}
	if err := mgr.Save(args[0]); err != nil {
		return err
	}
	fmt.Printf("Snapshot %q saved.\n", args[0])
	return nil
}

func runSnapshotRestore(cmd *cobra.Command, args []string) error {
	mgr, err := snapManager()
	if err != nil {
		return err
	}
	if err := mgr.Restore(args[0]); err != nil {
		return err
	}
	fmt.Printf("Snapshot %q restored.\n", args[0])
	return nil
}

func runSnapshotList(cmd *cobra.Command, args []string) error {
	mgr, err := snapManager()
	if err != nil {
		return err
	}
	snaps, err := mgr.List()
	if err != nil {
		return err
	}
	if len(snaps) == 0 {
		fmt.Println("No snapshots. Use 'nova snapshot save <name>' to create one.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tMACHINES\tCREATED")
	for _, s := range snaps {
		fmt.Fprintf(tw, "%s\t%d\t%s\n", s.Name, len(s.Machines), s.CreatedAt.Format("2006-01-02 15:04:05"))
	}
	return tw.Flush()
}

func runSnapshotDelete(cmd *cobra.Command, args []string) error {
	mgr, err := snapManager()
	if err != nil {
		return err
	}
	if err := mgr.Delete(args[0]); err != nil {
		return err
	}
	fmt.Printf("Snapshot %q deleted.\n", args[0])
	return nil
}

func runSnapshotPush(cmd *cobra.Command, args []string) error {
	name, ref := args[0], args[1]

	mgr, err := snapManager()
	if err != nil {
		return err
	}

	fmt.Printf("Packing snapshot %q...\n", name)
	packDir, err := mgr.Pack(name)
	if err != nil {
		return err
	}
	defer os.RemoveAll(packDir)

	home, _ := os.UserHomeDir()
	imgMgr, err := image.NewManager(filepath.Join(home, ".nova", "cache", "images"))
	if err != nil {
		return err
	}

	fmt.Printf("Pushing to %s...\n", ref)
	if err := imgMgr.PushDir(context.Background(), packDir, ref); err != nil {
		return err
	}

	fmt.Printf("Snapshot %q pushed to %s\n", name, ref)
	return nil
}

func runSnapshotPull(cmd *cobra.Command, args []string) error {
	ref := args[0]

	home, _ := os.UserHomeDir()
	imgMgr, err := image.NewManager(filepath.Join(home, ".nova", "cache", "images"))
	if err != nil {
		return err
	}

	fmt.Printf("Pulling snapshot from %s...\n", ref)
	pullDir, err := imgMgr.PullDir(context.Background(), ref)
	if err != nil {
		return err
	}
	defer os.RemoveAll(pullDir)

	mgr, err := snapManager()
	if err != nil {
		return err
	}

	if err := mgr.Unpack(pullDir); err != nil {
		return err
	}

	fmt.Printf("Snapshot pulled and imported from %s\n", ref)
	return nil
}

var snapExportOutput string

func newSnapshotExportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export <name>",
		Short: "Export a snapshot as a portable archive file",
		Long: `Packs a saved snapshot into a single .novasnap file that can be
transferred to another machine and imported with 'nova snapshot import'.

This is useful for sharing VM environments without needing a registry.

Example:
  nova snapshot save my-env
  nova snapshot export my-env -o my-env.novasnap
  # scp my-env.novasnap to colleague
  # colleague runs: nova snapshot import my-env.novasnap`,
		Args: cobra.ExactArgs(1),
		RunE: runSnapshotExport,
	}
	cmd.Flags().StringVarP(&snapExportOutput, "output", "o", "", "output file path (default: ./<name>.novasnap)")
	return cmd
}

func newSnapshotImportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "import <file>",
		Short: "Import a snapshot from a portable archive file",
		Long: `Imports a .novasnap archive produced by 'nova snapshot export'.
After importing, use 'nova snapshot restore <name>' to load the VMs,
then 'nova up' to boot them.`,
		Args: cobra.ExactArgs(1),
		RunE: runSnapshotImport,
	}
}

func runSnapshotExport(cmd *cobra.Command, args []string) error {
	name := args[0]

	mgr, err := snapManager()
	if err != nil {
		return err
	}

	outputPath := snapExportOutput
	if outputPath == "" {
		outputPath = name + ".novasnap"
	}

	fmt.Printf("Exporting snapshot %q to %s...\n", name, outputPath)
	if err := mgr.Export(name, outputPath); err != nil {
		return err
	}

	fi, err := os.Stat(outputPath)
	if err != nil {
		return err
	}
	fmt.Printf("Exported %s (%s)\n", outputPath, humanBytes(fi.Size()))
	return nil
}

func runSnapshotImport(cmd *cobra.Command, args []string) error {
	archivePath := args[0]

	mgr, err := snapManager()
	if err != nil {
		return err
	}

	fmt.Printf("Importing snapshot from %s...\n", archivePath)
	if err := mgr.Import(archivePath); err != nil {
		return err
	}

	fmt.Printf("Snapshot imported. Use 'nova snapshot list' to see it, then 'nova snapshot restore <name>' + 'nova up'.\n")
	return nil
}
