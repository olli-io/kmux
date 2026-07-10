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
var (
	launchWindow       = kitty.Launch
	closeWindow        = kitty.CloseWindow
	liveWindowIDs      = kitty.LiveWindowIDs
	snapshot           = kitty.Snapshot
	windowColumns      = kitty.WindowColumns
	resizeHoriz     = kitty.ResizeWindowHoriz
	adoptHints      = idler.ReadAdoptHints
	removeAdoptHint = idler.RemoveAdoptHint
	setWindowTitle  = kitty.SetWindowTitle
)

const (
	maxColumns  = 3  // sidebar + up to 3 agent columns of vertical splits
	sidebarBias = 85 // % given to the first agent column on creation

	// Target pane fractions of the tab width, converged toward by rebalance.
	// Always sidebar + maxColumns columns, so these sum to 1: 0.16 + 3*0.28 = 1.0.
	sidebarFrac = 0.16 // fraction of the tab width pinned to the sidebar
	agentFrac   = 0.28 // fraction of the tab width per agent column
)

// placeholderTitle labels the filler panes that pad the layout to maxColumns so
// real agent panes always render at a fixed width.
const placeholderTitle = "·idle"

// placeholderCmd is the command a placeholder pane runs. With the kmux-idler helper
// installed beside the binary the slot is an interactive launcher held by a cheap
// shell loop; without it, an inert pane that shows a hint and sleeps forever.
func placeholderCmd() []string {
	if p := idlerPath(); p != "" {
		return []string{"sh", "-c", idler.IdleLoopScript(p)}
	}
	return []string{
		"sh", "-c",
		`clear; printf '\n  \033[2midle slot\033[0m\n  \033[2m(reserved to keep agent panes a fixed width)\033[0m\n'; while :; do sleep 86400; done`,
	}
}

// IdlerPath returns the path to the kmux-idler helper, or "" when it isn't present.
func IdlerPath() string {
	return idlerPath()
}

// idlerPath resolves the helper relative to the running executable, through any
// symlink, mirroring how the default config.yaml is located. "" when not present.
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
	// mu serializes every layout transaction: bubbletea runs each tea.Cmd in its own
	// goroutine, so passes would otherwise race the maps. Exported entry points take
	// the lock; unexported cores assume it is held.
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

// Attached reports whether a session currently has a pane. Called from the UI
// render path while transactions mutate bySession, so it takes the read lock.
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

// SidebarID returns the sidebar's kitty window id. Fixed at construction, so no
// lock is needed.
func (m *Manager) SidebarID() int {
	return m.sidebarID
}

func (m *Manager) WindowID(session string) (int, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.bySession[session]
	return id, ok
}

// ReconcileAll runs the full layout transaction atomically under the lock, so
// overlapping passes serialize instead of racing the shared state. The live window
// set is fetched in-lock so it stays consistent with the serialized manager state.
func (m *Manager) ReconcileAll(active []string) (changed bool, blanks []kitty.BlankPane, errs []error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// One in-lock snapshot feeds both the live-id prune here and the blank-pane list
	// returned to the caller, a single kitten invocation per poll instead of two.
	live, blanks, err := snapshot(m.sidebarID)
	if err != nil {
		live = nil // best-effort: skip the manual-close prune this round
	}
	hints, err := adoptHints()
	if err != nil {
		hints = nil // best-effort: skip in-place adoption; reconcile.add still opens a pane
	}
	// Prune hints whose window is gone so a stale hint can't mis-adopt a later
	// session, and adoptInPlace can trust every remaining hint points at a live window.
	if live != nil {
		for id := range hints {
			if !live[id] {
				delete(hints, id)
				_ = removeAdoptHint(id)
			}
		}
	}
	achanged := m.adoptInPlace(active, hints, live)
	changed, errs = m.reconcile(active, live)
	changed = changed || achanged
	cchanged, cerrs := m.compact()
	errs = append(errs, cerrs...)
	pchanged, perrs := m.syncPlaceholders(live)
	errs = append(errs, perrs...)
	if changed || cchanged || pchanged {
		errs = append(errs, m.rebalance()...)
	}
	return changed, blanks, errs
}

