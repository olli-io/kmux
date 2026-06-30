package layout

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/olli-io/kmux/internal/idler"
	"github.com/olli-io/kmux/internal/kitty"
	"github.com/olli-io/kmux/internal/tmux"
)

// Indirection seams over the kitty package so tests can inject a fake backend.
// Defaults are the real kitty calls, so production behavior is unchanged.
var (
	launchWindow        = kitty.Launch
	closeWindow         = kitty.CloseWindow
	liveWindowIDs       = kitty.LiveWindowIDs
	windowColumns       = kitty.WindowColumns
	resizeHoriz         = kitty.ResizeWindowHoriz
	tmuxSessionByWindow = kitty.TmuxSessionByWindow
)

const (
	maxColumns  = 3  // sidebar + up to 3 agent columns of vertical splits
	sidebarBias = 85 // % given to the first agent column on creation

	// Target pane fractions of the tab width, converged toward by rebalance.
	// The layout is always sidebar + maxColumns columns (real agent panes padded
	// with placeholders), so these sum to 1: 0.16 + 3*0.28 = 1.0.
	sidebarFrac = 0.16 // fraction of the tab width pinned to the sidebar
	agentFrac   = 0.28 // fraction of the tab width per agent column
)

// placeholderTitle labels the inert filler panes that pad the layout up to
// maxColumns so that real agent panes always render at the same fixed width.
const placeholderTitle = "·idle"

// placeholderCmd is the command a placeholder pane runs. When the kmux-idler
// helper is installed beside the kmux binary the slot becomes an interactive
// launcher: a tiny shell loop draws a hint and blocks on a single keypress, then
// spawns kmux-idler only for the moment the user is choosing what to launch. On
// select kmux-idler execs the agent's tmux client in place, so this very window
// becomes the agent pane instantly; the next reconcile adopts it (see
// adoptPlaceholders). Holding the idle slot with a shell — not a resting Go
// process — keeps an idle pane's footprint to a shell instead of a whole runtime.
// Without the helper it falls back to an inert pane that shows a dim hint and
// sleeps forever, never touching any agent's tmux session.
func placeholderCmd() []string {
	if p := idlerPath(); p != "" {
		return []string{"sh", "-c", idler.IdleLoopScript(p)}
	}
	return []string{
		"sh", "-c",
		`clear; printf '\n  \033[2midle slot\033[0m\n  \033[2m(reserved to keep agent panes a fixed width)\033[0m\n'; while :; do sleep 86400; done`,
	}
}

// IdlerPath returns the absolute path to the kmux-idler helper installed beside
// the kmux binary, or "" when it isn't present. The dashboard uses it to learn
// both whether the helper exists and how to invoke it when turning a user-spawned
// blank pane into an idle launcher (`<IdlerPath> --idle-loop`).
func IdlerPath() string {
	return idlerPath()
}

// idlerPath returns the path to the kmux-idler helper installed beside the kmux
// binary, or "" when it isn't present (so placeholderCmd falls back to the inert
// slot). Resolving it relative to the running executable — through any symlink —
// mirrors how the default config.yaml is located.
func idlerPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	p := filepath.Join(filepath.Dir(exe), "kmux-idler")
	if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
		return p
	}
	return ""
}

