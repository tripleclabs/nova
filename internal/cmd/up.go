package cmd

import (
	"context"
	"fmt"
	"os"

	pb "github.com/3clabs/nova/pkg/novapb/nova/v1"
	"github.com/spf13/cobra"
)

func newUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up",
		Short: "Create and start a VM from the current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			hcl, err := os.ReadFile(flagConfig)
			if err != nil {
				return fmt.Errorf("reading config %s: %w", flagConfig, err)
			}

			return withDaemon(func(ctx context.Context, client pb.NovaClient) error {
				resp, err := client.Apply(ctx, &pb.ApplyRequest{
					HclConfig: string(hcl),
				})
				if err != nil {
					return fmt.Errorf("apply: %w", err)
				}

				for _, n := range resp.Nodes {
					fmt.Printf("%s: %s (ip: %s)\n", n.Name, n.State, n.Ip)
				}
				return nil
			})
		},
	}
}
