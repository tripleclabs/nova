package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tripleclabs/nova/internal/network"
	"github.com/spf13/cobra"
)

var (
	degradeLatency string
	degradeJitter  string
	degradeLoss    string
)

func newLinkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Manage network conditions between nodes",
	}

	degrade := &cobra.Command{
		Use:   "degrade <node-a> <node-b>",
		Short: "Add latency, jitter, or packet loss between two nodes",
		Args:  cobra.ExactArgs(2),
		RunE:  runLinkDegrade,
	}
	degrade.Flags().StringVar(&degradeLatency, "latency", "0ms", "one-way latency (e.g. 50ms)")
	degrade.Flags().StringVar(&degradeJitter, "jitter", "0ms", "jitter range (e.g. 10ms)")
	degrade.Flags().StringVar(&degradeLoss, "loss", "0%", "packet loss percentage (e.g. 5%)")

	partition := &cobra.Command{
		Use:   "partition <node-a> <node-b>",
		Short: "Create a hard network partition between two nodes",
		Args:  cobra.ExactArgs(2),
		RunE:  runLinkPartition,
	}

	heal := &cobra.Command{
		Use:   "heal <node-a> <node-b>",
		Short: "Remove all network conditions between two nodes",
		Args:  cobra.ExactArgs(2),
		RunE:  runLinkHeal,
	}

	status := &cobra.Command{
		Use:   "status",
		Short: "Show all active network conditions",
		RunE:  runLinkStatus,
	}

	reset := &cobra.Command{
		Use:   "reset",
		Short: "Remove all network conditions",
		RunE:  runLinkReset,
	}

	cmd.AddCommand(degrade, partition, heal, status, reset)
	return cmd
}

func runLinkDegrade(cmd *cobra.Command, args []string) error {
	c, err := loadConditioner()
	if err != nil {
		return err
	}

	latency, err := time.ParseDuration(degradeLatency)
	if err != nil {
		return fmt.Errorf("invalid latency: %w", err)
	}
	jitter, err := time.ParseDuration(degradeJitter)
	if err != nil {
		return fmt.Errorf("invalid jitter: %w", err)
	}
	loss, err := parsePercent(degradeLoss)
	if err != nil {
		return fmt.Errorf("invalid loss: %w", err)
	}

	if err := c.Degrade(args[0], args[1], latency, jitter, loss); err != nil {
		return err
	}

	if err := saveConditioner(c); err != nil {
		return err
	}

	fmt.Printf("Link %s <-> %s: latency=%v jitter=%v loss=%.1f%%\n",
		args[0], args[1], latency, jitter, loss*100)
	return nil
}

func runLinkPartition(cmd *cobra.Command, args []string) error {
	c, err := loadConditioner()
	if err != nil {
		return err
	}
	c.Partition(args[0], args[1])
	if err := saveConditioner(c); err != nil {
		return err
	}
	fmt.Printf("Link %s <-> %s: PARTITIONED\n", args[0], args[1])
	return nil
}

func runLinkHeal(cmd *cobra.Command, args []string) error {
	c, err := loadConditioner()
	if err != nil {
		return err
	}
	c.Heal(args[0], args[1])
	if err := saveConditioner(c); err != nil {
		return err
	}
	fmt.Printf("Link %s <-> %s: healed\n", args[0], args[1])
	return nil
}

func runLinkStatus(cmd *cobra.Command, args []string) error {
	c, err := loadConditioner()
	if err != nil {
		return err
	}

	rules := c.AllRules()
	if len(rules) == 0 {
		fmt.Println("No active network conditions.")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "LINK\tLATENCY\tJITTER\tLOSS\tSTATUS")
	for _, r := range rules {
		status := "degraded"
		if r.Down {
			status = "PARTITIONED"
		}
		fmt.Fprintf(tw, "%s <-> %s\t%v\t%v\t%.1f%%\t%s\n",
			r.NodeA, r.NodeB, r.Latency, r.Jitter, r.Loss*100, status)
	}
	return tw.Flush()
}

func runLinkReset(cmd *cobra.Command, args []string) error {
	c, err := loadConditioner()
	if err != nil {
		return err
	}
	c.Reset()
	if err := saveConditioner(c); err != nil {
		return err
	}
	fmt.Println("All network conditions cleared.")
	return nil
}

// Persistence: store conditioner rules in ~/.nova/chaos.json
func chaosPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".nova", "chaos.json"), nil
}

func loadConditioner() (*network.Conditioner, error) {
	c := network.NewConditioner()
	path, err := chaosPath()
	if err != nil {
		return c, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return c, nil // No file yet.
	}
	var rules []network.LinkRule
	if err := json.Unmarshal(data, &rules); err != nil {
		return c, nil
	}
	for _, r := range rules {
		c.SetRule(r)
	}
	return c, nil
}

func saveConditioner(c *network.Conditioner) error {
	path, err := chaosPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(c.AllRules(), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func parsePercent(s string) (float64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "%")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	return v / 100, nil
}
