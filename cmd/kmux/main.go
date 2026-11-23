package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/olli-io/kmux/internal/agent"
	"github.com/olli-io/kmux/internal/config"
	"github.com/olli-io/kmux/internal/grid"
	"github.com/olli-io/kmux/internal/project"
	"github.com/olli-io/kmux/internal/status"
	"github.com/olli-io/kmux/internal/tmux"
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
	if pa.Help {
		fmt.Print(helpText)
		return
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
	if pa.PrintPane {
		id, err := agent.PaneID(pa.Path, pa.Agent)
		if err != nil {
			fmt.Fprintf(os.Stderr, "kmux: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(id)
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
	if pa.PrintProjects {
		dirs, err := agent.LaunchDirs(pa.Path)
		if err != nil {
			fail(err)
		}
		for _, d := range dirs {
			fmt.Println(d)
		}
		return
	}
	if pa.Launch {
		if err := agent.Launch(pa.Path, pa.Agent); err != nil {
			fail(err)
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
	runDashboard(pa.Path)
}

// runDashboard launches the kmux dashboard. A non-empty pathArg scopes kmux to the
// single git project containing it; otherwise it scans ~/git plus configured folders.
//
// The dashboard runs in a tmux pane, and the window of that pane becomes the
// grid. Outside tmux, kmux starts (or attaches to) a tmux session that runs it.
func runDashboard(pathArg string) {
	var scopeDir string
	if pathArg != "" {
		proj, err := project.ScanProject(pathArg)
		if err != nil {
			fail(err)
		}
		scopeDir = proj.Path
	}

	pane := os.Getenv("TMUX_PANE")
	if os.Getenv("TMUX") == "" || pane == "" {
		fail(execInTmux(scopeDir))
	}
	win, err := tmux.Display(pane, "#{window_id}")
	if err != nil {
		fail(err)
	}
	win = strings.TrimSpace(win)
	snap, err := tmux.Snapshot()
	if err != nil {
		fail(err)
	}
	for _, p := range snap {
		if p.Window == win && p.Role == tmux.RoleDash && p.ID != pane && !p.Dead {
			fail(fmt.Errorf("this tmux window already has a kmux dashboard (pane %s)", p.ID))
		}
	}

	// Reap sessions already idle past the timeout at launch, before the dashboard
	// moves their panes into the grid. Best-effort: read errors skip the sweep.
	cfg, _ := config.LoadConfig()
	if _, idle, err := status.LoadState(); err == nil {
		status.SweepIdleAtLaunch(time.Now(), cfg.IdleDuration(), idle)
	}

	mgr, err := grid.New(pane, win)
	if err != nil {
		fail(err)
	}
	mgr.SetLauncher(launcherCmd(scopeDir))
	// The mark lets a later `kmux` find this dashboard when its session name changed.
	_ = tmux.SetPaneOptions(pane, tmux.OptDash, agent.DashTabTitle(scopeDir, false))
	// The dashboard declares its own terminal state (alt screen, focus reporting)
	// on the tea.View it returns, so the program needs no options here.
	p := tea.NewProgram(tui.NewModel(mgr, scopeDir))
	if _, err := p.Run(); err != nil {
		_ = mgr.Close()
		fail(err)
	}
}

// execInTmux replaces this process with a tmux client on the dashboard session.
// tmux makes the session, with kmux as its command, if it does not exist. It
// returns only on an error.
func execInTmux(scopeDir string) error {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("kmux needs tmux: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// tmux gives one command argument to a shell, thus the quotes.
	line := shellQuote(exe)
	if scopeDir != "" {
		line += " " + shellQuote(scopeDir)
	}
	name := agent.DashTabTitle(scopeDir, false)
	snap, snapErr := tmux.Snapshot()
	// A dashboard for this scope runs already. Attach to its session, whatever
	// the name of that session is now, and give the session its name back.
	if dash, rename := runningDash(snap, name); dash != "" {
		args := []string{"tmux"}
		if rename {
			args = append(args, "rename-session", "-t", dash, name, ";")
		}
		return syscall.Exec(tmuxPath, append(args, "attach-session", "-t", dash), os.Environ())
	}
	// A dashboard that stopped without its cleanup leaves its session with the
	// agent panes but no dashboard. Start a new dashboard pane in that window.
	if snapErr == nil {
		var orphan *tmux.Pane
		hasDash := false
		for i, p := range snap {
			if p.Session != name || p.Dead {
				continue
			}
			hasDash = hasDash || p.Role == tmux.RoleDash
			if orphan == nil || (orphan.Grid == "" && p.Grid != "") {
				orphan = &snap[i]
			}
		}
		if orphan != nil && !hasDash {
			if err := tmux.Chain([][]string{{"split-window", "-d", "-h", "-b", "-t", orphan.ID, line}}); err != nil {
				return err
			}
		}
	}
	return syscall.Exec(tmuxPath, []string{"tmux", "new-session", "-A", "-s", name, "-n", name, line}, os.Environ())
}

// launcherCmd returns the command of the launcher pane: the kmux-launcher script
// that is installed beside the binary. It returns "" when there is no script.
func launcherCmd(scopeDir string) string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	script := filepath.Join(filepath.Dir(exe), "kmux-launcher")
	if info, err := os.Stat(script); err != nil || info.IsDir() {
		return ""
	}
	// env makes the line independent of the syntax of the user's shell.
	return "exec env KMUX=" + shellQuote(exe) +
		" KMUX_SCOPE=" + shellQuote(scopeDir) +
		" KMUX_KINDS=" + shellQuote(strings.Join(agent.KindNames(), " ")) +
		" " + shellQuote(script)
}

// runningDash returns the pane of the live dashboard that has the session name
// name, or "". rename is true when the session of that pane has a different
// name now and name is free.
func runningDash(snap []tmux.Pane, name string) (pane string, rename bool) {
	taken := false
	var dash *tmux.Pane
	for i, p := range snap {
		taken = taken || p.Session == name
		if dash == nil && !p.Dead && p.Role == tmux.RoleDash && p.Dash == name {
			dash = &snap[i]
		}
	}
	if dash == nil {
		return "", false
	}
	return dash.ID, dash.Session != name && !taken
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "kmux: %v\n", err)
	os.Exit(1)
}