// adoptInPlace binds sessions that launched their agent in place to the kitty window
// they took over, before reconcile.add runs, so reconcile never opens a second pane.
// A tracked placeholder is promoted into the columns; an untracked user-split pane is
// bound but left out of the column model. The caller must hold mu.
func (m *Manager) adoptInPlace(active []string, hints map[int]string, live map[int]bool) (changed bool) {
	if len(hints) == 0 {
		return false
	}
	placeholder := make(map[int]bool, len(m.placeholders))
	for _, id := range m.placeholders {
		placeholder[id] = true
	}
	for _, session := range active {
		if m.attached(session) {
			continue
		}
		id := hintedWindow(hints, session)
		switch {
		case id == 0:
			continue // no in-place launch for this session
		case placeholder[id]:
			m.removePlaceholder(id)
			m.columns = append(m.columns, []int{id})
			m.bySession[session] = id
			_ = setWindowTitle(id, session) // match managed-pane titling; best effort
		case !m.ownsWindow(id) && live[id]:
			// Untracked spare pane: bind it but keep it out of the column model.
			m.bySession[session] = id
			_ = setWindowTitle(id, session)
		default:
			continue // owned non-placeholder (shouldn't happen) — leave to reconcile
		}
		_ = removeAdoptHint(id) // consumed; don't re-adopt next poll
		changed = true
	}
	return changed
}

// hintedWindow returns the window id hinted for session, or 0 if none (kitty ids
// are always positive, so 0 is a safe "none").
func hintedWindow(hints map[int]string, session string) int {
	for id, s := range hints {
		if s == session {
			return id
		}
	}
	return 0
}

func (m *Manager) removePlaceholder(id int) {
	for i, pid := range m.placeholders {
		if pid == id {
			m.placeholders = append(m.placeholders[:i], m.placeholders[i+1:]...)
			return
		}
	}
}

