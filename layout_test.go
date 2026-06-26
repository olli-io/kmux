package main

import (
	"reflect"
	"testing"

	"github.com/olli-io/kmux/internal/kitty"
)

func TestAgentKind(t *testing.T) {
	cases := map[string]string{
		"proj/wt~cl": "claude",
		"proj/wt~oc": "opencode",
		"scratch":    "",
		"foo~clx":    "",
	}
	for name, want := range cases {
		if got := AgentKind(name); got != want {
			t.Errorf("AgentKind(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestAgentSessionRegex(t *testing.T) {
	match := []string{"a~cl", "a/b~oc", "x/y/z~cl"}
	noMatch := []string{"scratch", "cl", "~clx", "oc_thing"}
	for _, s := range match {
		if !agentSession.MatchString(s) {
			t.Errorf("expected %q to match", s)
		}
	}
	for _, s := range noMatch {
		if agentSession.MatchString(s) {
			t.Errorf("expected %q NOT to match", s)
		}
	}
}

// TestPlacement verifies the column-assignment algorithm: first maxColumns
// agents open new columns (vsplit), then further agents stack (hsplit) under
// the shortest column.
func TestPlacement(t *testing.T) {
	m := NewManager(100) // sidebar window id 100

	// Agent 1 -> col 0, vsplit from sidebar with the sidebar bias.
	loc, match, bias, col := m.placement()
	if loc != kitty.VSplit || match != 100 || bias != sidebarBias || col != 0 {
		t.Fatalf("agent1 placement = (%s,%d,%d,%d)", loc, match, bias, col)
	}
	m.columns = append(m.columns, []int{1}) // pretend launch returned id 1

	// Agent 2 -> col 1, vsplit from rightmost column anchor (id 1).
	loc, match, bias, col = m.placement()
	if loc != kitty.VSplit || match != 1 || bias != 0 || col != 1 {
		t.Fatalf("agent2 placement = (%s,%d,%d,%d)", loc, match, bias, col)
	}
	m.columns = append(m.columns, []int{2})

	// Agent 3 -> col 2, vsplit from rightmost column anchor (id 2).
	loc, match, _, col = m.placement()
	if loc != kitty.VSplit || match != 2 || col != 2 {
		t.Fatalf("agent3 placement = (%s,%d,_,%d)", loc, match, col)
	}
	m.columns = append(m.columns, []int{3})

	// Agent 4 -> hsplit under the shortest column (all len 1 -> leftmost col 0),
	// splitting that column's bottom window (id 1).
	loc, match, _, col = m.placement()
	if loc != kitty.HSplit || match != 1 || col != 0 {
		t.Fatalf("agent4 placement = (%s,%d,_,%d)", loc, match, col)
	}
	m.columns[0] = append(m.columns[0], 4)

	// Agent 5 -> col 0 now has 2, cols 1&2 have 1 -> target col 1, bottom id 2.
	loc, match, _, col = m.placement()
	if loc != kitty.HSplit || match != 2 || col != 1 {
		t.Fatalf("agent5 placement = (%s,%d,_,%d)", loc, match, col)
	}
}

// TestRebalanceTargets verifies the sidebar is pinned to a fixed sidebarWidth
// and the remaining width is split evenly (whole cells) across the columns.
func TestRebalanceTargets(t *testing.T) {
	// 3 columns: total 140, sidebar -> round(0.16*140)=22, each column
	// round(0.28*140)=39 (the last column absorbs the remainder).
	total, ts, tc := rebalanceTargets(50, []int{30, 30, 30})
	if total != 140 || ts != 22 || tc != 39 {
		t.Fatalf("got total=%d sidebar=%d col=%d, want 140/22/39", total, ts, tc)
	}

	// Single column: total 100, sidebar round(0.16*100)=16, column
	// round(0.28*100)=28.
	total, ts, tc = rebalanceTargets(100, []int{0})
	if total != 100 || ts != 16 || tc != 28 {
		t.Fatalf("got total=%d sidebar=%d col=%d, want 100/16/28", total, ts, tc)
	}

	// Degenerate: no columns yields zero targets.
	if _, ts, tc = rebalanceTargets(10, nil); ts != 0 || tc != 0 {
		t.Fatalf("empty columns should give zero targets, got %d/%d", ts, tc)
	}
}

// TestPlaceholderTarget verifies the layout is padded up to maxColumns while
// real agent columns exist, and not otherwise.
func TestPlaceholderTarget(t *testing.T) {
	cases := []struct {
		columns [][]int
		want    int
	}{
		{nil, 0},                       // no agents -> no padding
		{[][]int{{1}}, 2},              // 1 column  -> pad 2
		{[][]int{{1}, {2}}, 1},         // 2 columns -> pad 1
		{[][]int{{1}, {2}, {3}}, 0},    // 3 columns -> full
		{[][]int{{1, 4}, {2}, {3}}, 0}, // stacked, already maxColumns
	}
	for _, tc := range cases {
		m := NewManager(100)
		m.columns = tc.columns
		if got := m.placeholderTarget(); got != tc.want {
			t.Errorf("placeholderTarget(%v) = %d, want %d", tc.columns, got, tc.want)
		}
	}
}

// TestPromotable verifies a stacked pane is chosen for promotion only when a
// column slot is free, picking the bottom of the tallest stack (ties -> left).
func TestPromotable(t *testing.T) {
	cases := []struct {
		columns [][]int
		wantID  int
		wantOK  bool
	}{
		{[][]int{{1}}, 0, false},              // one column, nothing stacked
		{[][]int{{1}, {2}}, 0, false},         // free slot but no stack
		{[][]int{{1, 4}, {2}}, 4, true},       // free slot + stack -> lift bottom (4)
		{[][]int{{1, 4}, {2}, {3}}, 0, false}, // full: no free slot despite stack
		{[][]int{{1, 4}, {2, 5}}, 4, true},    // two stacks tie -> leftmost bottom (4)
		{[][]int{{1}, {2, 5, 6}}, 6, true},    // tallest stack wins -> its bottom (6)
	}
	for _, tc := range cases {
		id, ok := promotable(tc.columns)
		if id != tc.wantID || ok != tc.wantOK {
			t.Errorf("promotable(%v) = (%d,%t), want (%d,%t)", tc.columns, id, ok, tc.wantID, tc.wantOK)
		}
	}
}

// TestColumnAnchors lists real column anchors (top window) first, then
// placeholders, left-to-right.
func TestColumnAnchors(t *testing.T) {
	m := NewManager(100)
	m.columns = [][]int{{1, 4}, {2}}
	m.placeholders = []int{9}
	want := []int{1, 2, 9}
	if got := m.columnAnchors(); !reflect.DeepEqual(got, want) {
		t.Fatalf("columnAnchors() = %v, want %v", got, want)
	}
}

// TestForget removes a window from its column and collapses empty columns.
func TestForget(t *testing.T) {
	m := NewManager(100)
	m.columns = [][]int{{1, 4}, {2}, {3}}
	m.bySession = map[string]int{"a~cl": 1, "b~cl": 4, "c~oc": 2, "d~oc": 3}

	m.forget("c~oc", 2) // empties column index 1
	want := [][]int{{1, 4}, {3}}
	if !reflect.DeepEqual(m.columns, want) {
		t.Fatalf("after forget columns = %v, want %v", m.columns, want)
	}
	if _, ok := m.bySession["c~oc"]; ok {
		t.Fatal("c~oc should be forgotten")
	}
}
