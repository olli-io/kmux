package idler

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/olli-io/kmux/internal/agent"
	"github.com/olli-io/kmux/internal/project"
)

// key builds a tea.KeyMsg whose .String() is s, for the keys the idler dispatches
// on ("c", "enter", "j", "esc", …).
func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// sized returns a picker model with a usable viewport and a couple of launch
// targets, the starting point for the render/transition tests. The zero mode is
// modeProject, the picker's entry screen.
func sized() model {
	return model{
		width:  44,
		height: 20,
		targets: []target{
			{label: "alpha", branch: "main", dir: "/g/alpha", session: agent.ExpectedSession("/g/alpha", "")},
			{label: "beta/feat", branch: "feat", dir: "/g/beta.feat", session: agent.ExpectedSession("/g/beta", "feat")},
		},
	}
}

// isQuit reports whether cmd resolves to tea.Quit. Safe to call only for commands
// that aren't a launch (a launch command shells out to tmux when executed).
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestNewModel(t *testing.T) {
	cases := []struct{ kind string }{{""}, {"claude"}, {"opencode"}}
	for _, tc := range cases {
		m := newModel(tc.kind)
		if m.mode != modeProject {
			t.Errorf("newModel(%q): mode = %d, want modeProject", tc.kind, m.mode)
		}
		if m.pendingKind != tc.kind {
			t.Errorf("newModel(%q): pendingKind = %q, want %q", tc.kind, m.pendingKind, tc.kind)
		}
	}
}

func TestProjectPickerRenders(t *testing.T) {
	out := sized().View()
	for _, want := range []string{"Select project", "alpha", "beta/feat"} {
		if !strings.Contains(out, want) {
			t.Errorf("project picker missing %q\n%s", want, out)
		}
	}
}

func TestDirectLaunchFromPicker(t *testing.T) {
	m := sized()
	m.pendingKind = "claude"
	m.pcursor = 1 // beta/feat

	// A preselected kind chooses the launch straight from the project picker and
	// quits; main then execs it in place.
	next, cmd := m.Update(key("enter"))
	m = next.(model)
	if !isQuit(cmd) {
		t.Error("after direct launch: picker should quit")
	}
	if m.launch == nil {
		t.Fatal("after direct launch: expected a chosen launch, got nil")
	}
	wantSession := agent.SessionForKind(m.targets[1].session, "claude")
	if m.launch.Session != wantSession {
		t.Errorf("launch session = %q, want %q", m.launch.Session, wantSession)
	}
	if m.launch.Dir != "/g/beta.feat" {
		t.Errorf("launch dir = %q, want /g/beta.feat", m.launch.Dir)
	}
	if m.launch.AgentCmd != agent.AgentCommand("claude") {
		t.Errorf("launch cmd = %q, want %q", m.launch.AgentCmd, agent.AgentCommand("claude"))
	}
}

func TestEnterPathPicksKindThenLaunches(t *testing.T) {
	m := sized()
	m.pendingKind = "" // the ↵ path
	m.pcursor = 0

	// Selecting a project advances to the kind picker, not a launch.
	next, cmd := m.Update(key("enter"))
	m = next.(model)
	if m.mode != modeKind {
		t.Fatalf("↵ path: after project select mode = %d, want modeKind", m.mode)
	}
	if cmd != nil {
		t.Error("↵ path: project select should not launch yet")
	}
	if m.chosen == nil || m.chosen.label != "alpha" {
		t.Fatalf("↵ path: chosen = %+v, want alpha", m.chosen)
	}

	// The kind picker renders both kinds.
	if out := m.View(); !strings.Contains(out, "Claude") || !strings.Contains(out, "OpenCode") {
		t.Errorf("kind picker missing a kind\n%s", out)
	}

	// Confirming the kind chooses the launch and quits.
	m.kcursor = 1 // OpenCode
	next, cmd = m.Update(key("enter"))
	m = next.(model)
	if !isQuit(cmd) {
		t.Error("↵ path: kind select should quit")
	}
	if m.launch == nil {
		t.Fatal("↵ path: expected a chosen launch")
	}
	if want := agent.SessionForKind(m.targets[0].session, "opencode"); m.launch.Session != want {
		t.Errorf("↵ path: launch session = %q, want %q", m.launch.Session, want)
	}
}

