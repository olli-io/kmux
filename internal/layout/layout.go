package layout

import (
	"math"
	"sort"

	"github.com/olli-io/kmux/internal/kitty"
	"github.com/olli-io/kmux/internal/tmux"
)

const (
	maxColumns  = 3  // sidebar + up to 3 agent columns of vertical splits
	sidebarBias = 85 // % given to the first agent column on creation

	// Target pane fractions of the tab width, converged toward by Rebalance.
	// The layout is always sidebar + maxColumns columns (real agent panes padded
	// with placeholders), so these sum to 1: 0.16 + 3*0.28 = 1.0.
	sidebarFrac = 0.16 // fraction of the tab width pinned to the sidebar
	agentFrac   = 0.28 // fraction of the tab width per agent column
)

// placeholderTitle labels the inert filler panes that pad the layout up to
// maxColumns so that real agent panes always render at the same fixed width.
const placeholderTitle = "·idle"

// placeholderCmd is the (inert) command a placeholder pane runs: it shows a
// dim hint and then sleeps forever, never touching the agent's tmux session.
func placeholderCmd() []string {
	return []string{
		"sh", "-c",
		`clear; printf '\n  \033[2midle slot\033[0m\n  \033[2m(reserved to keep agent panes a fixed width)\033[0m\n'; while :; do sleep 86400; done`,
	}
}

// Manager owns the mapping between tmux agent sessions and the kitty windows
// (panes) attached to them, plus the column layout state.
type Manager struct {
	sidebarID    int            // KITTY_WINDOW_ID; the kmux sidebar itself
	columns      [][]int        // up to maxColumns; each is window ids top->bottom
	placeholders []int          // filler panes padding the layout to maxColumns
	bySession    map[string]int // session name -> window id
}

func NewManager(sidebarID int) *Manager {
	return &Manager{
		sidebarID: sidebarID,
		bySession: make(map[string]int),
	}
}

