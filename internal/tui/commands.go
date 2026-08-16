package tui

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// macCadence throttles a polling interval on macOS, where the kitty and tmux
// round-trips are noticeably more expensive.
func macCadence(linux, mac time.Duration) time.Duration {
	if runtime.GOOS == "darwin" {
		return mac
	}
	return linux
}

var pollInterval = macCadence(250*time.Millisecond, 500*time.Millisecond)

// orphanConfirmPolls requires more than one miss so a transient stat failure
// can't reap a live agent.
const orphanConfirmPolls = 2

// projectInterval is far slower than pollInterval: a scan shells out to git many
// times over, and dirty/ahead/behind barely change second to second.
const projectInterval = 5 * time.Second

const spinnerInterval = 150 * time.Millisecond

// launcherMin keeps the splash covering the pane churn that continues past the
// first reconcile; launcherCap dismisses it if no reconcile ever completes.
const launcherMin = 750 * time.Millisecond
const launcherCap = 1500 * time.Millisecond

var spinnerFrames = []string{"⠹", "⠼", "⠶", "⠧", "⠏", "⠛"}

// messages
type tickMsg time.Time
type projectTickMsg time.Time
type sessionsMsg struct {
	names []string
	// orphaned sessions are killed after orphanConfirmPolls consecutive misses.
	// Nil on a poll that couldn't compute it.
	orphaned []string
	err      error
}
type projectsMsg struct {
	projects []project.Project
	err      error
	// partial carries only the rescanned active projects; the model merges those
	// by path instead of replacing m.projects wholesale.
	partial bool
}
type spinnerMsg struct{}

// reconciledMsg carries the blank panes read from the same snapshot the reconcile
// took. scanned marks the reconciles that looked for blanks, so an empty slice
// means "none" rather than "didn't check".
type reconciledMsg struct {
	errs    []error
	blanks  []kitty.BlankPane
	scanned bool
}

type launcherCapMsg struct{}
type launcherMinMsg struct{}
type launcherDismissedMsg struct{ err error }
type attentionMsg struct {
	states map[string]status.AttentionState
	hashes map[string]uint64 // session name -> pane fingerprint, for idle tracking
}
type focusedMsg struct{ err error }
type savedMsg struct{ err error }
type idleConvertedMsg struct{ err error }

// commandErrMsg drives the dismissible error float.
type commandErrMsg struct {
	title string
	err   error
}

func tickCmd() tea.Cmd {
	return tea.Tick(pollInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func projectTickCmd() tea.Cmd {
	return tea.Tick(projectInterval, func(t time.Time) tea.Msg { return projectTickMsg(t) })
}

func spinnerCmd() tea.Cmd {
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return spinnerMsg{} })
}

func launcherCapCmd() tea.Cmd {
	return tea.Tick(launcherCap, func(time.Time) tea.Msg { return launcherCapMsg{} })
}

func launcherMinCmd() tea.Cmd {
	return tea.Tick(launcherMin, func(time.Time) tea.Msg { return launcherMinMsg{} })
}

func pollCmd() tea.Cmd {
	return func() tea.Msg {
		return pollSessions()
	}
}

// pollSessions lists the live agent sessions and flags those whose anchor
// directory has vanished. A tmux error is returned verbatim so the caller leaves
// the session list untouched.
func pollSessions() tea.Msg {
	sessions, err := tmux.ListAgentSessionsFull()
	if err != nil {
		return sessionsMsg{err: err}
	}
	names := make([]string, len(sessions))
	var orphaned []string
	for i, s := range sessions {
		names[i] = s.Name
		// A deliberately-orphaned session was never bound to a project, so its
		// directory going away isn't a worktree removal.
		if !agent.IsOrphan(s.Name) && isMissingDir(s.Dir) {
			orphaned = append(orphaned, s.Name)
		}
	}
	return sessionsMsg{names: names, orphaned: orphaned}
}

// isMissingDir counts only a not-exist error; an empty path or any other stat
// error (permissions, transient I/O, an unavailable mount) reads as present.
func isMissingDir(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Stat(dir)
	return errors.Is(err, fs.ErrNotExist)
}

// attentionCmd classifies every session's attention state from its tmux pane.
// Panes are captured in one batched call, falling back to one call per session
// so a session that died mid-cycle can't blank the rest. Detached sessions are
// included; tmux keeps their buffers.
func attentionCmd(sessions []string) tea.Cmd {
	snap := append([]string(nil), sessions...)
	return func() tea.Msg {
		states := make(map[string]status.AttentionState, len(snap))
		hashes := make(map[string]uint64, len(snap))
		texts, err := tmux.CapturePanes(snap)
		if err != nil {
			texts = make(map[string]string, len(snap))
			for _, s := range snap {
				if t, err := tmux.CapturePane(s); err == nil {
					texts[s] = t
				}
			}
		}
		for _, s := range snap {
			text, ok := texts[s]
			if !ok {
				// Omitting the hash makes the idle tracker treat this session as gone
				// for this poll, so a flaky capture never causes a kill.
				states[s] = status.AttnUnknown
				continue
			}
			states[s] = status.ClassifyAttention(agent.AgentKind(s), text)
			hashes[s] = status.HashPane(text)
		}
		return attentionMsg{states: states, hashes: hashes}
	}
}

