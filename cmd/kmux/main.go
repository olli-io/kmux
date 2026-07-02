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
	// Route the command line: `--session` prints the resolved session name and
	// exits (for scripting); `--project` prints the project directory of the tmux
	// session the caller is inside and exits (for scripts bound to a tmux
	// keybinding); `--agent` selects the agent launcher (create/attach a tmux
	// session in the current terminal, no kitty needed); otherwise kmux runs the
	// dashboard as before.
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
	if pa.Agent != "" {
		if err := agent.RunAgent(pa.Path, pa.Agent); err != nil {
			fmt.Fprintf(os.Stderr, "kmux: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if pa.Splash {
		// Internal launch-overlay mode: the dashboard spawns `kmux --splash` in its
		// own kitty tab and closes that tab once its layout has settled.
		if err := runSplash(); err != nil {
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

	// Label the sidebar window "[kmux]dashboard" so the dashboard process is
	// identifiable alongside the [kmux]… agent sessions. Best-effort: a title
	// failure must not stop the dashboard from launching.
	_ = kitty.SetWindowTitle(sidebarID, agent.DashboardTitle())

	// Reap sessions that were already idle past the timeout when this run
	// started, before the dashboard attaches panes to them. Best-effort: config
	// or state read errors just skip the sweep.
	cfg, _ := config.LoadConfig()
	if _, idle, err := status.LoadState(); err == nil {
		status.SweepIdleAtLaunch(time.Now(), cfg.IdleDuration(), idle)
	}

	// Pop a launch-overlay splash tab in front of the dashboard so the user doesn't
	// watch the first reconcile assemble the layout pane by pane. GotoLayoutSplits
	// above already ran on this (still-active) tab; opening the splash focused
	// pushes this tab to the background, where the dashboard builds its panes hidden
	// (kitty.Launch pins them to this tab regardless of focus). The model closes the
	// splash once its first reconcile settles. Best-effort: on failure launcherID is
	// 0 and startup proceeds with no overlay, exactly as before.
	var launcherID int
	if exe, err := os.Executable(); err == nil {
		launcherID, _ = kitty.OpenLauncherTab(exe)
	}

	mgr := layout.NewManager(sidebarID)
	// AltScreen gives a clean, full-pane dashboard (clears on launch, restores on exit).
	p := tea.NewProgram(tui.NewModel(mgr, scopeDir, launcherID), tea.WithAltScreen())
	_, runErr := p.Run()
	// Close the splash tab unconditionally on exit. The model normally dismisses it
	// once the first reconcile settles, but a quit before that (or a crash) would
	// otherwise orphan it; CloseWindow ignores a missing match, so closing an
	// already-dismissed splash is a no-op.
	if launcherID != 0 {
		_ = kitty.CloseWindow(launcherID)
	}
	if runErr != nil {
		mgr.CloseAll()
		fmt.Fprintf(os.Stderr, "kmux: %v\n", runErr)
		os.Exit(1)
	}
}
