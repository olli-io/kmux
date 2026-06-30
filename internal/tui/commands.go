package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/olli-io/kmux/internal/agent"
	"github.com/olli-io/kmux/internal/kitty"
	"github.com/olli-io/kmux/internal/layout"
	"github.com/olli-io/kmux/internal/project"
	"github.com/olli-io/kmux/internal/status"
	"github.com/olli-io/kmux/internal/tmux"
)

const pollInterval = 1 * time.Second

// blankPaneInterval is how often kmux scans kitty for user-spawned blank panes to
// turn into idle launchers. It runs on its own faster ticker, decoupled from
// pollInterval, because the scan is cheap (a single `kitten @ ls`) and benefits
// from low latency, whereas the main poll also drives the heavier project git
// scan and reconcile, which there's no reason to run this often.
const blankPaneInterval = 250 * time.Millisecond

// spinnerInterval is how often the busy-session animation advances a frame.
// Faster than pollInterval so the spinner reads as smooth motion without
// re-listing sessions each tick.
const spinnerInterval = 150 * time.Millisecond

// spinnerFrames is the rotating braille glyph cycle shown for a busy session: an
// arc of 4 filled dots (with a 2-dot gap) sweeping clockwise around the perimeter
// of one braille cell.
var spinnerFrames = []string{"⠹", "⠼", "⠶", "⠧", "⠏", "⠛"}

// messages
type tickMsg time.Time
type blankTickMsg time.Time
type sessionsMsg struct {
	names []string
	err   error
}
type projectsMsg struct {
	projects []project.Project
	err      error
}
type spinnerMsg struct{}
type reconciledMsg struct{ errs []error }
type attentionMsg struct {
	states map[string]status.AttentionState
	hashes map[string]uint64 // session name -> pane fingerprint, for idle tracking
}
type focusedMsg struct{ err error }
type savedMsg struct{ err error }

// blankPanesMsg carries the kitty window ids of bare interactive shells — panes
// the user spawned outside kmux. The dashboard turns newly appearing ones into
// idle launchers (see update's handling).
type blankPanesMsg struct {
	ids []int
	err error
}

// idleConvertedMsg reports the result of turning a blank pane into an idle slot.
type idleConvertedMsg struct{ err error }

// commandErrMsg reports a user-configured command that failed to launch; it
// drives the dismissible error float.
type commandErrMsg struct {
	title string
	err   error
}

func tickCmd() tea.Cmd {
	return tea.Tick(pollInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// blankTickCmd schedules the next blank-pane scan on the faster blankPaneInterval.
func blankTickCmd() tea.Cmd {
	return tea.Tick(blankPaneInterval, func(t time.Time) tea.Msg { return blankTickMsg(t) })
}

// spinnerCmd schedules the next busy-animation frame.
func spinnerCmd() tea.Cmd {
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return spinnerMsg{} })
}

// pollCmd lists agent sessions off the UI goroutine.
func pollCmd() tea.Cmd {
	return func() tea.Msg {
		names, err := tmux.ListAgentSessions()
		return sessionsMsg{names: names, err: err}
	}
}

// attentionCmd captures each session's tmux pane off the UI goroutine and
// classifies its attention state. It runs sequentially (capture-pane is a cheap
// local call and there are few sessions) and is best-effort: a capture failure
// for one session yields status.AttnUnknown for it rather than failing the whole batch.
// It is fed the full session list (including detached ones — tmux keeps their
// buffers), so a detached-but-waiting agent still shows its status glyph.
func attentionCmd(sessions []string) tea.Cmd {
	snap := append([]string(nil), sessions...)
	return func() tea.Msg {
		states := make(map[string]status.AttentionState, len(snap))
		hashes := make(map[string]uint64, len(snap))
		for _, s := range snap {
			text, err := tmux.CapturePane(s)
			if err != nil {
				// No hash recorded: the idle tracker treats this session as gone
				// for this poll and resets its clock when capture recovers, so a
				// flaky capture never causes a kill.
				states[s] = status.AttnUnknown
				continue
			}
			states[s] = status.ClassifyAttention(agent.AgentKind(s), text)
			hashes[s] = status.HashPane(text)
		}
		return attentionMsg{states: states, hashes: hashes}
	}
}

// blankPanesCmd lists kitty windows that are bare interactive shells (panes the
// user spawned outside kmux), off the UI goroutine. The dashboard uses the result
// to convert newly appearing blank panes into idle launchers.
func blankPanesCmd() tea.Cmd {
	return func() tea.Msg {
		ids, err := kitty.BlankShellWindows()
		return blankPanesMsg{ids: ids, err: err}
	}
}

