package grid

import (
	"reflect"
	"strings"
	"testing"

	"github.com/olli-io/kmux/internal/tmux"
)

func TestChecksum(t *testing.T) {
	// Reference value from tmux: a single 80x24 pane with id 0.
	if got := withChecksum("80x24,0,0,0"); got != "b25d,80x24,0,0,0" {
		t.Errorf("got %s", got)
	}
}

func TestLayout(t *testing.T) {
	cases := []struct {
		name   string
		cols   [][]string
		filler string
		want   string // layout without checksum
	}{
		{"dash only", nil, "", "200x48,0,0,1"},
		{"no agent", nil, "%9", "200x48,0,0{32x48,0,0,1,167x48,33,0,9}"},
		{"one column", [][]string{{"%2"}}, "%9", "200x48,0,0{32x48,0,0,1,55x48,33,0,2,111x48,89,0,9}"},
		{"stack and full", [][]string{{"%2", "%3"}, {"%4"}, {"%5"}}, "",
			"200x48,0,0{32x48,0,0,1,55x48,33,0[55x23,33,0,2,55x24,33,24,3],55x48,89,0,4,55x48,145,0,5}"},
	}
	for _, c := range cases {
		got := Layout(200, 48, "%1", c.cols, c.filler)
		if _, body, _ := strings.Cut(got, ","); body != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, body, c.want)
		}
	}
}

func TestLayoutFillsWindow(t *testing.T) {
	// For each number of panes the cells and separators must add up to the window.
	for n := 0; n <= 7; n++ {
		var cols [][]string
		for i := 0; i < n; i++ {
			cols = place(cols, "%"+string(rune('2'+i)))
		}
		filler := ""
		if len(cols) < maxColumns {
			filler = "%9"
		}
		got := Layout(213, 57, "%1", cols, filler)
		if strings.Contains(got, "x0,") || strings.Contains(got, ",-") {
			t.Errorf("n=%d: bad cell in %s", n, got)
		}
	}
}

func TestPlaceAndPromote(t *testing.T) {
	var cols [][]string
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		cols = place(cols, id)
	}
	want := [][]string{{"a", "d"}, {"b", "e"}, {"c"}}
	if !reflect.DeepEqual(cols, want) {
		t.Fatalf("place: got %v, want %v", cols, want)
	}
	if _, _, ok := promotable(cols); ok {
		t.Error("promotable with all columns in use")
	}
	if id, from, ok := promotable([][]string{{"a", "d"}, {"b", "e"}}); !ok || id != "e" || from != 1 {
		t.Errorf("promotable: got %s from %d", id, from)
	}
}

const win = "@1"

func dash() tmux.Pane {
	return tmux.Pane{ID: "%1", Role: tmux.RoleDash, Window: win, Grid: "1", WinW: 200, WinH: 48}
}
func agent(id, name string, idx, col int) tmux.Pane {
	return tmux.Pane{ID: id, Name: name, Dir: "/p/" + name, Window: win, Grid: "1", Index: idx, Col: col}
}
func parked(id, name string) tmux.Pane {
	return tmux.Pane{ID: id, Name: name, Dir: "/p/" + name, Window: "@" + id, Session: name, Panes: 1}
}
func flat(cmds [][]string) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = strings.Join(c, " ")
	}
	return out
}

