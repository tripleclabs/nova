package cmd

import (
	"context"
	"fmt"
	"os"

	"google.golang.org/protobuf/types/known/emptypb"
	"github.com/spf13/cobra"

	pb "github.com/tripleclabs/nova/pkg/novapb/nova/v1"
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
				// Subscribe to events BEFORE calling Apply so no progress is missed.
				evtCtx, evtCancel := context.WithCancel(ctx)
				defer evtCancel()

				stream, err := client.StreamEvents(evtCtx, &emptypb.Empty{})
				if err != nil {
					return fmt.Errorf("streaming events: %w", err)
				}

				// Forward events to stdout until apply_done or the stream closes.
				evtDone := make(chan struct{})
				go func() {
					defer close(evtDone)
					for {
						evt, err := stream.Recv()
						if err != nil {
							return
						}
						if evt.Type == "apply_done" {
							return
						}
						if evt.Node != "" {
							fmt.Printf("[%s] %s\n", evt.Node, evt.Detail)
						} else {
							fmt.Println(evt.Detail)
						}
					}
				}()

				resp, applyErr := client.Apply(ctx, &pb.ApplyRequest{
					HclConfig: string(hcl),
				})

				// On success, wait for the event goroutine to drain apply_done.
				// On error, cancel the stream so the goroutine exits.
				if applyErr != nil {
					evtCancel()
				}
				<-evtDone
				evtCancel() // no-op if already called

				if applyErr != nil {
					return fmt.Errorf("apply: %w", applyErr)
				}

				fmt.Println()
				for _, n := range resp.Nodes {
					fmt.Printf("%s: %s (ip: %s)\n", n.Name, n.State, n.Ip)
				}
				return nil
			})
		},
	}
}