// idleConvertCmd turns the blank pane with the given window id into a kmux idle
// launcher off the UI goroutine: it sends an `exec` of `kmux-idler --idle-loop`
// into the pane's shell, so the pane starts showing the idle hint and launching
// the picker on a keypress — exactly like a managed placeholder slot. idlerPath is
// the absolute path to the helper (from layout.IdlerPath).
func idleConvertCmd(id int, idlerPath string) tea.Cmd {
	return func() tea.Msg {
		runline := "exec " + shellQuote(idlerPath) + " --idle-loop"
		return idleConvertedMsg{err: kitty.RunInWindow(id, runline)}
	}
}

// projectsCmd scans projects off the UI goroutine. When scopeDir is set it
// resolves just that one project (scoped mode); otherwise it scans ~/git plus
// any folders listed in the config file.
func projectsCmd(scopeDir string) tea.Cmd {
	return func() tea.Msg {
		if scopeDir != "" {
			p, err := project.ScanProject(scopeDir)
			if err != nil {
				return projectsMsg{err: err}
			}
			return projectsMsg{projects: []project.Project{*p}}
		}
		projects, err := project.ScanProjects()
		return projectsMsg{projects: projects, err: err}
	}
}

// reconcileCmd performs kitty RC work off the UI goroutine. It attaches/detaches
// agent panes, then pads the layout with placeholder panes so real agent panes
// keep a fixed width. When the pane layout changes it follows up with a
// Rebalance to pin the sidebar width and even out the agent columns.
func reconcileCmd(mgr *layout.Manager, active []string) tea.Cmd {
	return func() tea.Msg {
		// A reconcile that adds a pane pulls the kitty app to the macOS foreground
		// even with --keep-focus, stealing system focus from whatever the user was
		// doing. These spawns are automatic (a session appeared on its own, not via
		// a manual open), so capture the frontmost app first and hand focus back
		// afterwards to keep the spawn in the background. Only query when an add is
		// actually pending, so the idle tick stays cheap. The Attached check is a
		// quick lock-free read; a slightly stale result at worst omits a restore.
		var prevApp string
		for _, s := range active {
			if !mgr.Attached(s) {
				prevApp = frontmostApp()
				break
			}
		}
		// The whole reconcile->compact->sync->rebalance pass runs atomically inside
		// the Manager, so overlapping reconciles (one per idle-reaped session, plus
		// the poll tick) serialize instead of racing the layout into extra slots.
		changed, errs := mgr.ReconcileAll(active)
		if changed && prevApp != "" {
			restoreFrontmostApp(prevApp)
		}
		return reconciledMsg{errs: errs}
	}
}

// focusCmd gives keyboard focus to a session's kitty pane off the UI goroutine.
func focusCmd(id int) tea.Cmd {
	return func() tea.Msg {
		return focusedMsg{err: kitty.FocusWindow(id)}
	}
}

// openSessionCmd creates (if needed) and attaches an agent session pane off the
// UI goroutine, then pads/rebalances the layout the same way reconcileCmd does
// so the new pane lands at the fixed agent width. agentCmd is the executable the
// new tmux session runs (e.g. "claude" or "opencode").
func openSessionCmd(mgr *layout.Manager, name, dir, agentCmd string) tea.Cmd {
	return func() tea.Msg {
		return reconciledMsg{errs: mgr.OpenAndSync(name, dir, agentCmd)}
	}
}

// killSessionCmd kills a session's tmux session off the UI goroutine, then
// re-lists so the panel updates immediately (the resulting sessionsMsg drives
// reconcile, which closes the now-orphaned pane).
func killSessionCmd(name string) tea.Cmd {
	return func() tea.Msg {
		if err := tmux.KillSession(name); err != nil {
			return sessionsMsg{err: err}
		}
		names, err := tmux.ListAgentSessions()
		return sessionsMsg{names: names, err: err}
	}
}

// reattachSessionCmd re-opens a pane for an already-running session off the UI
// goroutine (for a session whose pane was lost), then pads/rebalances the layout
// the same way openSessionCmd does.
func reattachSessionCmd(mgr *layout.Manager, name string) tea.Cmd {
	return func() tea.Msg {
		return reconciledMsg{errs: mgr.ReattachAndSync(name)}
	}
}

// saveStateCmd persists the detached-session set and the idle clocks off the UI
// goroutine. It snapshots both maps first so a later mutation can't race the
// write. (idle.snapshot already returns a fresh copy.)
func (m model) saveStateCmd() tea.Cmd {
	detached := make(map[string]bool, len(m.detached))
	for k, on := range m.detached {
		if on {
			detached[k] = true
		}
	}
	idle := m.idle.Snapshot()
	return func() tea.Msg {
		return savedMsg{err: status.SaveState(detached, idle)}
	}
}