func TestPlanAttach(t *testing.T) {
	snap := []tmux.Pane{dash(), parked("%2", "A"), parked("%3", "B")}
	s := analyse(snap, win, []string{"A", "B"})
	if !s.needFiller() || s.filler != nil {
		t.Fatalf("filler state wrong: need=%v filler=%v", s.needFiller(), s.filler)
	}
	p := s.commands(nil, "%9")
	want := []string{
		"join-pane -d -h -s %2 -t %1", "select-layout -t @1 tiled",
		"join-pane -d -h -s %3 -t %2", "select-layout -t @1 tiled",
		"join-pane -d -h -s %9 -t %3", "select-layout -t @1 tiled",
		"select-pane -d -t %9",
		"set-option -p -t %2 @kmux_col 1",
		"set-option -p -t %3 @kmux_col 2",
		"select-layout -t @1 " + Layout(200, 48, "%1", [][]string{{"%2"}, {"%3"}}, "%9"),
	}
	if got := flat(p.Cmds); !reflect.DeepEqual(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestPlanLauncherFillerTakesInput(t *testing.T) {
	s := analyse([]tmux.Pane{dash(), parked("%2", "A")}, win, []string{"A"})
	s.launcher = true
	got := strings.Join(flat(s.commands(nil, "%9").Cmds), "\n")
	if !strings.Contains(got, "select-pane -e -t %9") || strings.Contains(got, "select-pane -d") {
		t.Errorf("got:\n%s", got)
	}
}

func TestPlanStable(t *testing.T) {
	d := dash()
	a, b, c := agent("%2", "A", 1, 1), agent("%3", "B", 2, 2), agent("%4", "C", 3, 3)
	// Geometry of Layout(200, 48, ...) with three columns.
	d.W, d.H = 32, 48
	a.X, a.W, a.H = 33, 55, 48
	b.X, b.W, b.H = 89, 55, 48
	c.X, c.W, c.H = 145, 55, 48
	s := analyse([]tmux.Pane{d, a, b, c}, win, []string{"A", "B", "C"})
	if p := s.commands(nil, ""); len(p.Cmds) != 0 {
		t.Errorf("stable grid gave commands: %v", flat(p.Cmds))
	}
	// A new window size gives a layout-only plan.
	d.WinW = 180
	s = analyse([]tmux.Pane{d, a, b, c}, win, []string{"A", "B", "C"})
	if p := s.commands(nil, ""); !p.LayoutOnly || len(p.Cmds) != 1 {
		t.Errorf("resize: got %v", flat(p.Cmds))
	}
}

func TestPlanParkAndPromote(t *testing.T) {
	// (A,D)|B|C and B goes out: D moves up into a column of its own.
	snap := []tmux.Pane{dash(), agent("%2", "A", 1, 1), agent("%5", "D", 2, 1),
		agent("%3", "B", 3, 2), agent("%4", "C", 4, 3)}
	s := analyse(snap, win, []string{"A", "C", "D"})
	if len(s.parks) != 1 || s.parks[0].Name != "B" {
		t.Fatalf("parks = %v", s.parks)
	}
	if want := [][]string{{"%2"}, {"%4"}, {"%5"}}; !reflect.DeepEqual(s.cols, want) {
		t.Fatalf("cols = %v, want %v", s.cols, want)
	}
	got := flat(s.commands(map[string]string{"B": "%7"}, "").Cmds)
	want := []string{
		"swap-pane -d -s %3 -t %7", "kill-pane -t %7", "rename-session -t %3 B", "set-option -p -u -t %3 @kmux_col",
		"join-pane -d -h -s %4 -t %2", "select-layout -t @1 tiled",
		"set-option -p -t %4 @kmux_col 2",
		"set-option -p -t %5 @kmux_col 3",
	}
	if !reflect.DeepEqual(got[:len(got)-1], want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestPlanGuestAndOtherGrid(t *testing.T) {
	guest := tmux.Pane{ID: "%6", Window: win, Grid: "1", Index: 1}
	other := tmux.Pane{ID: "%7", Name: "B", Window: "@2", Grid: "1", Col: 1}
	s := analyse([]tmux.Pane{dash(), guest, other, parked("%2", "A")}, win, []string{"A", "B"})
	if want := [][]string{{"%6"}, {"%2"}}; !reflect.DeepEqual(s.cols, want) {
		t.Errorf("cols = %v, want %v (guest stays, agent of other grid stays there)", s.cols, want)
	}
}

func TestPlanKillsFillerWhenFull(t *testing.T) {
	f := tmux.Pane{ID: "%9", Role: tmux.RoleFiller, Window: win, Grid: "1", Index: 3}
	snap := []tmux.Pane{dash(), agent("%2", "A", 1, 1), agent("%3", "B", 2, 2), f, parked("%4", "C")}
	s := analyse(snap, win, []string{"A", "B", "C"})
	got := flat(s.commands(nil, "").Cmds)
	if got[0] != "kill-pane -t %9" || got[1] != "join-pane -d -h -s %4 -t %3" {
		t.Errorf("got %v", got)
	}
}

func TestZoomedGridIsNotChanged(t *testing.T) {
	d := dash()
	d.Zoomed = true
	s := analyse([]tmux.Pane{d, parked("%2", "A")}, win, []string{"A"})
	if p := s.commands(nil, "%9"); len(p.Cmds) != 0 {
		t.Errorf("zoomed grid gave commands: %v", flat(p.Cmds))
	}
}

func TestCloseEndsZoomAndParksAll(t *testing.T) {
	d := dash()
	d.Zoomed = true
	s := analyse([]tmux.Pane{d, agent("%2", "A", 1, 1)}, win, nil)
	s.unzoom = true
	got := flat(s.commands(map[string]string{"A": "%7"}, "%9").Cmds)
	if got[0] != "resize-pane -Z -t %1" || got[1] != "swap-pane -d -s %2 -t %7" {
		t.Errorf("got %v", got)
	}
}

func TestLeftoverDummyIsKilled(t *testing.T) {
	left := tmux.Pane{ID: "%8", Role: RoleDummy, Window: "@5", Session: "kmux-park-3", Panes: 1}
	s := analyse([]tmux.Pane{dash(), left}, win, nil)
	if len(s.kills) != 1 || s.kills[0] != "%8" {
		t.Errorf("kills = %v", s.kills)
	}
}

func TestAgentWithClientGetsView(t *testing.T) {
	p := parked("%2", "A")
	p.Clients, p.Ext = 1, 1
	s := analyse([]tmux.Pane{dash(), p}, win, []string{"A"})
	if len(s.views) != 1 || len(s.cols) != 1 || s.cols[0][0] != viewKey("A") {
		t.Fatalf("views = %v, cols = %v; want one view placeholder", s.views, s.cols)
	}
	s.setView("A", "%9")
	got := strings.Join(flat(s.commands(nil, "").Cmds), "\n")
	if !strings.Contains(got, "join-pane -d -h -s %9 -t %1") || strings.Contains(got, "%2") {
		t.Errorf("the view pane must come in and the agent pane must stay:\n%s", got)
	}

	// A view pane that the Manager could not make leaves no column.
	s = analyse([]tmux.Pane{dash(), p}, win, []string{"A"})
	if s.setView("A", ""); len(s.cols) != 0 {
		t.Errorf("cols = %v, want none", s.cols)
	}
}

func view(id, name string, idx, col int) tmux.Pane {
	return tmux.Pane{ID: id, View: name, Window: win, Grid: "1", Index: idx, Col: col}
}

func TestViewStaysWhileTerminalShowsAgent(t *testing.T) {
	p := parked("%2", "A")
	p.Clients, p.Ext = 2, 1 // the terminal and the view pane
	s := analyse([]tmux.Pane{dash(), view("%9", "A", 1, 2), p}, win, []string{"A"})
	if len(s.kills) != 0 || len(s.views) != 0 || len(s.cols) != 1 || s.cols[0][0] != "%9" {
		t.Errorf("kills = %v, views = %v, cols = %v; want the view pane only", s.kills, s.views, s.cols)
	}
}

func TestAgentReplacesViewWhenTerminalDetaches(t *testing.T) {
	p := parked("%2", "A")
	p.Clients, p.Ext = 1, 0 // the view pane only
	b := agent("%3", "B", 1, 1)
	s := analyse([]tmux.Pane{dash(), b, view("%9", "A", 2, 2), p}, win, []string{"A", "B"})
	want := [][]string{{"%3"}, {"%2"}}
	if len(s.kills) != 1 || s.kills[0] != "%9" || !reflect.DeepEqual(s.cols, want) {
		t.Errorf("kills = %v, cols = %v; want kill %%9 and cols %v", s.kills, s.cols, want)
	}
}

func TestViewGoesWithItsAgent(t *testing.T) {
	// The agent stopped, or the dashboard does not want it.
	s := analyse([]tmux.Pane{dash(), view("%9", "A", 1, 1)}, win, []string{"A"})
	if len(s.kills) != 1 || len(s.cols) != 0 {
		t.Errorf("kills = %v, cols = %v", s.kills, s.cols)
	}
	p := parked("%2", "A")
	p.Clients, p.Ext = 2, 1
	s = analyse([]tmux.Pane{dash(), view("%9", "A", 1, 1), p}, win, nil)
	if len(s.kills) != 1 || len(s.cols) != 0 || len(s.parks) != 0 {
		t.Errorf("kills = %v, cols = %v, parks = %v", s.kills, s.cols, s.parks)
	}
}

func TestKillSession(t *testing.T) {
	oldChain, oldSnap := chain, snapshot
	defer func() { chain, snapshot = oldChain, oldSnap }()
	var got []string
	chain = func(cmds [][]string) error { got = flat(cmds); return nil }

	d := dash()
	d.Session = "[kmux][dash]~/git"
	other := tmux.Pane{ID: "%7", Window: "@7", Session: d.Session} // a second window
	hidden := tmux.Pane{ID: "%8", Role: tmux.RoleFiller, Window: "@8", Session: "5"}
	snapshot = func() ([]tmux.Pane, error) { return []tmux.Pane{d, other, hidden, parked("%2", "A")}, nil }
	m := &Manager{win: win, dash: d.ID}
	if err := m.KillSession(); err != nil {
		t.Fatal(err)
	}
	want := []string{"kill-pane -t %8", "kill-session -t =[kmux][dash]~/git"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// An agent that is still in the session keeps the session.
	got = nil
	a := agent("%2", "A", 1, 1)
	a.Session = d.Session
	snapshot = func() ([]tmux.Pane, error) { return []tmux.Pane{d, a}, nil }
	if err := m.KillSession(); err == nil || got != nil {
		t.Errorf("err = %v, commands = %v; want an error and no command", err, got)
	}
}
