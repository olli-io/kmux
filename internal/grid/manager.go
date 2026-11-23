package grid

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/olli-io/kmux/internal/tmux"
)

// Seams for tests.
var (
	chain     = tmux.Chain
	snapshot  = tmux.Snapshot
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
	// The last layout-only request and the geometry that caused it. If tmux does
	// not apply a layout exactly, the same request must not go out on each poll.
	lastWant, lastSeen string
	seq                atomic.Int64 // number of passes that changed tmux
	launcher           string       // command of the filler pane; "" = fillerCmd
}

// SetLauncher makes the filler pane run cmd, an interactive program, in place
// of the static text. An empty cmd changes nothing.
func (m *Manager) SetLauncher(cmd string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.launcher = cmd
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

// Clients returns the number of tmux clients that show the dashboard's session.
func (m *Manager) Clients() (int, error) {
	out, err := tmux.Display(m.dash, "#{session_attached}")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// Window returns the id of the grid window.
func (m *Manager) Window() string { return m.win }

// Seq changes each time a pass changes tmux. A caller reads it before it takes
// a snapshot, so that Reconcile can reject a snapshot that is out of date.
func (m *Manager) Seq() int64 { return m.seq.Load() }

// Reconcile makes the grid show the wanted agents. It returns true when it sent
// commands to tmux. snap must come from after Seq() returned seq; if a different
// pass changed tmux since then, or snap is nil, Reconcile takes a new snapshot.
func (m *Manager) Reconcile(snap []tmux.Pane, seq int64, want []string) (bool, error) {
	return m.reconcile(snap, seq, want, false)
}

func (m *Manager) reconcile(snap []tmux.Pane, seq int64, want []string, closing bool) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if snap == nil || seq != m.seq.Load() {
		var err error
		if snap, err = snapshot(); err != nil {
			return false, err
		}
	}

	s := analyse(snap, m.win, want)
	if s.dash == nil {
		return false, errors.New("the dashboard pane is not in the tmux snapshot")
	}
	if s.unzoom = closing; s.zoomed && !closing {
		return false, nil // a layout change would end the zoom
	}
	if s.dash.Clients == 0 && !closing {
		// No terminal shows this grid. A grid out of view must not take agents.
		return false, nil
	}

	fill := fillerCmd
	if s.launcher = m.launcher != ""; s.launcher {
		fill = m.launcher
	}

	// Make the necessary panes out of view. The plan then moves them into view.
	var errs []error
	dummies := map[string]string{}
	for _, p := range s.parks {
		id, err := NewDummy(p)
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
		id, err := newHidden("", "", fill)
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
	sig := s.signature()
	if plan.LayoutOnly && plan.Layout == m.lastWant && sig == m.lastSeen {
		return false, errors.Join(errs...)
	}
	m.seq.Add(1)
	if err := chain(plan.Cmds); err != nil {
		errs = append(errs, err)
	}
	m.lastWant, m.lastSeen = "", ""
	if plan.LayoutOnly {
		m.lastWant, m.lastSeen = plan.Layout, sig
	}
	return true, errors.Join(errs...)
}

// Close moves all agents out of the grid and removes the filler. The agents
// continue to run in their own sessions.
func (m *Manager) Close() error {
	_, err := m.reconcile(nil, 0, nil, true)
	m.mu.Lock()
	defer m.mu.Unlock()
	var cmds [][]string
	if snap, serr := snapshot(); serr == nil {
		for _, p := range snap {
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

// NewDummy makes the hidden session and pane that ParkCommands needs for the
// agent pane p, and returns the pane id. The temporary session name is not an
// agent name, so no poll takes the dummy for an agent.
func NewDummy(p tmux.Pane) (string, error) {
	id, err := newHidden("kmux-park-"+strings.TrimPrefix(p.ID, "%"), p.Dir, dummyCmd)
	if err != nil {
		return "", err
	}
	return id, setOpts(id, tmux.OptRole, RoleDummy)
}
