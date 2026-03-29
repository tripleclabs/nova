package cmd

import (
	"context"
	"fmt"

	pb "github.com/tripleclabs/nova/pkg/novapb/nova/v1"
	"github.com/spf13/cobra"
)

func newDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down [name]",
		Short: "Gracefully stop a running VM",
		RunE: func(cmd *cobra.Command, args []string) error {
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
				if _, err := client.NodeStop(ctx, &pb.NodeRequest{Name: name}); err != nil {
					return err
				}
				fmt.Printf("VM %q stopped.\n", name)
				return nil
			})
		},
	}
}
