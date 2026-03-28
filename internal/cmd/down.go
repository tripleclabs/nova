package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down",
		Short: "Gracefully stop a running VM",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("not yet implemented")
			return nil
		},
	}
}
