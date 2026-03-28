package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newShellCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "shell",
		Short: "Open an SSH session to a running VM",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("not yet implemented")
			return nil
		},
	}
}
