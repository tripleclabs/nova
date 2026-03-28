package cmd

import (
	"log/slog"
	"os"

	"github.com/spf13/cobra"
)

// Global flags.
var (
	flagConfig  string
	flagVerbose bool
	flagNoColor bool
)

// NewRootCmd builds the top-level nova command with all subcommands.
func NewRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:   "nova",
		Short: "Next-generation local VM orchestrator",
		Long:  "Nova is a modern, lightning-fast, cloud-native replacement for Vagrant.",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			level := slog.LevelInfo
			if flagVerbose {
				level = slog.LevelDebug
			}
			slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
				Level: level,
			})))
		},
		SilenceUsage: true,
	}

	root.PersistentFlags().StringVar(&flagConfig, "config", "nova.hcl", "path to configuration file")
	root.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false, "enable debug logging")
	root.PersistentFlags().BoolVar(&flagNoColor, "no-color", false, "disable colored output")

	root.AddCommand(
		newVersionCmd(version),
		newInitCmd(),
		newUpCmd(),
		newDownCmd(),
		newStatusCmd(),
		newNukeCmd(),
		newShellCmd(),
		newLinkCmd(),
		newMonitorCmd(),
	)

	return root
}
