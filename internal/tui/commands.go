package tui

import (
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/olli-io/kmux/internal/agent"
	"github.com/olli-io/kmux/internal/kitty"
	"github.com/olli-io/kmux/internal/layout"
	"github.com/olli-io/kmux/internal/project"
	"github.com/olli-io/kmux/internal/status"
	"github.com/olli-io/kmux/internal/tmux"
)

const pollInterval = 2 * time.Second

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

func tickCmd() tea.Cmd {
	return tea.Tick(pollInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
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
		live, err := kitty.LiveWindowIDs()
		if err != nil {
			live = nil // best-effort: skip the manual-close prune this round
		}
		// A reconcile that adds a pane pulls the kitty app to the macOS foreground
		// even with --keep-focus, stealing system focus from whatever the user was
		// doing. These spawns are automatic (a session appeared on its own, not via
		// a manual open), so capture the frontmost app first and hand focus back
		// afterwards to keep the spawn in the background. Only query when an add is
		// actually pending, so the idle tick stays cheap.
		var prevApp string
		for _, s := range active {
			if !mgr.Attached(s) {
				prevApp = frontmostApp()
				break
			}
		}
		changed, errs := mgr.Reconcile(active, live)
		// Lift stacked panes into any slot a removed column just freed, before
		// padding, so a collapsed split fills the gap instead of an idle slot.
		cchanged, cerrs := mgr.Compact()
		errs = append(errs, cerrs...)
		pchanged, perrs := mgr.SyncPlaceholders(live)
		errs = append(errs, perrs...)
		if changed || cchanged || pchanged {
			errs = append(errs, mgr.Rebalance()...)
		}
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
		if err := mgr.Open(name, dir, agentCmd); err != nil {
			return reconciledMsg{errs: []error{err}}
		}
		live, err := kitty.LiveWindowIDs()
		if err != nil {
			live = nil // best-effort: skip the manual-close prune this round
		}
		_, errs := mgr.SyncPlaceholders(live)
		errs = append(errs, mgr.Rebalance()...)
		return reconciledMsg{errs: errs}
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
		if err := mgr.Reattach(name); err != nil {
			return reconciledMsg{errs: []error{err}}
		}
		live, err := kitty.LiveWindowIDs()
		if err != nil {
			live = nil // best-effort: skip the manual-close prune this round
		}
		_, errs := mgr.SyncPlaceholders(live)
		errs = append(errs, mgr.Rebalance()...)
		return reconciledMsg{errs: errs}
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

// lazygitCmd opens lazygit for dir in a new kitty tab.
func lazygitCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		return focusedMsg{err: kitty.OpenLazygit(dir)}
	}
}

// editorCmd opens (or focuses) an editor for dir off the UI goroutine.
func editorCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		return focusedMsg{err: openEditor(dir)}
	}
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