// convertBlankPaneCmd handles a newly appeared user-spawned blank pane off the UI
// goroutine. A pane that is its own full-height column is a manual *vertical* split
// — a fourth column the fixed sidebar+maxColumns layout has no room for — so it is
// restacked under an existing column (layout.ReorgVerticalPane). Any other blank
// pane (already stacked, i.e. a horizontal split) is turned into a kmux idle
// launcher in place: an `exec` of `kmux-idler --idle-loop` makes it show the idle
// hint and launch the picker on a keypress, exactly like a managed placeholder
// slot. The standalone-column classification rides along on the pane from the scan
// (see kitty.BlankPane), so no second `ls` is needed here. idlerPath is the
// absolute path to the helper (from layout.IdlerPath).
func convertBlankPaneCmd(mgr *layout.Manager, pane kitty.BlankPane, idlerPath string) tea.Cmd {
	return func() tea.Msg {
		if pane.StandaloneColumn {
			return idleConvertedMsg{err: mgr.ReorgVerticalPane(pane.ID)}
		}
		runline := "exec " + shellQuote(idlerPath) + " --idle-loop"
		return idleConvertedMsg{err: kitty.RunInWindow(pane.ID, runline)}
	}
}

// projectsCmd resolves just scopeDir when set, otherwise scans ~/git plus any
// folders listed in the config file.
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

// activeProjectsCmd is the steady-state refresh: it rescans only the projects
// with a running session.
func activeProjectsCmd(paths []string) tea.Cmd {
	return func() tea.Msg {
		return projectsMsg{projects: project.ScanProjectsAt(paths), partial: true}
	}
}

// reconcileCmd attaches/detaches agent panes, then pads the layout with
// placeholders so real agent panes keep a fixed width.
func reconcileCmd(mgr *layout.Manager, active []string) tea.Cmd {
	return func() tea.Msg {
		// Adding a pane pulls kitty to the macOS foreground even with --keep-focus,
		// stealing system focus. These spawns are automatic, so capture the frontmost
		// app first and hand focus back afterwards. Only query when an add is pending;
		// a stale Attached read at worst omits a restore.
		var prevApp string
		for _, s := range active {
			if !mgr.Attached(s) {
				prevApp = frontmostApp()
				break
			}
		}
		// Atomic inside the Manager, so overlapping reconciles serialize instead of
		// racing the layout into extra slots.
		changed, blanks, errs := mgr.ReconcileAll(active)
		if changed && prevApp != "" {
			restoreFrontmostApp(prevApp)
		}
		return reconciledMsg{errs: errs, blanks: blanks, scanned: true}
	}
}

func focusCmd(id int) tea.Cmd {
	return func() tea.Msg {
		return focusedMsg{err: kitty.FocusWindow(id)}
	}
}

// setTabTitleCmd retitles the dashboard's kitty tab off the UI goroutine, matching
// on the sidebar window id (windowID). It is best-effort and returns no message: a
// title failure must not disrupt the dashboard, and the title carries no state the
// model needs to hear back about.
func setTabTitleCmd(windowID int, scopeDir string, needsAttention bool) tea.Cmd {
	return func() tea.Msg {
		_ = kitty.SetTabTitle(windowID, agent.DashTabTitle(scopeDir, needsAttention))
		return nil
	}
}

// setSessionAttnCmd renames session (a canonical name) to surface or clear the "[!!]"
// attention marker on the tmux SESSION itself, off the UI goroutine. It renames from the
// session's current live name (resolved via LiveName) to AttnSession(canonical, want), so
// the marker shows in tmux's status bar and choose-tree. parseSessionList strips the
// marker back off on the next poll, so kmux's identity/map keys stay canonical.
// Best-effort and message-less, matching setTabTitleCmd: a rename failure must not
// disrupt the dashboard. The attentionMsg handler only calls this on a permission-state
// flip, so it doesn't shell out every poll.
func setSessionAttnCmd(session string, needsAttention bool) tea.Cmd {
	return func() tea.Msg {
		_ = tmux.RenameSession(tmux.LiveName(session), agent.AttnSession(session, needsAttention))
		return nil
	}
}

