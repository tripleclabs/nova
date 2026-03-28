package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/3clabs/nova/internal/state"
	"github.com/3clabs/nova/internal/vm"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the status of all managed VMs",
		RunE: func(cmd *cobra.Command, args []string) error {
			orch, err := vm.NewOrchestrator()
			if err != nil {
				return err
			}
			machines, err := orch.Status()
			if err != nil {
				return err
			}
			if len(machines) == 0 {
				fmt.Println("No VMs found. Run 'nova up' to create one.")
				return nil
			}
			return printStatus(machines)
		},
	}
}

func printStatus(machines []*state.Machine) error {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATE\tPID\tCREATED")
	for _, m := range machines {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n",
			m.Name,
			m.State,
			m.PID,
			m.CreatedAt.Format("2006-01-02 15:04:05"),
		)
	}
	return tw.Flush()
}
