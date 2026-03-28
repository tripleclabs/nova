package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/3clabs/nova/internal/network"
	"github.com/3clabs/nova/internal/state"
	"github.com/3clabs/nova/internal/vm"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

func newMonitorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "monitor",
		Short: "Launch the interactive TUI dashboard",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := tea.NewProgram(newMonitorModel(), tea.WithAltScreen())
			_, err := p.Run()
			return err
		},
	}
}

// --- Bubbletea model ---

type tickMsg time.Time

type monitorModel struct {
	machines []*state.Machine
	rules    []network.LinkRule
	cursor   int
	width    int
	height   int
	err      error
}

func newMonitorModel() monitorModel {
	return monitorModel{}
}

func (m monitorModel) Init() tea.Cmd {
	return tea.Batch(tickCmd(), tea.WindowSize())
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m monitorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.rules)-1 {
				m.cursor++
			}
		case "p":
			// Toggle partition on selected link.
			if m.cursor < len(m.rules) {
				r := m.rules[m.cursor]
				c, _ := loadConditioner()
				if r.Down {
					c.Heal(r.NodeA, r.NodeB)
				} else {
					c.Partition(r.NodeA, r.NodeB)
				}
				saveConditioner(c)
			}
		case "h":
			// Heal selected link.
			if m.cursor < len(m.rules) {
				r := m.rules[m.cursor]
				c, _ := loadConditioner()
				c.Heal(r.NodeA, r.NodeB)
				saveConditioner(c)
			}
		}

	case tickMsg:
		m.refresh()
		return m, tickCmd()

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.refresh()
	}

	return m, nil
}

func (m *monitorModel) refresh() {
	orch, err := vm.NewOrchestrator()
	if err != nil {
		m.err = err
		return
	}
	m.machines, m.err = orch.Status()

	c, _ := loadConditioner()
	m.rules = c.AllRules()
}

func (m monitorModel) View() string {
	if m.err != nil {
		return fmt.Sprintf("Error: %v\n\nPress q to quit.", m.err)
	}

	var b strings.Builder

	// Header.
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("212")).
		MarginBottom(1)
	b.WriteString(headerStyle.Render("NOVA MONITOR"))
	b.WriteString("\n\n")

	// Nodes panel.
	nodeHeaderStyle := lipgloss.NewStyle().Bold(true).Underline(true)
	b.WriteString(nodeHeaderStyle.Render("Nodes"))
	b.WriteString("\n")

	if len(m.machines) == 0 {
		dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
		b.WriteString(dimStyle.Render("  No VMs running."))
		b.WriteString("\n")
	} else {
		for _, machine := range m.machines {
			stateIcon := stateIndicator(machine.State)
			uptime := time.Since(machine.CreatedAt).Truncate(time.Second)
			b.WriteString(fmt.Sprintf("  %s %-20s %-10s  up %s\n",
				stateIcon, machine.Name, machine.State, uptime))
		}
	}
	b.WriteString("\n")

	// Network topology panel.
	b.WriteString(nodeHeaderStyle.Render("Network Links"))
	b.WriteString("\n")

	if len(m.rules) == 0 {
		dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
		b.WriteString(dimStyle.Render("  No active conditions. Use 'nova link degrade' to add some."))
		b.WriteString("\n")
	} else {
		for i, r := range m.rules {
			cursor := "  "
			if i == m.cursor {
				cursor = "> "
			}

			linkStr := fmt.Sprintf("%s <-> %s", r.NodeA, r.NodeB)
			var condStr string
			if r.Down {
				condStr = lipgloss.NewStyle().
					Foreground(lipgloss.Color("196")).
					Bold(true).
					Render("PARTITIONED")
			} else {
				parts := []string{}
				if r.Latency > 0 {
					parts = append(parts, fmt.Sprintf("lat=%v", r.Latency))
				}
				if r.Jitter > 0 {
					parts = append(parts, fmt.Sprintf("jit=%v", r.Jitter))
				}
				if r.Loss > 0 {
					parts = append(parts, fmt.Sprintf("loss=%.1f%%", r.Loss*100))
				}
				if len(parts) == 0 {
					parts = append(parts, "healthy")
				}
				condStr = lipgloss.NewStyle().
					Foreground(lipgloss.Color("214")).
					Render(strings.Join(parts, " "))
			}

			b.WriteString(fmt.Sprintf("%s%-25s  %s\n", cursor, linkStr, condStr))
		}
	}

	// Controls.
	b.WriteString("\n")
	controlStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	b.WriteString(controlStyle.Render("  [p] toggle partition  [h] heal  [j/k] navigate  [q] quit"))
	b.WriteString("\n")

	return b.String()
}

func stateIndicator(s state.MachineState) string {
	switch s {
	case state.StateRunning:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("●")
	case state.StateStopped:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("○")
	case state.StateCreating:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render("◐")
	case state.StateError:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("✗")
	default:
		return "?"
	}
}