// Sessions returns the currently tracked session names, sorted.
func (m *Manager) Sessions() []string {
	names := make([]string, 0, len(m.bySession))
	for name := range m.bySession {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Attached reports whether a session currently has a pane.
func (m *Manager) Attached(session string) bool {
	_, ok := m.bySession[session]
	return ok
}

// WindowID returns the kitty window id attached to session, if any.
func (m *Manager) WindowID(session string) (int, bool) {
	id, ok := m.bySession[session]
	return id, ok
}

// Reconcile makes the live panes match the set of active sessions: it attaches
// panes for new sessions and closes panes for vanished ones. It also prunes
// panes the user closed manually (detected via the live id set). It reports
// whether the pane layout changed (so the caller can trigger a Rebalance).
// Errors from individual kitty calls are collected and returned together;
// reconcile is best-effort and continues past failures.
func (m *Manager) Reconcile(active []string, live map[int]bool) (changed bool, errs []error) {
	// Prune panes the user closed by hand so our state stays truthful.
	if live != nil {
		for session, id := range m.bySession {
			if !live[id] {
				m.forget(session, id)
				changed = true
			}
		}
	}

	activeSet := make(map[string]bool, len(active))
	for _, s := range active {
		activeSet[s] = true
	}

	// Remove panes for sessions that disappeared.
	for session, id := range m.bySession {
		if !activeSet[session] {
			if err := kitty.CloseWindow(id); err != nil {
				errs = append(errs, err)
			}
			m.forget(session, id)
			changed = true
		}
	}

	// Add panes for new sessions (sorted for deterministic column assignment).
	sort.Strings(active)
	for _, session := range active {
		if m.Attached(session) {
			continue
		}
		if err := m.add(session); err != nil {
			errs = append(errs, err)
			continue
		}
		changed = true
	}
	return changed, errs
}

// Open ensures a detached tmux session named `name` (running agentCmd in dir)
// exists, then attaches a pane for it and records it in the layout. If the
// session is already attached it is a no-op; callers should focus it instead.
func (m *Manager) Open(name, dir, agentCmd string) error {
	if m.Attached(name) {
		return nil
	}
	if err := tmux.NewDetachedSession(name, dir, agentCmd); err != nil {
		return err
	}
	return m.add(name)
}

// Reattach attaches a fresh pane to an already-running session, without creating
// a tmux session. It is used to re-open a pane the user closed by hand (or
// otherwise lost) for a session that is still live. It is a no-op when the
// session is already attached; callers should focus it instead.
func (m *Manager) Reattach(session string) error {
	if m.Attached(session) {
		return nil
	}
	return m.add(session)
}

// add launches a pane for session and records it in the layout.
func (m *Manager) add(session string) error {
	// When a placeholder slot is reserved, a new agent column must take it over
	// rather than carve space out of an existing column: splitting a real column
	// would trap both agents inside that column's single slot (two thin panes),
	// and no later resize can free them. Consuming the placeholder lands the new
	// column in a full-width slot of its own.
	if len(m.columns) < maxColumns && len(m.placeholders) > 0 {
		return m.addInPlaceholderSlot(session)
	}

	loc, matchID, bias, col := m.placement()
	id, err := kitty.Launch(loc, matchID, bias, session, "tmux", "attach", "-t", session)
	if err != nil {
		return err
	}
	if col == len(m.columns) {
		m.columns = append(m.columns, []int{id})
	} else {
		m.columns[col] = append(m.columns[col], id)
	}
	m.bySession[session] = id
	return nil
}

// addInPlaceholderSlot launches a new agent column into the leftmost reserved
// placeholder's slot: it splits that placeholder (so the new pane shares its
// slot) and then closes the placeholder (so the new pane absorbs the whole
// slot). The result is a fixed-width column instead of a half-width split of an
// existing agent column.
func (m *Manager) addInPlaceholderSlot(session string) error {
	ph := m.placeholders[0]
	id, err := kitty.Launch(kitty.VSplit, ph, 0, session, "tmux", "attach", "-t", session)
	if err != nil {
		return err
	}
	// Drop the placeholder so the new column expands to fill its slot. Best
	// effort: even if the close call fails, SyncPlaceholders prunes it later.
	_ = kitty.CloseWindow(ph)
	m.placeholders = m.placeholders[1:]
	m.columns = append(m.columns, []int{id})
	m.bySession[session] = id
	return nil
}

// placement decides where the next pane goes:
//   - fewer than maxColumns columns -> open a NEW column via vsplit
//   - otherwise -> STACK via hsplit under the column with the fewest panes
//
// It returns the split location, the window id to split from, the bias, and the
// index of the target column.
func (m *Manager) placement() (kitty.SplitLocation, int, int, int) {
	if len(m.columns) < maxColumns {
		col := len(m.columns)
		if col == 0 {
			// First agent column splits the sidebar; bias keeps sidebar narrow.
			return kitty.VSplit, m.sidebarID, sidebarBias, col
		}
		// New column lands to the right of the current rightmost column.
		rightmostAnchor := m.columns[col-1][0]
		return kitty.VSplit, rightmostAnchor, 0, col
	}

	// All columns exist: stack under the shortest one (ties -> leftmost).
	target := 0
	for c := 1; c < len(m.columns); c++ {
		if len(m.columns[c]) < len(m.columns[target]) {
			target = c
		}
	}
	bottom := m.columns[target][len(m.columns[target])-1]
	return kitty.HSplit, bottom, 0, target
}

// sessionFor returns the session name attached to a window id, or "" if none.
func (m *Manager) sessionFor(id int) string {
	for session, wid := range m.bySession {
		if wid == id {
			return session
		}
	}
	return ""
}

// promotable returns the window id of a stacked pane that should be lifted into
// its own column, and whether one exists. A pane is promotable when the layout
// has a free column slot (fewer than maxColumns columns) yet some column still
// holds more than one pane: it picks the bottom pane of the *rightmost* such
// column. The freed slot is always added on the right (see add/placement), so
// pulling from the rightmost stack keeps horizontal splits packed on the left:
// e.g. (A-B)|(C-D)|E losing E lifts D, yielding (A-B)|C|D rather than A|(C-D)|B.
// This is what makes detaching a single-pane column collapse a horizontal split
// into the freed slot instead of leaving an idle placeholder.
func promotable(columns [][]int) (id int, ok bool) {
	if len(columns) >= maxColumns {
		return 0, false
	}
	for c := len(columns) - 1; c >= 0; c-- {
		if len(columns[c]) > 1 {
			col := columns[c]
			return col[len(col)-1], true
		}
	}
	return 0, false
}

// Compact lifts stacked agent panes into free column slots so that detaching a
// column collapses a horizontal split rather than leaving an idle slot behind.
// While a slot is free (fewer than maxColumns columns) and some column is still
// stacked, it moves the bottom pane of the tallest stack into a new column of its
// own. Moving a pane means closing its kitty window (which only detaches tmux)
// and re-attaching it as a fresh column via add. It reports whether anything
// changed (so the caller can Rebalance) and collects per-window errors; like
// Reconcile it is best-effort.
func (m *Manager) Compact() (changed bool, errs []error) {
	for {
		id, ok := promotable(m.columns)
		if !ok {
			return changed, errs
		}
		session := m.sessionFor(id)
		if session == "" {
			return changed, errs // unknown id; avoid spinning
		}
		// Detach the stacked pane and re-attach it as its own column. add lands it
		// in a free slot (vsplit) since columns < maxColumns here.
		if err := kitty.CloseWindow(id); err != nil {
			errs = append(errs, err)
		}
		m.forget(session, id)
		if err := m.add(session); err != nil {
			errs = append(errs, err)
			return changed, errs
		}
		changed = true
	}
}

// forget removes a window id from the session map and its column.
func (m *Manager) forget(session string, id int) {
	delete(m.bySession, session)
	for c := range m.columns {
		for i, wid := range m.columns[c] {
			if wid == id {
				m.columns[c] = append(m.columns[c][:i], m.columns[c][i+1:]...)
				break
			}
		}
	}
	// Drop now-empty columns so future adds reuse the freed slots.
	cleaned := m.columns[:0]
	for _, col := range m.columns {
		if len(col) > 0 {
			cleaned = append(cleaned, col)
		}
	}
	m.columns = cleaned
}

// placeholderTarget is how many filler panes the layout should currently hold:
// enough to keep the agent area at maxColumns columns so the dashboard and any
// real agent panes stay a fixed width. It holds even with zero agents: an idle
// dashboard shows the sidebar beside maxColumns idle slots rather than a lone
// sidebar stretched across the whole tab (and rather than degrading to a single
// wide pane as sessions are reaped one by one). Once the columns stack
// (>= maxColumns) the width is already fixed and no padding is needed.
func (m *Manager) placeholderTarget() int {
	if len(m.columns) >= maxColumns {
		return 0
	}
	return maxColumns - len(m.columns)
}

// columnAnchors returns one window id per agent column, real columns first then
// placeholders, left-to-right. A real column's anchor is its top window; a
// placeholder is its own anchor. Rebalance evens these out so each occupies an
// equal fixed slice of the agent area.
func (m *Manager) columnAnchors() []int {
	anchors := make([]int, 0, len(m.columns)+len(m.placeholders))
	for _, col := range m.columns {
		anchors = append(anchors, col[0])
	}
	return append(anchors, m.placeholders...)
}

// rightmostAnchor is the window to vsplit a new rightmost column from: the last
// placeholder, else the last real column, else the sidebar.
func (m *Manager) rightmostAnchor() int {
	if n := len(m.placeholders); n > 0 {
		return m.placeholders[n-1]
	}
	if n := len(m.columns); n > 0 {
		return m.columns[n-1][0]
	}
	return m.sidebarID
}

// SyncPlaceholders adds or removes filler panes so the agent area always holds
// maxColumns columns (real + placeholder) while any agent is active, keeping
// real agent panes at a constant width. It first prunes placeholders the user
// closed by hand (via the live id set), then converges to placeholderTarget. It
// reports whether anything changed (so the caller can Rebalance) and collects
// per-window errors; like Reconcile it is best-effort.
func (m *Manager) SyncPlaceholders(live map[int]bool) (changed bool, errs []error) {
	if live != nil {
		kept := m.placeholders[:0]
		for _, id := range m.placeholders {
			if live[id] {
				kept = append(kept, id)
			} else {
				changed = true
			}
		}
		m.placeholders = kept
	}

	want := m.placeholderTarget()

	// Close surplus placeholders from the right (the freed column is reused by
	// the real agent that just claimed it).
	for len(m.placeholders) > want {
		last := m.placeholders[len(m.placeholders)-1]
		if err := kitty.CloseWindow(last); err != nil {
			errs = append(errs, err)
		}
		m.placeholders = m.placeholders[:len(m.placeholders)-1]
		changed = true
	}

	// Open missing placeholders as new rightmost columns.
	for len(m.placeholders) < want {
		id, err := kitty.Launch(kitty.VSplit, m.rightmostAnchor(), 0, placeholderTitle, placeholderCmd()...)
		if err != nil {
			errs = append(errs, err)
			break
		}
		m.placeholders = append(m.placeholders, id)
		changed = true
	}
	return changed, errs
}

// rebalanceTargets computes the target sidebar and per-column widths (in cells)
// as fixed fractions of the total tab width, given the current sidebar width and
// the per-column widths. Total text-columns is invariant under resizing, so
// these absolute targets can be computed once and then converged toward. The
// last column absorbs any rounding remainder.
func rebalanceTargets(curSidebar int, colWidths []int) (total, targetSidebar, targetCol int) {
	total = curSidebar
	for _, w := range colWidths {
		total += w
	}
	if total <= 0 || len(colWidths) == 0 {
		return total, 0, 0
	}
	targetSidebar = int(math.Round(sidebarFrac * float64(total)))
	targetCol = int(math.Round(agentFrac * float64(total)))
	if targetSidebar < 1 {
		targetSidebar = 1
	}
	if targetCol < 1 {
		targetCol = 1
	}
	return total, targetSidebar, targetCol
}

// rebalanceTolerance is how many cells off-target a window may be before
// Rebalance stops trying to correct it.
const rebalanceTolerance = 1

// rebalanceMaxPasses caps how many convergence passes Rebalance makes. A single
// relative-resize pass often under-shoots (kitty's resize-window does not always
// move the full requested delta in one call, and an unbalanced split tree needs
// several nudges), so we repeat until widths settle or this cap is hit.
const rebalanceMaxPasses = 6

// Rebalance sizes the sidebar and agent columns to their target fractions of the
// tab width (sidebarFrac / agentFrac). It resizes the sidebar and every column
// but the last (which absorbs the remainder), re-reading live widths before each
// step so the relative resizes converge regardless of the underlying split tree
// shape, and repeats the whole pass until every window is within
// rebalanceTolerance of its target or rebalanceMaxPasses is reached.
func (m *Manager) Rebalance() []error {
	anchors := m.columnAnchors()
	if len(anchors) == 0 {
		return nil
	}

	type step struct{ id, target int }
	var errs []error
	for pass := 0; pass < rebalanceMaxPasses; pass++ {
		widths, err := kitty.WindowColumns()
		if err != nil {
			return append(errs, err)
		}
		colWidths := make([]int, len(anchors))
		for i, a := range anchors {
			colWidths[i] = widths[a] // a column's width == its anchor's width
		}
		_, targetSidebar, targetCol := rebalanceTargets(widths[m.sidebarID], colWidths)
		if targetSidebar == 0 {
			return errs
		}

		// Resize order: sidebar first, then every column except the last (which
		// absorbs the rounding remainder). Placeholders are columns too.
		steps := []step{{m.sidebarID, targetSidebar}}
		for i := 0; i < len(anchors)-1; i++ {
			steps = append(steps, step{anchors[i], targetCol})
		}

		converged := true
		for _, s := range steps {
			// Re-read because each resize shifts the widths of the windows to its right.
			cur, err := kitty.WindowColumns()
			if err != nil {
				errs = append(errs, err)
				continue
			}
			delta := s.target - cur[s.id]
			if delta > rebalanceTolerance || delta < -rebalanceTolerance {
				converged = false
			}
			if err := kitty.ResizeWindowHoriz(s.id, delta); err != nil {
				errs = append(errs, err)
			}
		}
		if converged {
			break
		}
	}
	return errs
}

// CloseAll closes every pane kmux spawned (detaching tmux, not killing it).
func (m *Manager) CloseAll() {
	for _, id := range m.bySession {
		_ = kitty.CloseWindow(id)
	}
	for _, id := range m.placeholders {
		_ = kitty.CloseWindow(id)
	}
	m.columns = nil
	m.placeholders = nil
	m.bySession = make(map[string]int)
}