func TestEscCancelsAndBacks(t *testing.T) {
	// From the project picker (the entry screen), esc cancels the whole picker.
	m := sized()
	if _, cmd := m.Update(key("esc")); !isQuit(cmd) {
		t.Error("esc from project picker should quit (cancel)")
	}

	// From the kind picker, esc returns to the project list (not all the way out).
	m = sized()
	m.mode = modeKind
	next, cmd := m.Update(key("esc"))
	if isQuit(cmd) {
		t.Error("esc from kind picker should not quit")
	}
	if next.(model).mode != modeProject {
		t.Error("esc from kind picker should return to the project picker")
	}
}

func TestRunCancelled(t *testing.T) {
	// A model that quits without choosing (esc) yields a nil launch.
	m := sized()
	next, _ := m.Update(key("esc"))
	if next.(model).launch != nil {
		t.Error("cancelled picker should leave launch nil")
	}
}

func TestBuildTargets(t *testing.T) {
	projects := []project.Project{
		{Name: "alpha", Path: "/home/u/git/alpha", Branch: "main"},
		{
			Name:   "beta",
			Path:   "/home/u/git/beta",
			Branch: "main",
			Worktrees: []project.Worktree{
				{Name: "feat", Path: "/home/u/git/beta.feat", Branch: "feat"},
			},
		},
	}

	ts := buildTargets(projects)
	if len(ts) != 3 {
		t.Fatalf("buildTargets: got %d targets, want 3 (2 mains + 1 worktree)", len(ts))
	}

	// Order: each project's main, then its worktrees, in scan order.
	wantLabels := []string{"alpha", "beta", "beta/feat"}
	for i, want := range wantLabels {
		if ts[i].label != want {
			t.Errorf("target %d label = %q, want %q", i, ts[i].label, want)
		}
	}

	// The worktree target's session must match what the dashboard would create,
	// so a session the idler plants is the very same one the dashboard manages.
	wantSession := agent.ExpectedSession("/home/u/git/beta", "feat")
	if ts[2].session != wantSession {
		t.Errorf("worktree session = %q, want %q", ts[2].session, wantSession)
	}
	if ts[2].dir != "/home/u/git/beta.feat" {
		t.Errorf("worktree dir = %q, want the worktree path", ts[2].dir)
	}

	// A main-worktree target carries the no-worktree session and the repo dir.
	if ts[0].session != agent.ExpectedSession("/home/u/git/alpha", "") {
		t.Errorf("alpha session = %q, want the main-worktree session", ts[0].session)
	}
}

func TestBuildTargetsEmpty(t *testing.T) {
	if ts := buildTargets(nil); len(ts) != 0 {
		t.Errorf("buildTargets(nil) = %d targets, want 0", len(ts))
	}
}

func TestScrollWindow(t *testing.T) {
	tests := []struct {
		name               string
		n, cursor, height  int
		wantStart, wantEnd int
	}{
		{"all fit", 3, 0, 5, 0, 3},
		{"exact fit", 5, 4, 5, 0, 5},
		{"cursor centered", 10, 5, 4, 3, 7},
		{"clamp to top", 10, 0, 4, 0, 4},
		{"clamp to bottom", 10, 9, 4, 6, 10},
		{"single row window", 10, 7, 1, 7, 8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start, end := scrollWindow(tc.n, tc.cursor, tc.height)
			if start != tc.wantStart || end != tc.wantEnd {
				t.Errorf("scrollWindow(%d,%d,%d) = (%d,%d), want (%d,%d)",
					tc.n, tc.cursor, tc.height, start, end, tc.wantStart, tc.wantEnd)
			}
			// The window must always contain the cursor and stay in bounds.
			if start < 0 || end > tc.n || start > tc.cursor || tc.cursor >= end {
				t.Errorf("scrollWindow(%d,%d,%d) = (%d,%d): cursor not contained / out of bounds",
					tc.n, tc.cursor, tc.height, start, end)
			}
		})
	}
}

func TestClampInner(t *testing.T) {
	// Fits within the pane: returned unchanged.
	if got := clampInner(20, 40); got != 20 {
		t.Errorf("clampInner(20,40) = %d, want 20", got)
	}
	// Too wide: capped to width-2 (the space between the vertical borders).
	if got := clampInner(50, 30); got != 28 {
		t.Errorf("clampInner(50,30) = %d, want 28", got)
	}
	// Never below 1, even in a degenerate-width pane.
	if got := clampInner(5, 1); got != 1 {
		t.Errorf("clampInner(5,1) = %d, want 1", got)
	}
}
