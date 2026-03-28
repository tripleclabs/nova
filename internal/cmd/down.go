package cmd

import (
	"github.com/3clabs/nova/internal/vm"
	"github.com/spf13/cobra"
)

func newDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down [name]",
		Short: "Gracefully stop a running VM",
		RunE: func(cmd *cobra.Command, args []string) error {
			orch, err := vm.NewOrchestrator()
			if err != nil {
				return err
			}
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			return orch.Down(name)
		},
	}
}