// reconcile makes the live panes match the active session set: attach new sessions,
// close vanished ones, and prune panes the user closed by hand. Best-effort; the
// caller must hold mu.
func (m *Manager) reconcile(active []string, live map[int]bool) (changed bool, errs []error) {
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
			if err := closeWindow(id); err != nil {
				errs = append(errs, err)
			}
			m.forget(session, id)
			changed = true
		}
	}

	// Add panes for new sessions (sorted for deterministic column assignment).
	sort.Strings(active)
	for _, session := range active {
		if m.attached(session) {
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

// ownsWindow reports whether id is a window the manager already tracks (sidebar,
// agent pane, or placeholder), so the watcher never reorganizes a kmux-owned window.
// The caller must hold mu.
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

// OpenAndSync opens a pane for a manually launched session, then pads and rebalances
// under the lock so it serializes with reconcile passes.
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

// ReattachAndSync re-opens a pane for a running session whose pane was lost, then
// pads and rebalances under the lock. The reattach counterpart of OpenAndSync.
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

// open ensures a detached tmux session exists, then attaches a pane for it. A no-op
// when already attached; callers should focus it instead. The caller must hold mu.
func (m *Manager) open(name, dir, agentCmd string) error {
	if m.attached(name) {
		return nil
	}
	if err := tmux.NewDetachedSession(name, dir, agentCmd); err != nil {
		return err
	}
	return m.add(name)
}

// reattach attaches a fresh pane to a running session without creating a tmux
// session. A no-op when already attached. The caller must hold mu.
func (m *Manager) reattach(session string) error {
	if m.attached(session) {
		return nil
	}
	return m.add(session)
}

func (m *Manager) add(session string) error {
	// A new agent column must consume a reserved placeholder rather than split a real
	// column, which would trap both agents in one slot with no way to resize free.
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

// addInPlaceholderSlot splits the leftmost placeholder then closes it, so the new
// column absorbs the whole slot at fixed width instead of half-splitting a real column.
func (m *Manager) addInPlaceholderSlot(session string) error {
	ph := m.placeholders[0]
	id, err := launchWindow(kitty.VSplit, ph, 0, session, "tmux", "attach", "-t", session)
	if err != nil {
		return err
	}
	// Drop the placeholder so the new column fills its slot; syncPlaceholders prunes
	// it later if the close fails.
	_ = closeWindow(ph)
	m.placeholders = m.placeholders[1:]
	m.columns = append(m.columns, []int{id})
	m.bySession[session] = id
	return nil
}

// placement decides where the next pane goes:
//   - fewer than maxColumns columns -> open a NEW column via vsplit
//   - otherwise -> STACK via hsplit under the column with the fewest panes
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

func (m *Manager) sessionFor(id int) string {
	for session, wid := range m.bySession {
		if wid == id {
			return session
		}
	}
	return ""
}

// promotable returns a stacked pane to lift into its own free column slot: the
// bottom pane of the rightmost stacked column. Pulling from the right keeps splits
// packed left, e.g. (A-B)|(C-D)|E losing E gives (A-B)|C|D not A|(C-D)|B.
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

// compact lifts stacked panes into free column slots so detaching a column collapses
// a horizontal split rather than leaving an idle slot. Moving a pane means closing
// its window (which only detaches tmux) and re-adding it. Best-effort; caller holds mu.
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
		// Re-attach the stacked pane as its own column; add lands it in a free slot.
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

// placeholderTarget is how many filler panes to hold to keep the agent area at
// maxColumns columns, so real panes stay a fixed width. Holds even with zero agents;
// once columns reach maxColumns the width is already fixed and no padding is needed.
func (m *Manager) placeholderTarget() int {
	if len(m.columns) >= maxColumns {
		return 0
	}
	return maxColumns - len(m.columns)
}

// columnAnchors returns one window id per agent column, real columns first then
// placeholders. A real column's anchor is its top window; a placeholder is its own.
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

// syncPlaceholders adds or removes filler panes to converge on placeholderTarget,
// first pruning placeholders the user closed by hand. Best-effort; the caller must
// hold mu, and live must be a snapshot taken under that same lock.
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

	// Close surplus placeholders from the right; the freed column is reused by the
	// real agent that just claimed it.
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

// rebalanceTargets computes target sidebar and per-column widths as fixed fractions
// of the total tab width. Total width is invariant under resizing, so these absolute
// targets can be computed once and converged toward; the last column absorbs rounding.
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

// rebalanceMaxPasses caps rebalance's convergence passes: a single relative-resize
// pass often under-shoots, so we repeat until widths settle or this cap is hit.
const rebalanceMaxPasses = 6

// rebalance sizes the sidebar and agent columns to their target fractions, resizing
// every column but the last (which absorbs the remainder) and re-reading live widths
// before each step so the relative resizes converge. The caller must hold mu.
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
		// The first step reuses the pass-opening snapshot; each later step re-reads,
		// since the resize before it shifts the widths of every window to its right.
		cur := widths
		for i, s := range steps {
			if i > 0 {
				cur, err = windowColumns()
				if err != nil {
					errs = append(errs, err)
					continue
				}
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

// ReorgVerticalPane absorbs a user-spawned full-height column (a manual vertical
// split) back into the fixed layout: it closes that pane and re-creates an idle slot
// stacked under the shortest column, so the area stays at maxColumns. The restacked
// slot is left untracked as the user's spare pane. Owning id is a no-op. Takes the lock.
func (m *Manager) ReorgVerticalPane(id int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ownsWindow(id) {
		return nil // never reorganize a pane kmux itself created
	}
	if err := closeWindow(id); err != nil {
		return err
	}
	_, err := launchWindow(kitty.HSplit, m.stackAnchor(), 0, placeholderTitle, placeholderCmd()...)
	return err
}

// stackAnchor is the window to hsplit a new stacked pane beneath: the bottom pane of
// the shortest agent column, else the first placeholder, else the sidebar.
func (m *Manager) stackAnchor() int {
	if len(m.columns) > 0 {
		t := 0
		for c := 1; c < len(m.columns); c++ {
			if len(m.columns[c]) < len(m.columns[t]) {
				t = c
			}
		}
		col := m.columns[t]
		return col[len(col)-1]
	}
	if len(m.placeholders) > 0 {
		return m.placeholders[0]
	}
	return m.sidebarID
}

// CloseAll closes every pane kmux spawned (detaching tmux, not killing it). Called on
// quit, so it takes the exclusive lock, running after any in-flight transaction.
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
