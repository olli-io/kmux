package idler

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/olli-io/kmux/internal/agent"
	"github.com/olli-io/kmux/internal/project"
)

// key builds a tea.KeyPressMsg whose .String() is s, for the keys the idler dispatches
// on ("c", "enter", "j", "esc", …).
func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	default:
		return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
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
	out := sized().View().Content
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
	if out := m.View().Content; !strings.Contains(out, "Claude") || !strings.Contains(out, "OpenCode") {
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

// TestDisabledRowsSkipped covers the grey-out UX: a running (disabled) project row
// is never landed on by the cursor and never launches. Here the middle row is
// running for claude, so a claude picker must skip it.
func TestDisabledRowsSkipped(t *testing.T) {
	running := map[string]bool{"claude": true}
	m := model{
		width: 44, height: 20, pendingKind: "claude",
		targets: []target{
			{label: "alpha", dir: "/g/alpha", session: agent.ExpectedSession("/g/alpha", "")},
			{label: "busy", dir: "/g/busy", session: agent.ExpectedSession("/g/busy", ""), running: running},
			{label: "gamma", dir: "/g/gamma", session: agent.ExpectedSession("/g/gamma", "")},
		},
	}

	// The greyed row still renders (visible), tagged with its running kind "[CC]".
	if out := m.View().Content; !strings.Contains(out, "busy") || !strings.Contains(out, "CC") {
		t.Errorf("disabled row should render with a [CC] running tag\n%s", out)
	}

	// Down from alpha (0) skips the running row and lands on gamma (2).
	next, _ := m.Update(key("down"))
	if got := next.(model).pcursor; got != 2 {
		t.Errorf("down from alpha: pcursor = %d, want 2 (skipped running row)", got)
	}

	// Parking the cursor on the disabled row and pressing enter is inert.
	m.pcursor = 1
	next, cmd := m.Update(key("enter"))
	if cmd != nil || next.(model).launch != nil {
		t.Error("enter on a running row should not launch")
	}
}

// TestKindPickerSkipsRunningKind covers the ↵ path: when one kind is already
// running for the chosen project, the kind picker greys it out, starts on the free
// kind, and won't launch the running one.
func TestKindPickerSkipsRunningKind(t *testing.T) {
	base := agent.ExpectedSession("/g/alpha", "")
	m := model{
		width: 44, height: 20, pendingKind: "",
		targets: []target{{label: "alpha", dir: "/g/alpha", session: base,
			running: map[string]bool{"claude": true}}},
	}
	// Enter the kind picker for alpha.
	next, _ := m.Update(key("enter"))
	m = next.(model)
	if m.mode != modeKind {
		t.Fatalf("mode = %d, want modeKind", m.mode)
	}
	// kindOptions[0] is claude (running) → cursor starts on opencode (1).
	if m.kcursor != 1 {
		t.Errorf("kcursor = %d, want 1 (claude is running, start on free kind)", m.kcursor)
	}
	// The greyed claude kind still shows, tagged with its "[CC]" marker.
	if out := m.View().Content; !strings.Contains(out, "CC") {
		t.Errorf("kind picker should tag the running kind with [CC]\n%s", out)
	}
	// Up would move to claude, but it's disabled → cursor stays on opencode.
	up, _ := m.Update(key("up"))
	if got := up.(model).kcursor; got != 1 {
		t.Errorf("up onto running kind: kcursor = %d, want 1 (skipped)", got)
	}
	// Enter launches opencode (the free kind), not claude.
	fin, cmd := m.Update(key("enter"))
	if !isQuit(cmd) {
		t.Fatal("enter should launch the free kind")
	}
	if want := agent.SessionForKind(base, "opencode"); fin.(model).launch.Session != want {
		t.Errorf("launched %q, want opencode session %q", fin.(model).launch.Session, want)
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

	ts := buildTargets(projects, nil)
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
	if ts := buildTargets(nil, nil); len(ts) != 0 {
		t.Errorf("buildTargets(nil) = %d targets, want 0", len(ts))
	}
}

// TestBuildTargetsMarksRunning covers the grey-out semantics: running sessions are
// kept in the list but tagged so disabledFor can skip them per kind. Nothing is
// dropped anymore — the picker shows every project.
func TestBuildTargetsMarksRunning(t *testing.T) {
	projects := []project.Project{
		{Name: "alpha", Path: "/home/u/git/alpha", Branch: "main"},
		{Name: "beta", Path: "/home/u/git/beta", Branch: "main"},
	}
	alphaClaude := agent.ExpectedSession("/home/u/git/alpha", "")
	alphaOpencode := agent.SessionForKind(alphaClaude, "opencode")

	// Only alpha's claude session runs: every project stays listed.
	ts := buildTargets(projects, []string{alphaClaude})
	if labels := labelsOf(ts); !equalStrings(labels, []string{"alpha", "beta"}) {
		t.Fatalf("buildTargets kept = %v, want [alpha beta] (nothing dropped)", labels)
	}
	alpha, beta := ts[0], ts[1]

	// A claude picker disables alpha (its claude session is live) but not beta.
	if !alpha.disabledFor("claude") {
		t.Error("alpha should be disabled for claude (its claude session runs)")
	}
	if beta.disabledFor("claude") {
		t.Error("beta should be launchable for claude (no session runs)")
	}

	// The other kind is still free: an opencode picker keeps alpha launchable.
	if alpha.disabledFor("opencode") {
		t.Error("alpha should be launchable for opencode (only its claude runs)")
	}

	// On the ↵ path (kind == "") a target is disabled only once every kind runs.
	if alpha.disabledFor("") {
		t.Error("alpha should stay launchable on the ↵ path (opencode is free)")
	}

	// With both alpha kinds running, alpha is disabled on the ↵ path too.
	ts = buildTargets(projects, []string{alphaClaude, alphaOpencode})
	if !ts[0].disabledFor("") {
		t.Error("alpha should be disabled on the ↵ path once both kinds run")
	}
	if !ts[0].disabledFor("opencode") {
		t.Error("alpha should be disabled for opencode once its opencode session runs")
	}
}

// TestBuildTargetsSortsRunningFirst covers the hoist: targets with a live session
// sort to the top, stably (scan order preserved within the running and the free
// groups). Here only gamma runs, so it jumps ahead of the earlier-scanned alpha and
// beta while those two keep their relative order.
func TestBuildTargetsSortsRunningFirst(t *testing.T) {
	projects := []project.Project{
		{Name: "alpha", Path: "/home/u/git/alpha", Branch: "main"},
		{Name: "beta", Path: "/home/u/git/beta", Branch: "main"},
		{Name: "gamma", Path: "/home/u/git/gamma", Branch: "main"},
	}
	gammaClaude := agent.ExpectedSession("/home/u/git/gamma", "")

	ts := buildTargets(projects, []string{gammaClaude})
	if labels := labelsOf(ts); !equalStrings(labels, []string{"gamma", "alpha", "beta"}) {
		t.Errorf("sorted order = %v, want [gamma alpha beta] (running first, rest stable)", labels)
	}
}

func labelsOf(ts []target) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.label
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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

// TestAdoptHintRoundTrip covers writing, reading, and removing the adopt hints the
// idler leaves for the dashboard to adopt an in-place launch. It points the hint
// dir at a temp XDG_RUNTIME_DIR so it touches the real filesystem logic.
func TestAdoptHintRoundTrip(t *testing.T) {
	// Isolate the hint dir: adoptDir resolves under config.ConfigDir, which honors
	// XDG_CONFIG_HOME, so this keeps the test off the real ~/.config/kmux.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Missing dir reads as empty, not an error.
	if got, err := ReadAdoptHints(); err != nil || len(got) != 0 {
		t.Fatalf("ReadAdoptHints() on empty = (%v, %v), want (empty, nil)", got, err)
	}

	if err := writeAdoptHint(42, "[kmux][CC]proj"); err != nil {
		t.Fatalf("writeAdoptHint: %v", err)
	}
	if err := writeAdoptHint(7, "[kmux][OC]other"); err != nil {
		t.Fatalf("writeAdoptHint: %v", err)
	}

	hints, err := ReadAdoptHints()
	if err != nil {
		t.Fatalf("ReadAdoptHints: %v", err)
	}
	if hints[42] != "[kmux][CC]proj" || hints[7] != "[kmux][OC]other" || len(hints) != 2 {
		t.Fatalf("ReadAdoptHints() = %v, want {42:[kmux][CC]proj, 7:[kmux][OC]other}", hints)
	}

	// Removing one leaves the other; removing a missing id is not an error.
	if err := RemoveAdoptHint(42); err != nil {
		t.Fatalf("RemoveAdoptHint: %v", err)
	}
	if err := RemoveAdoptHint(999); err != nil {
		t.Fatalf("RemoveAdoptHint(missing) = %v, want nil", err)
	}
	hints, _ = ReadAdoptHints()
	if _, ok := hints[42]; ok || hints[7] != "[kmux][OC]other" || len(hints) != 1 {
		t.Fatalf("after remove ReadAdoptHints() = %v, want {7:[kmux][OC]other}", hints)
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
