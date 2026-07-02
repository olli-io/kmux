package main

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// runSplash renders the launch-overlay animation until the process is killed.
// It backs the internal `kmux --splash` mode the dashboard spawns in its own
// kitty tab (see kitty.OpenLauncherTab): the splash covers the screen while the
// dashboard builds its panes in the background tab, hiding the first reconcile's
// pane churn, and the dashboard closes this tab once its layout has settled — so
// the splash has no exit logic of its own, it just animates until then. Run
// standalone (for a look), q or ctrl+c quits it.
func runSplash() error {
	_, err := tea.NewProgram(splashModel{}, tea.WithAltScreen()).Run()
	return err
}

var (
	// splashWordmark styles the centered "kmux" banner (bold blue, matching the
	// dashboard's folder/claude accent). splashDim styles the spinner line under it.
	splashWordmark = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	splashDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// splashBanner is the "kmux" wordmark drawn as a framed block, shown centered on
// the launch overlay. Every row is 35 columns wide with the left "██" border at
// columns 2-3 and the right at 33-34; the blank corner rows are padded with
// strings.Repeat so the box stays rectangular, and the whole block is styled with
// splashWordmark.
var splashBanner = strings.Join([]string{
	" ██▀▀" + strings.Repeat(" ", 25) + "▀▀██ ",
	" ██ ▄▄" + strings.Repeat(" ", 26) + "██ ",
	" ██ ██ ▄█▀ ███▄███▄ ██ ██ ██ ██ ██ ",
	" ██ ████   ██ ██ ██ ██ ██  ███  ██ ",
	" ██ ██ ▀█▄ ██ ██ ██ ▀██▀█ ██ ██ ██ ",
	" ██▄▄" + strings.Repeat(" ", 25) + "▄▄██ ",
}, "\n")

// splashFrames is the spinner cycle shown under the wordmark — the same rotating
// braille arc the dashboard uses for busy sessions (tui.spinnerFrames).
var splashFrames = []string{"⠹", "⠼", "⠶", "⠧", "⠏", "⠛"}

// splashInterval is how often the splash spinner advances a frame.
const splashInterval = 120 * time.Millisecond

type splashTickMsg struct{}

type splashModel struct {
	frame         int
	width, height int
}

func (splashModel) Init() tea.Cmd { return splashTick() }

func splashTick() tea.Cmd {
	return tea.Tick(splashInterval, func(time.Time) tea.Msg { return splashTickMsg{} })
}

func (m splashModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		if s := msg.String(); s == "ctrl+c" || s == "q" {
			return m, tea.Quit
		}
		return m, nil
	case splashTickMsg:
		m.frame++
		return m, splashTick()
	}
	return m, nil
}

func (m splashModel) View() string {
	spinner := splashFrames[m.frame%len(splashFrames)]
	block := lipgloss.JoinVertical(lipgloss.Center,
		splashWordmark.Render(splashBanner),
		"",
		splashDim.Render(spinner+" launching…"),
	)
	if m.width == 0 || m.height == 0 {
		return block // no size yet: draw unplaced rather than collapse to empty
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, block)
}
