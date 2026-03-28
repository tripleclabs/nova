package cmd

import (
	"github.com/3clabs/nova/internal/vm"
	"github.com/spf13/cobra"
)

func newNukeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "nuke [name]",
		Short: "Force kill a VM and delete all its data",
		RunE: func(cmd *cobra.Command, args []string) error {
			orch, err := vm.NewOrchestrator()
			if err != nil {
				return err
			}
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			return orch.Nuke(name)
		},
	}
}
