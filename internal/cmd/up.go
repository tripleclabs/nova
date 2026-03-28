package cmd

import (
	"github.com/3clabs/nova/internal/vm"
	"github.com/spf13/cobra"
)

func newUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up",
		Short: "Create and start a VM from the current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			orch, err := vm.NewOrchestrator()
			if err != nil {
				return err
			}
			return orch.Up(cmd.Context(), flagConfig)
		},
	}
}
