package tui

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/olli-io/kmux/internal/agent"
	"github.com/olli-io/kmux/internal/grid"
	"github.com/olli-io/kmux/internal/project"
	"github.com/olli-io/kmux/internal/status"
	"github.com/olli-io/kmux/internal/tmux"
)

// macCadence throttles a polling interval on macOS, where a tmux round-trip is
// noticeably more expensive.
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

var spinnerFrames = []string{"⠹", "⠼", "⠶", "⠧", "⠏", "⠛"}

// messages
type tickMsg time.Time
type projectTickMsg time.Time
type sessionsMsg struct {
	names []string
	// orphaned sessions are killed after orphanConfirmPolls consecutive misses.
	// Nil on a poll that couldn't compute it.
	orphaned []string
	// snap is the tmux pane snapshot of this poll; seq is the grid sequence number
	// read before the snapshot (see grid.Manager.Reconcile).
	snap []tmux.Pane
	seq  int64
	err  error
}
type projectsMsg struct {
	projects []project.Project
	err      error
	// partial carries only the rescanned active projects; the model merges those
	// by path instead of replacing m.projects wholesale.
	partial bool
}
type spinnerMsg struct{}

// reconciledMsg is the result of a grid pass that changed nothing.
type reconciledMsg struct{ err error }

type attentionMsg struct {
	states map[string]status.AttentionState
	hashes map[string]uint64 // session name -> pane fingerprint, for idle tracking
}
type focusedMsg struct{ err error }

// clientsMsg carries the number of tmux clients on the dashboard's session.
type clientsMsg struct {
	n   int
	err error
}

// noClientChecks is the number of consecutive checks without a client after
// which the dashboard stops. One check can fall in a short client change.
const noClientChecks = 2

type savedMsg struct{ err error }

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

func pollCmd(mgr *grid.Manager) tea.Cmd {
	return func() tea.Msg {
		return pollSessions(mgr)
	}
}

