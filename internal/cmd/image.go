package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/tripleclabs/nova/internal/distro"
	"github.com/tripleclabs/nova/internal/image"
	"github.com/spf13/cobra"
)

func newImageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Manage VM disk images",
	}
	cmd.AddCommand(
		newImageGetCmd(),
		newImageBuildCmd(),
		newImageListCmd(),
		newImageRmCmd(),
	)
	return cmd
}

func newImageGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <distro:version>",
		Short: "Download a known distro image into the nova cache",
		Long: `Downloads an official cloud image for a known Linux distribution and caches
it locally so 'nova up' can use it immediately.

Known distros: ubuntu:24.04, ubuntu:22.04, alpine:3.21, alpine:3.20

You can also just reference the distro directly in nova.hcl and nova will
pull it automatically on 'nova up':

  vm {
    image = "ubuntu:24.04"
  }`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			shorthand := args[0]
			if _, ok := distro.Lookup(shorthand); !ok {
				return fmt.Errorf("unknown distro %q — run 'nova image list' to see cached images or 'nova image build' for custom images", shorthand)
			}

			mgr, err := newImageManager()
			if err != nil {
				return err
			}

			fmt.Printf("Fetching %s...\n", shorthand)
			var lastPct float64
			_, err = mgr.Pull(context.Background(), shorthand, func(complete, total int64) {
				if total <= 0 {
					return
				}
				pct := float64(complete) / float64(total) * 100
				if pct-lastPct >= 1 {
					fmt.Printf("\r  %.0f%%", pct)
					lastPct = pct
				}
			})
			if err != nil {
				return err
			}
			fmt.Printf("\r  done   \n")

			ci := mgr.ResolveImage(shorthand)
			if ci != nil {
				fmt.Printf("Cached %s\n", ci.Ref)
				fmt.Printf("  digest  %s\n", ci.Digest[:12])
				fmt.Printf("  size    %s\n", humanBytes(ci.Size))
			}
			return nil
		},
	}
}

func newImageBuildCmd() *cobra.Command {
	var tag    string
	var push   bool
	var osName string

	cmd := &cobra.Command{
		Use:   "build <file>",
		Short: "Package a local disk image into the nova image cache",
		Long: `Wraps a local qcow2 or raw disk image as an OCI image and seeds it into
the nova cache so that 'nova up' can use it immediately without pulling from
a registry. Pass --push to also upload the image to the remote registry.

Use --os to tag the image with its OS family so nova can apply the right
cloud-init defaults (shell, sudo/doas, etc.):

  nova image build myimage.qcow2 --tag nova.local/ubuntu:24.04 --os ubuntu`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := newImageManager()
			if err != nil {
				return err
			}
			fmt.Printf("Building image from %s...\n", args[0])
			ci, err := mgr.Build(cmd.Context(), args[0], tag, osName, push)
			if err != nil {
				return err
			}
			fmt.Printf("Built %s\n", ci.Ref)
			fmt.Printf("  digest  %s\n", ci.Digest[:12])
			fmt.Printf("  size    %s\n", humanBytes(ci.Size))
			fmt.Printf("  cached  %s\n", ci.DiskPath)
			if push {
				fmt.Printf("  pushed  %s\n", ci.Ref)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&tag, "tag", "t", "", "image reference (e.g. ghcr.io/org/myimage:latest)")
	cmd.MarkFlagRequired("tag")
	cmd.Flags().BoolVar(&push, "push", false, "push image to registry after building")
	cmd.Flags().StringVar(&osName, "os", "", "OS family for cloud-init profile (e.g. ubuntu, alpine)")
	return cmd
}

func newImageListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List all cached VM disk images",
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := newImageManager()
			if err != nil {
				return err
			}
			images, err := mgr.List()
			if err != nil {
				return err
			}
			if len(images) == 0 {
				fmt.Println("No cached images. Run 'nova image build' or 'nova up' to add one.")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "REF\tDIGEST\tSIZE\tPULLED")
			for _, ci := range images {
				short := ci.Digest
				if len(short) > 12 {
					short = short[:12]
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
					ci.Ref,
					short,
					humanBytes(ci.Size),
					ci.PulledAt.Format("2006-01-02 15:04"),
				)
			}
			return tw.Flush()
		},
	}
}

func newImageRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <ref>",
		Short: "Remove a cached image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := newImageManager()
			if err != nil {
				return err
			}
			if err := mgr.Delete(args[0]); err != nil {
				return err
			}
			fmt.Printf("Removed %s\n", args[0])
			return nil
		},
	}
}

func newImageManager() (*image.Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting home dir: %w", err)
	}
	return image.NewManager(filepath.Join(home, ".nova", "cache", "images"))
}

// humanBytes formats a byte count as a human-readable string.
func humanBytes(b int64) string {
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
