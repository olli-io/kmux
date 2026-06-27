package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/olli-io/kmux/internal/agent"
	"github.com/olli-io/kmux/internal/config"
	"github.com/olli-io/kmux/internal/kitty"
	"github.com/olli-io/kmux/internal/layout"
	"github.com/olli-io/kmux/internal/project"
	"github.com/olli-io/kmux/internal/status"
	"github.com/olli-io/kmux/internal/tui"
)

func main() {
	// Route the command line: `--agent` selects the agent launcher (create/attach
	// a tmux session in the current terminal, no kitty needed); otherwise kmux
	// runs the dashboard as before.
	pa, err := agent.ParseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "kmux: %v\n", err)
		os.Exit(1)
	}
	if pa.Agent != "" {
		if err := agent.RunAgent(pa.Path, pa.Agent); err != nil {
			fmt.Fprintf(os.Stderr, "kmux: %v\n", err)
			os.Exit(1)
		}
		return
	}
	runDashboard(pa.Path)
}

// runDashboard launches the kmux dashboard. pathArg, when non-empty, scopes kmux
// to the single git project containing it: the Sessions and Projects panels then
// show only that project (and its worktrees). Without it, kmux scans ~/git plus
// any configured folders. The dashboard requires kitty with remote control.
func runDashboard(pathArg string) {
	// kmux drives its split-pane dashboard through kitty's remote control, so it
	// only works inside the kitty terminal. Detect a non-kitty host first and fail
	// with a clear compatibility message, rather than the remote-control hint below
	// (which wrongly implies the user is already in kitty).
	if !kitty.InKitty() {
		fmt.Fprintln(os.Stderr, "kmux: incompatible terminal — kmux only runs inside the kitty terminal.")
		if term := os.Getenv("TERM"); term != "" {
			fmt.Fprintf(os.Stderr, "Detected TERM=%s.\n", term)
		}
		fmt.Fprintln(os.Stderr, "Install kitty and run kmux inside it: https://sw.kovidgoyal.net/kitty/")
		os.Exit(1)
	}

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
		proj, err := project.ScanProject(pathArg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "kmux: %v\n", err)
			os.Exit(1)
		}
		scopeDir = proj.Path
	}

	if err := kitty.GotoLayoutSplits(); err != nil {
		fmt.Fprintf(os.Stderr, "kmux: could not switch to splits layout: %v\n", err)
		os.Exit(1)
	}

	// Reap sessions that were already idle past the timeout when this run
	// started, before the dashboard attaches panes to them. Best-effort: config
	// or state read errors just skip the sweep.
	cfg, _ := config.LoadConfig()
	if _, idle, err := status.LoadState(); err == nil {
		status.SweepIdleAtLaunch(time.Now(), cfg.IdleDuration(), idle)
	}

	mgr := layout.NewManager(sidebarID)
	// AltScreen gives a clean, full-pane dashboard (clears on launch, restores on exit).
	p := tea.NewProgram(tui.NewModel(mgr, scopeDir), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		mgr.CloseAll()
		fmt.Fprintf(os.Stderr, "kmux: %v\n", err)
		os.Exit(1)
	}
}
