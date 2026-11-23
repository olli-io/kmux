package grid

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/olli-io/kmux/internal/tmux"
)

// Seams for tests.
var (
	chain     = tmux.Chain
	newHidden = tmux.NewHiddenPane
	setOpts   = tmux.SetPaneOptions
)

// fillerCmd is the command of the filler pane. It shows a static text and waits.
const fillerCmd = `printf '\n  \033[2mfree column\033[0m\n'; exec sleep 2147483647`

// dummyCmd is the command of a dummy pane. It only waits.
const dummyCmd = "exec sleep 2147483647"

// borderFormat is the pane title line of the grid window: the attention mark and
// the agent name.
const borderFormat = " #{?" + tmux.OptAttn + ",#[fg=red bold][!!]#[default] ,}#{?" + tmux.OptName + ",#{" + tmux.OptName + "},#{pane_title}} "

// Manager applies the grid to one tmux window.
type Manager struct {
	mu   sync.Mutex // each tea.Cmd runs in its own goroutine
	win  string
	dash string
	// Result of the last layout-only pass. tmux can adjust a layout; then the same
	// request must not go out again on each poll.
	lastWant, lastGot string
}

// New makes the window of the dashboard pane a grid window.
func New(dashPane, win string) (*Manager, error) {
	err := chain([][]string{
		tmux.SetPaneOption(dashPane, tmux.OptRole, tmux.RoleDash),
		{"set-option", "-w", "-t", win, tmux.OptGrid, "1"},
		{"set-option", "-w", "-t", win, "pane-border-status", "top"},
		{"set-option", "-w", "-t", win, "pane-border-format", borderFormat},
	})
	if err != nil {
		return nil, err
	}
	return &Manager{win: win, dash: dashPane}, nil
}

// Window returns the id of the grid window.
func (m *Manager) Window() string { return m.win }

// Reconcile makes the grid show the wanted agents. It returns true when it sent
// commands to tmux.
func (m *Manager) Reconcile(snap []tmux.Pane, want []string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s := analyse(snap, m.win, want)
	if s.dash == nil {
		return false, errors.New("the dashboard pane is not in the tmux snapshot")
	}
	if s.zoomed {
		return false, nil // a layout change would end the zoom
	}

	// Make the necessary panes out of view. The plan then moves them into view.
	var errs []error
	dummies := s.dummy
	for _, p := range s.parks {
		if dummies[p.Name] != "" {
			continue
		}
		id, err := newHidden(p.Name, p.Dir, dummyCmd)
		if err == nil {
			err = setOpts(id, tmux.OptRole, RoleDummy)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("detach %s: %w", p.Name, err))
			continue
		}
		dummies[p.Name] = id
	}
	filler := ""
	switch {
	case !s.needFiller():
	case s.filler != nil:
		filler = s.filler.ID
	default:
		id, err := newHidden("", "", fillerCmd)
		if err == nil {
			err = setOpts(id, tmux.OptRole, tmux.RoleFiller)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("filler pane: %w", err))
		}
		filler = id
	}

	plan := s.commands(dummies, filler)
	if len(plan.Cmds) == 0 {
		return false, errors.Join(errs...)
	}
	if plan.LayoutOnly && plan.Layout == m.lastWant && s.dash.Layout == m.lastGot {
		return false, errors.Join(errs...)
	}
	if err := chain(plan.Cmds); err != nil {
		errs = append(errs, err)
	}
	m.lastWant, m.lastGot = "", ""
	if plan.LayoutOnly {
		m.lastWant = plan.Layout
		if got, err := windowLayout(m.win); err == nil {
			m.lastGot = got
		}
	}
	return true, errors.Join(errs...)
}

// Close moves all agents out of the grid and removes the filler. The agents
// continue to run in their own sessions.
func (m *Manager) Close(snap []tmux.Pane) error {
	_, err := m.Reconcile(snap, nil)
	m.mu.Lock()
	defer m.mu.Unlock()
	var cmds [][]string
	if snap2, serr := tmux.Snapshot(); serr == nil {
		for _, p := range snap2 {
			if p.Window == m.win && p.Role == tmux.RoleFiller {
				cmds = append(cmds, []string{"kill-pane", "-t", p.ID})
			}
		}
	}
	cmds = append(cmds,
		[]string{"set-option", "-w", "-u", "-t", m.win, tmux.OptGrid},
		[]string{"set-option", "-w", "-u", "-t", m.win, "pane-border-status"},
		[]string{"set-option", "-w", "-u", "-t", m.win, "pane-border-format"},
		tmux.SetPaneOption(m.dash, tmux.OptRole, ""))
	return errors.Join(err, chain(cmds))
}

// Focus selects a pane. The pane can be in a different grid window.
func Focus(p tmux.Pane) error {
	return chain([][]string{
		{"select-window", "-t", p.Window},
		{"select-pane", "-t", p.ID},
	})
}

// Zoom selects a pane and toggles the tmux zoom on it.
func Zoom(p tmux.Pane) error {
	return chain([][]string{
		{"select-window", "-t", p.Window},
		{"select-pane", "-t", p.ID},
		{"resize-pane", "-Z", "-t", p.ID},
	})
}

var windowLayout = func(win string) (string, error) {
	out, err := tmux.Display(win, "#{window_layout}")
	return strings.TrimSpace(out), err
}
