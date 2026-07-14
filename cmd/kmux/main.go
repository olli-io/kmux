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
	// The print/agent modes are for scripting and keybindings; otherwise kmux runs
	// the dashboard.
	pa, err := agent.ParseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "kmux: %v\n", err)
		os.Exit(1)
	}
	if pa.PrintSession {
		name, err := agent.SessionName(pa.Path, pa.Agent)
		if err != nil {
			fmt.Fprintf(os.Stderr, "kmux: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(name)
		return
	}
	if pa.PrintProject {
		dir, err := agent.CurrentProjectDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "kmux: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(dir)
		return
	}
	if pa.Attn {
		// Attention-marker hook mode: invoked by a Claude Code hook (event JSON on
		// stdin) or an OpenCode plugin (event as flags) when a session changes
		// attention state. Best-effort — a hook must never fail an agent turn — so we
		// log any error and exit 0.
		if err := agent.RunAttnHook(pa, os.Stdin); err != nil {
			fmt.Fprintf(os.Stderr, "kmux --attn: %v\n", err)
		}
		return
	}
	if pa.Agent != "" {
		if err := agent.RunAgent(pa.Path, pa.Agent); err != nil {
			fmt.Fprintf(os.Stderr, "kmux: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if pa.Splash {
		// Internal launch-overlay mode, spawned by the dashboard in its own kitty tab.
		if err := runSplash(); err != nil {
			fmt.Fprintf(os.Stderr, "kmux: %v\n", err)
			os.Exit(1)
		}
		return
	}
	runDashboard(pa.Path)
}

// runDashboard launches the kmux dashboard. A non-empty pathArg scopes kmux to the
// single git project containing it; otherwise it scans ~/git plus configured folders.
func runDashboard(pathArg string) {
	// Detect a non-kitty host before the remote-control check below, whose hint
	// wrongly implies the user is already in kitty.
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

	// Label the sidebar window so the dashboard is identifiable alongside the
	// [kmux]… agent sessions. Best-effort.
	_ = kitty.SetWindowTitle(sidebarID, agent.DashboardTitle())

	// Reap sessions already idle past the timeout at launch, before the dashboard
	// attaches panes to them. Best-effort: read errors skip the sweep.
	cfg, _ := config.LoadConfig()
	if _, idle, err := status.LoadState(); err == nil {
		status.SweepIdleAtLaunch(time.Now(), cfg.IdleDuration(), idle)
	}

	// Pop a splash tab in front so the user doesn't watch the first reconcile
	// assemble the layout pane by pane; opening it focused pushes this tab to the
	// background, where the dashboard builds its panes hidden. The model closes the
	// splash once its first reconcile settles. Best-effort: on failure launcherID is
	// 0 and startup proceeds with no overlay.
	var launcherID int
	if exe, err := os.Executable(); err == nil {
		launcherID, _ = kitty.OpenLauncherTab(exe)
	}

	mgr := layout.NewManager(sidebarID)
	// AltScreen gives a clean, full-pane dashboard (clears on launch, restores on exit).
	p := tea.NewProgram(tui.NewModel(mgr, scopeDir, launcherID), tea.WithAltScreen())
	_, runErr := p.Run()
	// Close the splash tab unconditionally on exit; a quit or crash before the model
	// dismisses it would otherwise orphan it. CloseWindow ignores a missing match.
	if launcherID != 0 {
		_ = kitty.CloseWindow(launcherID)
	}
	if runErr != nil {
		mgr.CloseAll()
		fmt.Fprintf(os.Stderr, "kmux: %v\n", runErr)
		os.Exit(1)
	}
}