// Manager owns the mapping between tmux agent sessions and the kitty windows
// (panes) attached to them, plus the column layout state.
type Manager struct {
	// mu serializes every layout transaction. bubbletea runs each tea.Cmd in its
	// own goroutine, so reconcile/open/reattach passes (and the UI's render-path
	// reads) would otherwise mutate columns/placeholders/bySession concurrently —
	// racing the maps and double-creating/orphaning placeholder panes. The
	// exported entry points take the lock; unexported cores assume it is held.
	mu           sync.RWMutex
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.bySession))
	for name := range m.bySession {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Attached reports whether a session currently has a pane. It is called from the
// UI render path (rows -> buildSessionRows) while transactions mutate bySession,
// so it takes the read lock.
func (m *Manager) Attached(session string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.attached(session)
}

// attached is the lock-free core of Attached, for callers already holding mu.
func (m *Manager) attached(session string) bool {
	_, ok := m.bySession[session]
	return ok
}

// WindowID returns the kitty window id attached to session, if any.
func (m *Manager) WindowID(session string) (int, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.bySession[session]
	return id, ok
}

// ReconcileAll runs the full layout transaction atomically: it reconciles panes
// against the active session set, compacts freed slots, pads with placeholders,
// and rebalances — all under the lock so overlapping reconcile passes (one per
// idle-reaped session, plus the periodic poll) serialize instead of racing the
// shared layout state. The live window set is fetched in-lock so it is always
// consistent with the serialized manager state (a stale snapshot would orphan a
// freshly created placeholder in syncPlaceholders). It reports whether the pane
// layout changed (so the caller can restore macOS focus) and collects per-window
// errors; like its constituent steps it is best-effort.
func (m *Manager) ReconcileAll(active []string) (changed bool, errs []error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	live, err := liveWindowIDs()
	if err != nil {
		live = nil // best-effort: skip the manual-close prune this round
	}
	// Which kitty windows are now running a tmux client for which session. Lets
	// reconcile adopt an idle slot that kmux-idler turned into an agent in place.
	// Best-effort: a query error just skips adoption this round (the session then
	// gets a fresh pane next round, the pre-in-place behaviour).
	winSessions, err := tmuxSessionByWindow()
	if err != nil {
		winSessions = nil
	}
	changed, errs = m.reconcile(active, live, winSessions)
	cchanged, cerrs := m.compact()
	errs = append(errs, cerrs...)
	pchanged, perrs := m.syncPlaceholders(live)
	errs = append(errs, perrs...)
	if changed || cchanged || pchanged {
		errs = append(errs, m.rebalance()...)
	}
	return changed, errs
}

// reconcile makes the live panes match the set of active sessions: it attaches
// panes for new sessions and closes panes for vanished ones. It also prunes
// panes the user closed manually (detected via the live id set) and adopts idle
// slots that kmux-idler turned into agents in place (winSessions maps a window id
// to the session its foreground tmux client targets). It reports whether the pane
// layout changed (so the caller can trigger a rebalance). Errors from individual
// kitty calls are collected and returned together; reconcile is best-effort and
// continues past failures. The caller must hold mu.
func (m *Manager) reconcile(active []string, live map[int]bool, winSessions map[int]string) (changed bool, errs []error) {
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

	// Adopt any placeholder a launch turned into an agent pane in place, so it
	// keeps its window (instant launch) instead of getting a duplicate pane below.
	if m.adoptPlaceholders(activeSet, winSessions) {
		changed = true
	}

	// Adopt any external window (one kmux did not create) that is already running
	// an active session's tmux client — e.g. a blank pane the user spawned, turned
	// into an idle launcher, then into an agent in place. Without this the add loop
	// below would launch a second pane for the same session.
	if m.adoptExternalWindows(activeSet, live, winSessions) {
		changed = true
	}

	// Remove panes for sessions that disappeared.
	for session, id := range m.bySession {
		if !activeSet[session] {
			if err := closeWindow(id); err != nil {
				errs = append(errs, err)
			}
			m.forget(session, id)
			changed = true
		}
	}

	// Sessions currently shown in a placeholder window but not yet adopted (e.g.
	// the tmux client has started but the session isn't in `active` yet): skip
	// creating a pane for them so the imminent adoption isn't pre-empted by a
	// duplicate.
	inPlaceholder := m.placeholderSessions(winSessions)

	// Add panes for new sessions (sorted for deterministic column assignment).
	sort.Strings(active)
	for _, session := range active {
		if m.attached(session) || inPlaceholder[session] {
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

// adoptPlaceholders binds any placeholder window that is now running a tmux client
// for an active, not-yet-attached session into the layout as that session's pane:
// the window kmux-idler launched into stays put (an instant, in-place launch)
// rather than being closed and replaced by a fresh pane on the next poll. The
// adopted placeholder becomes its own agent column; syncPlaceholders then refills
// the idle slot count. winSessions maps a window id to the session its foreground
// tmux client targets. It reports whether it adopted anything. The caller must
// hold mu.
func (m *Manager) adoptPlaceholders(activeSet map[string]bool, winSessions map[int]string) (changed bool) {
	for i := 0; i < len(m.placeholders); {
		wid := m.placeholders[i]
		session := winSessions[wid]
		if session == "" || !activeSet[session] || m.attached(session) {
			i++
			continue
		}
		m.placeholders = append(m.placeholders[:i], m.placeholders[i+1:]...)
		m.columns = append(m.columns, []int{wid})
		m.bySession[session] = wid
		changed = true
	}
	return changed
}

// adoptExternalWindows binds any live window kmux does not already own that is
// running the tmux client of an active, not-yet-attached session into the layout
// as that session's pane — making it a new agent column. It is the counterpart of
// adoptPlaceholders for windows kmux never created (a pane the user spawned and
// then launched an agent in via the idle loop). Adopting in place keeps the
// session's single pane where the agent already started instead of racing a
// duplicate against it below. winSessions maps a window id to the session its
// foreground tmux client targets; live is the set of windows kitty currently
// knows about. It reports whether it adopted anything. The caller must hold mu.
func (m *Manager) adoptExternalWindows(activeSet map[string]bool, live map[int]bool, winSessions map[int]string) (changed bool) {
	// Sorted so column assignment is deterministic when several are adopted at once.
	wids := make([]int, 0, len(winSessions))
	for wid := range winSessions {
		wids = append(wids, wid)
	}
	sort.Ints(wids)
	for _, wid := range wids {
		session := winSessions[wid]
		if session == "" || !activeSet[session] || m.attached(session) {
			continue
		}
		if m.ownsWindow(wid) || (live != nil && !live[wid]) {
			continue
		}
		m.columns = append(m.columns, []int{wid})
		m.bySession[session] = wid
		changed = true
	}
	return changed
}

// ownsWindow reports whether id is a window the manager already tracks — the
// sidebar, an attached agent pane, or a placeholder — so adoptExternalWindows
// only ever claims genuinely external windows. The caller must hold mu.
func (m *Manager) ownsWindow(id int) bool {
	if id == m.sidebarID {
		return true
	}
	for _, col := range m.columns {
		for _, wid := range col {
			if wid == id {
				return true
			}
		}
	}
	for _, wid := range m.placeholders {
		if wid == id {
			return true
		}
	}
	return false
}

// placeholderSessions is the set of sessions whose foreground tmux client runs in
// one of the manager's current placeholder windows (per winSessions). reconcile
// uses it to avoid racing a duplicate pane against an imminent adoption.
func (m *Manager) placeholderSessions(winSessions map[int]string) map[string]bool {
	if len(winSessions) == 0 {
		return nil
	}
	out := make(map[string]bool, len(m.placeholders))
	for _, wid := range m.placeholders {
		if s := winSessions[wid]; s != "" {
			out[s] = true
		}
	}
	return out
}

// OpenAndSync opens a pane for a manually launched session, then pads and
// rebalances the layout — all under the lock so it serializes with reconcile
// passes. It mirrors ReconcileAll's transaction shape for the single-session
// open path. Returns collected per-window errors.
func (m *Manager) OpenAndSync(name, dir, agentCmd string) []error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.open(name, dir, agentCmd); err != nil {
		return []error{err}
	}
	live, err := liveWindowIDs()
	if err != nil {
		live = nil // best-effort: skip the manual-close prune this round
	}
	_, errs := m.syncPlaceholders(live)
	return append(errs, m.rebalance()...)
}

// ReattachAndSync re-opens a pane for an already-running session whose pane was
// lost, then pads and rebalances — the reattach counterpart of OpenAndSync,
// under the lock.
func (m *Manager) ReattachAndSync(name string) []error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reattach(name); err != nil {
		return []error{err}
	}
	live, err := liveWindowIDs()
	if err != nil {
		live = nil // best-effort: skip the manual-close prune this round
	}
	_, errs := m.syncPlaceholders(live)
	return append(errs, m.rebalance()...)
}

// open ensures a detached tmux session named `name` (running agentCmd in dir)
// exists, then attaches a pane for it and records it in the layout. If the
// session is already attached it is a no-op; callers should focus it instead.
// The caller must hold mu.
func (m *Manager) open(name, dir, agentCmd string) error {
	if m.attached(name) {
		return nil
	}
	if err := tmux.NewDetachedSession(name, dir, agentCmd); err != nil {
		return err
	}
	return m.add(name)
}

// reattach attaches a fresh pane to an already-running session, without creating
// a tmux session. It is used to re-open a pane the user closed by hand (or
// otherwise lost) for a session that is still live. It is a no-op when the
// session is already attached; callers should focus it instead. The caller must
// hold mu.
func (m *Manager) reattach(session string) error {
	if m.attached(session) {
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
	id, err := launchWindow(loc, matchID, bias, session, "tmux", "attach", "-t", session)
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
	id, err := launchWindow(kitty.VSplit, ph, 0, session, "tmux", "attach", "-t", session)
	if err != nil {
		return err
	}
	// Drop the placeholder so the new column expands to fill its slot. Best
	// effort: even if the close call fails, syncPlaceholders prunes it later.
	_ = closeWindow(ph)
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

// compact lifts stacked agent panes into free column slots so that detaching a
// column collapses a horizontal split rather than leaving an idle slot behind.
// While a slot is free (fewer than maxColumns columns) and some column is still
// stacked, it moves the bottom pane of the tallest stack into a new column of its
// own. Moving a pane means closing its kitty window (which only detaches tmux)
// and re-attaching it as a fresh column via add. It reports whether anything
// changed (so the caller can rebalance) and collects per-window errors; like
// reconcile it is best-effort. The caller must hold mu.
func (m *Manager) compact() (changed bool, errs []error) {
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
		if err := closeWindow(id); err != nil {
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
// placeholder is its own anchor. rebalance evens these out so each occupies an
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

// syncPlaceholders adds or removes filler panes so the agent area always holds
// maxColumns columns (real + placeholder) while any agent is active, keeping
// real agent panes at a constant width. It first prunes placeholders the user
// closed by hand (via the live id set), then converges to placeholderTarget. It
// reports whether anything changed (so the caller can rebalance) and collects
// per-window errors; like reconcile it is best-effort. The caller must hold mu,
// and live must be a snapshot taken under that same lock so a freshly created
// placeholder is never mistaken for one the user closed and orphaned.
func (m *Manager) syncPlaceholders(live map[int]bool) (changed bool, errs []error) {
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
		if err := closeWindow(last); err != nil {
			errs = append(errs, err)
		}
		m.placeholders = m.placeholders[:len(m.placeholders)-1]
		changed = true
	}

	// Open missing placeholders as new rightmost columns.
	for len(m.placeholders) < want {
		id, err := launchWindow(kitty.VSplit, m.rightmostAnchor(), 0, placeholderTitle, placeholderCmd()...)
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
// rebalance stops trying to correct it.
const rebalanceTolerance = 1

// rebalanceMaxPasses caps how many convergence passes rebalance makes. A single
// relative-resize pass often under-shoots (kitty's resize-window does not always
// move the full requested delta in one call, and an unbalanced split tree needs
// several nudges), so we repeat until widths settle or this cap is hit.
const rebalanceMaxPasses = 6

// rebalance sizes the sidebar and agent columns to their target fractions of the
// tab width (sidebarFrac / agentFrac). It resizes the sidebar and every column
// but the last (which absorbs the remainder), re-reading live widths before each
// step so the relative resizes converge regardless of the underlying split tree
// shape, and repeats the whole pass until every window is within
// rebalanceTolerance of its target or rebalanceMaxPasses is reached. The caller
// must hold mu.
func (m *Manager) rebalance() []error {
	anchors := m.columnAnchors()
	if len(anchors) == 0 {
		return nil
	}

	type step struct{ id, target int }
	var errs []error
	for pass := 0; pass < rebalanceMaxPasses; pass++ {
		widths, err := windowColumns()
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
			cur, err := windowColumns()
			if err != nil {
				errs = append(errs, err)
				continue
			}
			delta := s.target - cur[s.id]
			if delta > rebalanceTolerance || delta < -rebalanceTolerance {
				converged = false
			}
			if err := resizeHoriz(s.id, delta); err != nil {
				errs = append(errs, err)
			}
		}
		if converged {
			break
		}
	}
	return errs
}

// CloseAll closes every pane kmux spawned (detaching tmux, not killing it). It
// mutates the layout and is called from the UI goroutine on quit, so it takes the
// exclusive lock — running after any in-flight transaction completes.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.bySession {
		_ = closeWindow(id)
	}
	for _, id := range m.placeholders {
		_ = closeWindow(id)
	}
	m.columns = nil
	m.placeholders = nil
	m.bySession = make(map[string]int)
}
