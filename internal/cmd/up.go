package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
	"github.com/spf13/cobra"

	"github.com/tripleclabs/nova/internal/config"
	pb "github.com/tripleclabs/nova/pkg/novapb/nova/v1"
)

// loadConfigNodes parses raw HCL and returns the node names defined in it.
// Used to filter streaming events to only those belonging to this apply.
func loadConfigNodes(hcl []byte) ([]string, error) {
	cfg, err := config.Parse(hcl, "")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, n := range cfg.ResolveNodes() {
		names = append(names, n.Name)
	}
	return names, nil
}

func newUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up",
		Short: "Create and start a VM from the current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			hcl, err := os.ReadFile(flagConfig)
			if err != nil {
				return fmt.Errorf("reading config %s: %w", flagConfig, err)
			}

			// Resolve the config file's directory to an absolute path so the daemon
			// can resolve relative host_paths (like ".") correctly regardless of
			// its own working directory.
			absConfig, err := filepath.Abs(flagConfig)
			if err != nil {
				return fmt.Errorf("resolving config path: %w", err)
			}
			configDir := filepath.Dir(absConfig)

			// Generate a unique session ID so concurrent nova-up calls only exit
			// on their own apply_done, not another invocation's.
			var sessionBytes [8]byte
			rand.Read(sessionBytes[:])
			sessionID := hex.EncodeToString(sessionBytes[:])

			// Parse the config to know which node names belong to this apply,
			// so we only print events for our own nodes.
			ownNodes := map[string]bool{}
			if cfg, err := loadConfigNodes(hcl); err == nil {
				for _, n := range cfg {
					ownNodes[n] = true
				}
			}

			return withDaemon(func(ctx context.Context, client pb.NovaClient) error {
				ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs(
					"nova-config-dir", configDir,
					"nova-session-id", sessionID,
				))
				// Subscribe to events BEFORE calling Apply so no progress is missed.
				evtCtx, evtCancel := context.WithCancel(ctx)
				defer evtCancel()

				stream, err := client.StreamEvents(evtCtx, &emptypb.Empty{})
				if err != nil {
					return fmt.Errorf("streaming events: %w", err)
				}

				// Forward events to stdout, filtered to our nodes and our apply_done.
				evtDone := make(chan struct{})
				go func() {
					defer close(evtDone)
					for {
						evt, err := stream.Recv()
						if err != nil {
							return
						}
						if evt.Type == "apply_done" {
							if evt.Detail == sessionID {
								return
							}
							continue // another concurrent nova-up finishing — ignore
						}
						// Only print log events for nodes in our config.
						if len(ownNodes) > 0 && evt.Node != "" && !ownNodes[evt.Node] {
							continue
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