// dismissLauncherCmd tears down the launch-overlay splash tab off the UI
// goroutine: it focuses the finished dashboard (sidebarID) first, so the reveal
// switches to the settled layout, then closes the splash tab (launcherID). Doing
// it in that order means the user never sees a bare tab between the splash
// closing and the dashboard appearing. Best-effort — close ignores a missing
// match — so a splash the user closed by hand can't error the launch.
func dismissLauncherCmd(sidebarID, launcherID int) tea.Cmd {
	return func() tea.Msg {
		if err := kitty.FocusWindow(sidebarID); err != nil {
			return launcherDismissedMsg{err: err}
		}
		return launcherDismissedMsg{err: kitty.CloseWindow(launcherID)}
	}
}

// openSessionCmd creates (if needed) and attaches an agent session pane, then
// pads/rebalances the layout. agentCmd is the executable the new tmux session runs.
func openSessionCmd(mgr *layout.Manager, name, dir, agentCmd string) tea.Cmd {
	return func() tea.Msg {
		return reconciledMsg{errs: mgr.OpenAndSync(name, dir, agentCmd)}
	}
}

// killSessionCmd re-lists after the kill so the panel updates immediately; the
// resulting sessionsMsg drives the reconcile that closes the orphaned pane.
func killSessionCmd(name string) tea.Cmd {
	return func() tea.Msg {
		if err := tmux.KillSession(name); err != nil {
			return sessionsMsg{err: err}
		}
		return pollSessions()
	}
}

// reattachSessionCmd re-opens a pane for a running session whose pane was lost.
func reattachSessionCmd(mgr *layout.Manager, name string) tea.Cmd {
	return func() tea.Msg {
		return reconciledMsg{errs: mgr.ReattachAndSync(name)}
	}
}

// saveProjectsCmd persists the full scanned project list off the UI goroutine so
// a freshly-spawned idler can paint its picker from disk instead of re-scanning
// every repo under ~/git. It snapshots the slice header so a later m.projects
// replacement can't race the write; the underlying Project values are only read.
// The result is discarded (a fresh tea.Msg the model ignores) — a write error is
// non-fatal, the idler just falls back to a live scan.
func saveProjectsCmd(projects []project.Project) tea.Cmd {
	snapshot := append([]project.Project(nil), projects...)
	return func() tea.Msg {
		_ = status.SaveProjects(snapshot)
		return nil
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
// panel, run against the selected row's directory. Returns nil when no command
// matches, or when the row has no directory.
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

// holdOnError keeps a failed command's kitty surface open with a labeled notice
// instead of flashing shut. The command runs out of kmux's reach, so this is how
// its failure surfaces. label is shell-escaped.
func holdOnError(runline, label string) string {
	return runline +
		`; __kmux_st=$?; if [ "$__kmux_st" -ne 0 ]; then ` +
		`printf '\n\033[1;31m%s failed (exit %s)\033[0m\nPress any key to close… ' ` + shellQuote(label) + ` "$__kmux_st"; ` +
		`__kmux_stty=$(stty -g 2>/dev/null); stty -echo -icanon 2>/dev/null; ` +
		`dd bs=1 count=1 >/dev/null 2>&1; ` +
		`[ -n "$__kmux_stty" ] && stty "$__kmux_stty" 2>/dev/null; fi`
}

// expandCommandVars shell-escapes each substituted value; placeholders with no
// matching key are left as-is. See commandVars for the available names.
func expandCommandVars(run string, vars map[string]string) string {
	for k, v := range vars {
		run = strings.ReplaceAll(run, "{"+k+"}", shellQuote(v))
	}
	return run
}

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

// detachProcessCmd runs runline as a detached background process with no kitty
// surface, for fork-and-return GUI apps. The child gets its own process group and
// survives kmux, but its early exit is still observable.
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

// shellQuote wraps s in single quotes for safe interpolation into a `sh -c` line.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// openAgentTabCmd attaches an agent session in a standalone kitty tab, not a
// managed pane. A non-empty agentCmd first ensures a detached tmux session exists;
// pass an empty one to just attach.
func openAgentTabCmd(name, dir, agentCmd string) tea.Cmd {
	return func() tea.Msg {
		if agentCmd != "" {
			if err := tmux.NewDetachedSession(name, dir, agentCmd); err != nil {
				return focusedMsg{err: err}
			}
		}
		// Attach by the live name (a blocked session carries "[!!]"); keep the canonical
		// name as the tab title.
		return focusedMsg{err: kitty.OpenAgentTab(tmux.LiveName(name), name)}
	}
}

// openTabCmd launches a new kitty tab running a fresh kmux scoped to dir.
func openTabCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		exe, err := os.Executable()
		if err != nil {
			return focusedMsg{err: err}
		}
		return focusedMsg{err: kitty.OpenTab(exe, dir, "kmux::"+filepath.Base(dir))}
	}
}
