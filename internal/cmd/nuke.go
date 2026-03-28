package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newNukeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "nuke",
		Short: "Force kill a VM and delete all its data",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("not yet implemented")
			return nil
		},
	}
}