// runUserCommand dispatches the configured command bound to key in the focused
// panel: it resolves the selected row's directory, substitutes {dir}, and runs
// the command in a new kitty tab. It returns nil when no command matches the key
// and panel, or when the row has no associated directory.
func (m model) runUserCommand(key string, rows []row) tea.Cmd {
	panel := panelName(m.focusedSection(rows))
	r := rowAt(rows, m.cursor)
	for _, c := range m.commands {
		if c.Key != key || !c.Matches(panel) {
			continue
		}
		vars := m.commandVars(r)
		if vars["dir"] == "" {
			return nil // no directory to run in (e.g. an ungrouped session)
		}
		runline := expandCommandVars(c.Cmd, vars)
		title := c.Title
		if title == "" {
			title = filepath.Base(vars["dir"])
		}
		switch c.EffectiveTarget() {
		case "detach":
			return detachProcessCmd(vars["dir"], title, runline)
		case "window":
			return userCommandCmd(vars["dir"], title, holdOnError(runline, title), kitty.OpenCommandWindow)
		default: // "tab"
			return userCommandCmd(vars["dir"], title, holdOnError(runline, title), kitty.OpenCommandTab)
		}
	}
	return nil
}

// holdOnError wraps a tab/window command so a non-zero exit keeps its kitty
// surface open with a labeled notice (awaiting a keypress) instead of flashing
// shut; a zero exit closes normally. It runs out of kmux's reach, so this is how
// a tab/window command's own failure surfaces. label is shell-escaped.
func holdOnError(runline, label string) string {
	return runline +
		`; __kmux_st=$?; if [ "$__kmux_st" -ne 0 ]; then ` +
		`printf '\n\033[1;31m%s failed (exit %s)\033[0m\nPress any key to close… ' ` + shellQuote(label) + ` "$__kmux_st"; ` +
		`__kmux_stty=$(stty -g 2>/dev/null); stty -echo -icanon 2>/dev/null; ` +
		`dd bs=1 count=1 >/dev/null 2>&1; ` +
		`[ -n "$__kmux_stty" ] && stty "$__kmux_stty" 2>/dev/null; fi`
}

// expandCommandVars substitutes each {name} placeholder in run with its
// shell-escaped value from vars; placeholders with no matching key are left
// as-is. See commandVars for the available names.
func expandCommandVars(run string, vars map[string]string) string {
	for k, v := range vars {
		run = strings.ReplaceAll(run, "{"+k+"}", shellQuote(v))
	}
	return run
}

// userCommandCmd runs a configured command's expanded run line off the UI
// goroutine via open (OpenCommandTab or OpenCommandWindow), with cwd set to dir.
func userCommandCmd(dir, title, runline string, open func(dir, title, runline string) error) tea.Cmd {
	return func() tea.Msg {
		if err := open(dir, title, runline); err != nil {
			return commandErrMsg{title: title, err: err}
		}
		return focusedMsg{}
	}
}

// detachGrace bounds how long detachProcessCmd waits for a just-started command
// to fail before treating it as a launched, live app.
const detachGrace = 600 * time.Millisecond

// detachProcessCmd runs runline (via `sh -c`) as a detached background process in
// dir with no kitty surface — for fork-and-return GUI apps (Zed, VS Code). The
// child gets its own process group, survives kmux, and has stdio at /dev/null.
// As kmux's own child its exit is observable: a failure within detachGrace floats
// a commandErrMsg; anything still alive is reaped in the background and reported
// launched.
func detachProcessCmd(dir, title, runline string) tea.Cmd {
	return func() tea.Msg {
		c := exec.Command("sh", "-c", runline)
		c.Dir = dir
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if devnull, err := os.Open(os.DevNull); err == nil {
			c.Stdin, c.Stdout, c.Stderr = devnull, devnull, devnull
		}
		if err := c.Start(); err != nil {
			return commandErrMsg{title: title, err: err}
		}
		done := make(chan error, 1)
		go func() { done <- c.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				return commandErrMsg{title: title, err: err}
			}
			return focusedMsg{}
		case <-time.After(detachGrace):
			return focusedMsg{} // still running: a live app, reaped by the goroutine
		}
	}
}

// shellQuote wraps s in single quotes for safe interpolation into a `sh -c` line,
// escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// openAgentTabCmd attaches an agent session in a new standalone kitty tab (not a
// managed pane), off the UI goroutine. When agentCmd is non-empty it first
// ensures a detached tmux session exists; for an already-running session pass an
// empty agentCmd to skip creation and just attach.
func openAgentTabCmd(name, dir, agentCmd string) tea.Cmd {
	return func() tea.Msg {
		if agentCmd != "" {
			if err := tmux.NewDetachedSession(name, dir, agentCmd); err != nil {
				return focusedMsg{err: err}
			}
		}
		return focusedMsg{err: kitty.OpenAgentTab(name, name)}
	}
}

// openTabCmd launches a new kitty tab running a fresh kmux scoped to dir, off
// the UI goroutine. The new tab is an independent kmux session in the same
// terminal window.
func openTabCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		exe, err := os.Executable()
		if err != nil {
			return focusedMsg{err: err}
		}
		return focusedMsg{err: kitty.OpenTab(exe, dir, "kmux::"+filepath.Base(dir))}
	}
}
