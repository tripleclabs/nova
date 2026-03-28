package cmd

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	pb "github.com/3clabs/nova/pkg/novapb/nova/v1"
	"google.golang.org/protobuf/types/known/emptypb"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the status of all managed VMs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withDaemon(func(ctx context.Context, client pb.NovaClient) error {
				resp, err := client.Status(ctx, &emptypb.Empty{})
				if err != nil {
					return err
				}

				if len(resp.Nodes) == 0 {
					fmt.Println("No VMs found. Run 'nova up' to create one.")
					return nil
				}

				tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "NAME\tSTATE\tIP\tSTARTED")
				for _, n := range resp.Nodes {
					started := ""
					if n.StartedAt != nil {
						started = n.StartedAt.AsTime().Format("2006-01-02 15:04:05")
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", n.Name, n.State, n.Ip, started)
				}
				return tw.Flush()
			})
		},
	}
}
