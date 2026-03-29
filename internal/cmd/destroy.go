package cmd

import (
	"context"
	"fmt"

	pb "github.com/tripleclabs/nova/pkg/novapb/nova/v1"
	"github.com/spf13/cobra"
)

func newDestroyCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "destroy [name]",
		Short:   "Force kill a VM and delete all its data",
		Aliases: []string{"nuke"},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) > 0 {
				name = args[0]
			}

			return withDaemon(func(ctx context.Context, client pb.NovaClient) error {
				if _, err := client.Destroy(ctx, &pb.DestroyRequest{Name: name, Force: true}); err != nil {
					return err
				}
				if name == "" {
					fmt.Println("All VMs destroyed.")
				} else {
					fmt.Printf("VM %q destroyed.\n", name)
				}
				return nil
			})
		},
	}
}