// pollSessions lists the live agent sessions and flags those whose anchor
// directory has vanished. A tmux error is returned verbatim so the caller leaves
// the session list untouched.
func pollSessions(mgr *grid.Manager) tea.Msg {
	seq := mgr.Seq()
	snap, err := tmux.Snapshot()
	if err != nil {
		return sessionsMsg{err: err}
	}
	agents := tmux.Agents(snap)
	names := make([]string, 0, len(agents))
	var orphaned []string
	for name, p := range agents {
		names = append(names, name)
		// A deliberately-orphaned session was never bound to a project, so its
		// directory going away isn't a worktree removal.
		if !agent.IsOrphan(name) && isMissingDir(p.Dir) {
			orphaned = append(orphaned, name)
		}
	}
	sort.Strings(names)
	sort.Strings(orphaned)
	return sessionsMsg{names: names, orphaned: orphaned, snap: snap, seq: seq}
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
// Panes are captured in one batched call, falling back to one call per pane so
// a pane that died mid-cycle can't blank the rest. Detached sessions are
// included; tmux keeps their buffers.
func attentionCmd(sessions []string, panes map[string]tmux.Pane) tea.Cmd {
	snap := append([]string(nil), sessions...)
	ids := make([]string, len(snap))
	for i, s := range snap {
		ids[i] = panes[s].ID
	}
	return func() tea.Msg {
		states := make(map[string]status.AttentionState, len(snap))
		hashes := make(map[string]uint64, len(snap))
		texts, err := tmux.CapturePaneIDs(ids)
		if err != nil {
			texts = make(map[string]string, len(ids))
			for _, id := range ids {
				if t := tmux.CapturePaneID(id); t != "" {
					texts[id] = t
				}
			}
		}
		for i, s := range snap {
			text, ok := texts[ids[i]]
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

// reconcileCmd moves agent panes into and out of the grid window and sets the
// layout. snap and seq come from the poll; a nil snap makes the Manager take a
// new snapshot. A pass that changed tmux is followed by a poll, so that the
// dashboard shows the new state at once.
func reconcileCmd(mgr *grid.Manager, snap []tmux.Pane, seq int64, active []string) tea.Cmd {
	want := append([]string(nil), active...)
	return func() tea.Msg {
		changed, err := mgr.Reconcile(snap, seq, want)
		if err != nil {
			return reconciledMsg{err: err}
		}
		if changed {
			return pollSessions(mgr)
		}
		return reconciledMsg{}
	}
}

// clientsCmd reads the number of clients on the dashboard's session.
func clientsCmd(mgr *grid.Manager) tea.Cmd {
	return func() tea.Msg {
		n, err := mgr.Clients()
		return clientsMsg{n: n, err: err}
	}
}

func focusCmd(p tmux.Pane) tea.Cmd {
	return func() tea.Msg {
		return focusedMsg{err: grid.Focus(p)}
	}
}

// zoomCmd shows one agent pane at full window size (tmux zoom).
func zoomCmd(p tmux.Pane) tea.Cmd {
	return func() tea.Msg {
		return focusedMsg{err: grid.Zoom(p)}
	}
}

// setWindowNameCmd renames the grid window off the UI goroutine. The name has
// the "[!!]" prefix while an agent is blocked, so the tmux status line shows it.
// It is best-effort and returns no message.
func setWindowNameCmd(win, scopeDir string, needsAttention bool) tea.Cmd {
	return func() tea.Msg {
		_ = tmux.Chain([][]string{{"rename-window", "-t", win, agent.DashTabTitle(scopeDir, needsAttention)}})
		return nil
	}
}

// setSessionAttnCmd sets or clears the attention mark of an agent, off the UI
// goroutine: the pane option that the grid's pane title line shows, and, for a
// parked agent, the "[!!]" prefix of its own tmux session name. Best-effort and
// message-less. The attentionMsg handler calls it only when the state flips.
func setSessionAttnCmd(p tmux.Pane, session string, needsAttention bool) tea.Cmd {
	return func() tea.Msg {
		mark := ""
		if needsAttention {
			mark = "1"
		}
		if p.ID == "" {
			return nil // the agent is gone; an empty target is the dashboard itself
		}
		_ = tmux.SetPaneOptions(p.ID, tmux.OptAttn, mark)
		// A parked agent has its own session. p can be out of date: a grid pass can
		// move the pane into the dashboard's session after the snapshot. Thus the
		// target is the exact name of the agent's session, not the pane. If the
		// agent is in a grid now, that session is gone and tmux does nothing.
		if p.Grid == "" && tmux.IsAgentSession(p.Session) {
			_ = tmux.RenameSession("="+p.Session, agent.AttnSession(session, needsAttention))
		}
		return nil
	}
}

// openSessionCmd creates an agent in a hidden session of its own, then polls.
// The poll's reconcile moves the new pane into the grid. agentCmd is the command
// the new pane runs.
func openSessionCmd(mgr *grid.Manager, name, dir, agentCmd string) tea.Cmd {
	return func() tea.Msg {
		if _, err := tmux.NewAgentSession(name, dir, agentCmd); err != nil {
			return sessionsMsg{err: err}
		}
		return pollSessions(mgr)
	}
}

// killSessionCmd removes the agent's pane, then polls so the panel and the grid
// update immediately.
func killSessionCmd(mgr *grid.Manager, p tmux.Pane) tea.Cmd {
	return func() tea.Msg {
		if err := tmux.KillPane(p.ID); err != nil {
			return sessionsMsg{err: err}
		}
		return pollSessions(mgr)
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
			return userCommandCmd(vars["dir"], title, holdOnError(runline, title), openCommandWindow)
		default: // "tab"
			return userCommandCmd(vars["dir"], title, holdOnError(runline, title), openCommandWindow)
		}
	}
	return nil
}

// openCommandWindow runs a shell line in a new tmux window in dir.
func openCommandWindow(dir, title, runline string) error {
	return tmux.Chain([][]string{{"new-window", "-c", dir, "-n", title, "sh", "-c", runline}})
}

// holdOnError keeps a failed command's tmux window open with a labeled notice
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

// detachProcessCmd runs runline as a detached background process with no tmux
// window, for fork-and-return GUI apps. The child gets its own process group and
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

// openTabCmd opens a new tmux window that runs a fresh kmux scoped to dir. That
// window is a grid of its own.
func openTabCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		exe, err := os.Executable()
		if err != nil {
			return focusedMsg{err: err}
		}
		return focusedMsg{err: tmux.Chain([][]string{
			{"new-window", "-c", dir, "-n", "kmux::" + filepath.Base(dir), exe, dir},
		})}
	}
}
