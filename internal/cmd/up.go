package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up",
		Short: "Create and start a VM from the current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("not yet implemented")
			return nil
		},
	}
}
