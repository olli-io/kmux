package grid

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/olli-io/kmux/internal/tmux"
)

// RoleDummy marks the temporary pane that holds a new session open while an
// agent pane moves out of the grid into that session.
const RoleDummy = "dummy"

// state is the result of the analysis of one snapshot for one grid window.
type state struct {
	win    string
	dash   *tmux.Pane
	filler *tmux.Pane        // filler to use: in the grid, or a hidden one; nil = none
	order  []string          // current pane order in the window, without killed panes
	kills  []string          // dead panes and filler panes that are not necessary
	parks  []tmux.Pane       // agent panes that must go out of the grid
	cols   [][]string        // target columns
	col    map[string]int    // current OptCol of each pane in cols
	geom   map[string]string // current "WxH,X,Y" of each pane in the window
	zoomed bool
	unzoom bool // end a zoom to apply the plan (used when the dashboard stops)
	// launcher is true when the filler runs an interactive program.
	launcher bool
}

func (s *state) needFiller() bool { return len(s.cols) < maxColumns }

// analyse reads the snapshot. want holds the names of the agents that must be in
// the grid. An agent that is in a different grid window stays there.
func analyse(snap []tmux.Pane, win string, want []string) *state {
	s := &state{win: win, col: map[string]int{}, geom: map[string]string{}}
	wanted := make(map[string]bool, len(want))
	for _, n := range want {
		wanted[n] = true
	}

	var inGrid, outside []tmux.Pane
	for _, p := range snap {
		if p.Window == win {
			inGrid = append(inGrid, p)
		} else {
			outside = append(outside, p)
		}
	}
	sort.Slice(inGrid, func(i, j int) bool { return inGrid[i].Index < inGrid[j].Index })

	columns := make([][]string, maxColumns)
	var pending []string
	present := map[string]bool{}
	for i := range inGrid {
		p := inGrid[i]
		s.geom["%"+strings.TrimPrefix(p.ID, "%")] = fmt.Sprintf("%dx%d,%d,%d", p.W, p.H, p.X, p.Y)
		switch {
		case p.Role == tmux.RoleDash && s.dash == nil:
			s.dash, s.zoomed = &inGrid[i], p.Zoomed
		case p.Role == tmux.RoleFiller && s.filler == nil && !p.Dead:
			s.filler = &inGrid[i]
		case p.Role != "" || (p.Dead && p.Name != ""):
			s.kills = append(s.kills, p.ID)
			continue
		case p.Name != "" && !wanted[p.Name]:
			s.parks = append(s.parks, p)
		case p.Col >= 1 && p.Col <= maxColumns:
			columns[p.Col-1] = append(columns[p.Col-1], p.ID)
			s.col[p.ID] = p.Col
			present[p.Name] = p.Name != ""
		default:
			pending = append(pending, p.ID) // new pane, or a pane that the user made
			present[p.Name] = p.Name != ""
		}
		s.order = append(s.order, p.ID)
	}

	// Parked agents that must come into the grid, in name order.
	sort.Slice(outside, func(i, j int) bool { return outside[i].Name < outside[j].Name })
	var hiddenFiller *tmux.Pane
	for i := range outside {
		p := outside[i]
		switch {
		case p.Dead:
		case p.Role == RoleDummy:
			// Left from a pass that failed. A new pass makes its own dummy panes.
			if p.Grid == "" {
				s.kills = append(s.kills, p.ID)
			}
		case p.Role == tmux.RoleFiller && p.Grid == "" && hiddenFiller == nil:
			hiddenFiller = &outside[i]
		case p.Name != "" && p.Clients > 0:
			// A client shows this agent's own session (`kmux --agent` or a manual
			// attach). The agent comes into the grid when that client detaches.
		case p.Name != "" && p.Grid == "" && wanted[p.Name] && !present[p.Name]:
			pending = append(pending, p.ID)
			present[p.Name] = true
		}
	}

	for _, c := range columns {
		if len(c) > 0 {
			s.cols = append(s.cols, c)
		}
	}
	for _, id := range pending {
		s.cols = place(s.cols, id)
	}
	for {
		id, from, ok := promotable(s.cols)
		if !ok {
			break
		}
		s.cols[from] = s.cols[from][:len(s.cols[from])-1]
		s.cols = append(s.cols, []string{id})
	}

	switch {
	case !s.needFiller():
		if s.filler != nil {
			s.kills = append(s.kills, s.filler.ID)
			s.order = remove(s.order, s.filler.ID)
			s.filler = nil
		}
	case s.filler == nil:
		s.filler = hiddenFiller
	}
	return s
}

// place adds a pane: a new column while there are fewer than maxColumns, then
// below the bottom pane of the shortest column (the leftmost one if equal).
func place(cols [][]string, id string) [][]string {
	if len(cols) < maxColumns {
		return append(cols, []string{id})
	}
	target := 0
	for c := 1; c < len(cols); c++ {
		if len(cols[c]) < len(cols[target]) {
			target = c
		}
	}
	cols[target] = append(cols[target], id)
	return cols
}

