package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	// Route the command line: `--agent` selects the agent launcher (create/attach
	// a tmux session in the current terminal, no kitty needed); otherwise kmux
	// runs the dashboard as before.
	pa, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "kmux: %v\n", err)
		os.Exit(1)
	}
	if pa.agent != "" {
		if err := runAgent(pa.path, pa.agent); err != nil {
			fmt.Fprintf(os.Stderr, "kmux: %v\n", err)
			os.Exit(1)
		}
		return
	}
	runDashboard(pa.path)
}

// runDashboard launches the kmux dashboard. pathArg, when non-empty, scopes kmux
// to the single git project containing it: the Sessions and Projects panels then
// show only that project (and its worktrees). Without it, kmux scans ~/git plus
// any configured folders. The dashboard requires kitty with remote control.
func runDashboard(pathArg string) {
	if os.Getenv("KITTY_LISTEN_ON") == "" {
		fmt.Fprintln(os.Stderr, "kmux: KITTY_LISTEN_ON is not set.")
		fmt.Fprintln(os.Stderr, "Run kmux inside kitty with remote control enabled:")
		fmt.Fprintln(os.Stderr, "  allow_remote_control yes")
		fmt.Fprintln(os.Stderr, "  listen_on unix:@kitty")
		os.Exit(1)
	}

	sidebarID, err := strconv.Atoi(os.Getenv("KITTY_WINDOW_ID"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "kmux: KITTY_WINDOW_ID is not set; run kmux inside a kitty window.")
		os.Exit(1)
	}

	var scopeDir string
	if pathArg != "" {
		proj, err := ScanProject(pathArg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "kmux: %v\n", err)
			os.Exit(1)
		}
		scopeDir = proj.Path
	}

	if err := GotoLayoutSplits(); err != nil {
		fmt.Fprintf(os.Stderr, "kmux: could not switch to splits layout: %v\n", err)
		os.Exit(1)
	}

	// Reap sessions that were already idle past the timeout when this run
	// started, before the dashboard attaches panes to them. Best-effort: config
	// or state read errors just skip the sweep.
	cfg, _ := LoadConfig()
	if _, idle, err := LoadState(); err == nil {
		sweepIdleAtLaunch(time.Now(), cfg.IdleDuration(), idle)
	}

	mgr := NewManager(sidebarID)
	// AltScreen gives a clean, full-pane dashboard (clears on launch, restores on exit).
	p := tea.NewProgram(newModel(mgr, scopeDir), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		mgr.CloseAll()
		fmt.Fprintf(os.Stderr, "kmux: %v\n", err)
		os.Exit(1)
	}
}