// promotable returns a stacked pane to move into a free column: the bottom pane
// of the rightmost stacked column. This keeps the stacks at the left.
func promotable(cols [][]string) (id string, from int, ok bool) {
	if len(cols) >= maxColumns {
		return "", 0, false
	}
	for c := len(cols) - 1; c >= 0; c-- {
		if n := len(cols[c]); n > 1 {
			return cols[c][n-1], c, true
		}
	}
	return "", 0, false
}

// Plan is the set of tmux commands for one pass.
type Plan struct {
	Cmds   [][]string
	Layout string // target layout; "" when the pass does not set one
	// LayoutOnly is true when the only command is select-layout. The Manager uses
	// it to stop a loop when tmux adjusts the layout that kmux sends.
	LayoutOnly bool
}

// commands builds the plan. Each pane in s.parks must have a dummy pane in
// dummies, and filler is the filler pane to use ("" = none).
func (s *state) commands(dummies map[string]string, filler string) Plan {
	if s.dash == nil || (s.zoomed && !s.unzoom) {
		return Plan{}
	}
	var cmds [][]string
	if s.zoomed {
		// resize-pane -Z on a pane of a zoomed window ends the zoom.
		cmds = append(cmds, []string{"resize-pane", "-Z", "-t", s.dash.ID})
	}
	for _, id := range s.kills {
		cmds = append(cmds, []string{"kill-pane", "-t", id})
	}
	order := s.order
	for _, p := range s.parks {
		d := dummies[p.Name]
		if d == "" {
			continue
		}
		cmds = append(cmds, ParkCommands(p, d)...)
		order = remove(order, p.ID)
	}

	want := []string{s.dash.ID}
	for _, c := range s.cols {
		want = append(want, c...)
	}
	if filler != "" {
		want = append(want, filler)
	}
	// The dashboard must be the first pane.
	if len(order) > 0 && order[0] != s.dash.ID {
		cmds = append(cmds, []string{"swap-pane", "-d", "-s", s.dash.ID, "-t", order[0]})
		for i, id := range order {
			if id == s.dash.ID {
				order[0], order[i] = order[i], order[0]
			}
		}
	}
	// Put each pane directly after the pane that comes before it in the target
	// order. This moves a pane in the window and brings a pane into the window.
	for i := 1; i < len(want); i++ {
		if i < len(order) && order[i] == want[i] {
			continue
		}
		cmds = append(cmds,
			[]string{"join-pane", "-d", "-h", "-s", want[i], "-t", want[i-1]},
			// tiled gives each pane sufficient space for the subsequent join.
			[]string{"select-layout", "-t", s.win, "tiled"})
		order = remove(order, want[i])
		order = append(order[:i], append([]string{want[i]}, order[i:]...)...)
	}
	if filler != "" && (s.filler == nil || s.filler.Window != s.win) {
		// -d disables the input of the pane, -e enables it.
		input := "-d"
		if s.launcher {
			input = "-e"
		}
		cmds = append(cmds, []string{"select-pane", input, "-t", filler})
	}
	for c, col := range s.cols {
		for _, id := range col {
			if s.col[id] != c+1 {
				cmds = append(cmds, tmux.SetPaneOption(id, tmux.OptCol, strconv.Itoa(c+1)))
			}
		}
	}

	layout := Layout(s.dash.WinW, s.dash.WinH, s.dash.ID, s.cols, filler)
	if len(cmds) == 0 && s.matches(layout) {
		return Plan{}
	}
	only := len(cmds) == 0
	cmds = append(cmds, []string{"select-layout", "-t", s.win, layout})
	return Plan{Cmds: cmds, Layout: layout, LayoutOnly: only}
}

func remove(list []string, id string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if v != id {
			out = append(out, v)
		}
	}
	return out
}

// leafRe matches one pane cell of a layout string: "WxH,X,Y,ID".
var leafRe = regexp.MustCompile(`(\d+x\d+,\d+,\d+),(\d+)`)

// matches reports whether the panes of the window have the geometry of the
// layout. The comparison uses pane geometry because the format of the tmux
// window_layout variable is not the same in all tmux versions.
func (s *state) matches(layout string) bool {
	leaves := leafRe.FindAllStringSubmatch(layout, -1)
	if len(leaves) != len(s.geom) {
		return false
	}
	for _, l := range leaves {
		if s.geom["%"+l[2]] != l[1] {
			return false
		}
	}
	return true
}

// signature identifies the current geometry of the window.
func (s *state) signature() string {
	ids := make([]string, 0, len(s.geom))
	for id, g := range s.geom {
		ids = append(ids, id+"="+g)
	}
	sort.Strings(ids)
	return fmt.Sprintf("%dx%d %s", s.dash.WinW, s.dash.WinH, strings.Join(ids, " "))
}

// ParkCommands returns the commands that move an agent pane out of a grid into
// a session of its own. dummy is a pane from NewDummy. After the swap the agent
// is the only pane of the dummy's session; that session then gets the agent name.
func ParkCommands(p tmux.Pane, dummy string) [][]string {
	return [][]string{
		{"swap-pane", "-d", "-s", p.ID, "-t", dummy},
		{"kill-pane", "-t", dummy},
		{"rename-session", "-t", p.ID, p.Name},
		tmux.SetPaneOption(p.ID, tmux.OptCol, ""),
	}
}
