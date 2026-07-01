package layout

import "testing"

// TestAdoptInPlace checks that a placeholder whose idle slot launched its agent in
// place (its window now hosts the session's tmux client) is promoted to a managed
// column in that same window, instead of reconcile opening a second pane for it.
func TestAdoptInPlace(t *testing.T) {
	f := newFakeKitty()
	installFakeKitty(t, f)
	m := NewManager(100)

	// One agent + two idle slots: columns=[[a]], placeholders=[p1,p2].
	if _, errs := m.ReconcileAll([]string{"a"}); len(errs) != 0 {
		t.Fatalf("seed reconcile: %v", errs)
	}
	if len(m.columns) != 1 || len(m.placeholders) != 2 {
		t.Fatalf("seed state: columns=%d placeholders=%d, want 1/2", len(m.columns), len(m.placeholders))
	}
	p1 := m.placeholders[0]
	p2 := m.placeholders[1]

	// The user launches "b" from idle slot p1: kmux-idler execs a tmux client in
	// that same window, so kitty now reports p1's foreground tmux client targets "b".
	f.mu.Lock()
	f.targets[p1] = "b"
	f.mu.Unlock()

	if _, errs := m.ReconcileAll([]string{"a", "b"}); len(errs) != 0 {
		t.Fatalf("adopt reconcile: %v", errs)
	}

	// "b" was adopted into the existing window p1 (not a freshly launched pane).
	if id, ok := m.WindowID("b"); !ok || id != p1 {
		t.Fatalf("bySession[b] = (%d,%t), want (%d,true) — adopted the placeholder in place", id, ok, p1)
	}
	// p1 moved placeholders -> columns; p2 stays the sole remaining idle slot.
	if !m.trackedWindows()[p1] {
		t.Errorf("adopted window %d should be tracked", p1)
	}
	if len(m.placeholders) != 1 || m.placeholders[0] != p2 {
		t.Errorf("placeholders = %v, want [%d]", m.placeholders, p2)
	}
	if len(m.columns) != 2 {
		t.Errorf("columns = %d, want 2 (agent a + adopted b)", len(m.columns))
	}
	// No orphans: every live window is tracked and vice-versa (no second pane for b).
	live := f.liveSet()
	tracked := m.trackedWindows()
	for id := range live {
		if id != 100 && !tracked[id] { // 100 is the sidebar, never launched
			t.Errorf("live window %d untracked (a duplicate pane was opened for b)", id)
		}
	}
	for id := range tracked {
		if !live[id] {
			t.Errorf("tracked window %d is not live", id)
		}
	}
}

// TestAdoptInPlaceUntracked checks that an agent launched in place from an
// untracked spare pane (one kmux never put in its column model) is bound to that
// window — preventing a duplicate pane — without being added to the columns.
func TestAdoptInPlaceUntracked(t *testing.T) {
	f := newFakeKitty()
	installFakeKitty(t, f)
	m := NewManager(100)

	// Full layout of three agent columns, no placeholders.
	if _, errs := m.ReconcileAll([]string{"a", "b", "c"}); len(errs) != 0 {
		t.Fatalf("seed reconcile: %v", errs)
	}
	if len(m.columns) != maxColumns || len(m.placeholders) != 0 {
		t.Fatalf("seed state: columns=%d placeholders=%d", len(m.columns), len(m.placeholders))
	}

	// A spare pane kmux doesn't own (id 500) launches session "d" in place.
	const spare = 500
	f.mu.Lock()
	f.live[spare] = true
	f.targets[spare] = "d"
	f.mu.Unlock()

	if _, errs := m.ReconcileAll([]string{"a", "b", "c", "d"}); len(errs) != 0 {
		t.Fatalf("adopt reconcile: %v", errs)
	}

	// "d" is bound to the spare window (no duplicate pane opened for it)...
	if id, ok := m.WindowID("d"); !ok || id != spare {
		t.Fatalf("bySession[d] = (%d,%t), want (%d,true)", id, ok, spare)
	}
	// ...but the spare stays out of the column model (still exactly maxColumns).
	if len(m.columns) != maxColumns {
		t.Errorf("columns = %d, want %d (spare must not become a column)", len(m.columns), maxColumns)
	}
	// No second pane was launched for "d": the only window bound to it is the spare.
	for _, col := range m.columns {
		for _, id := range col {
			if id == spare {
				t.Errorf("spare %d was added to a column; it should stay untracked", spare)
			}
		}
	}
}

// TestReorgVerticalPaneRestacks checks that a user-spawned extra full-height column
// is closed and replaced by an idle slot stacked under an existing column, so the
// agent area never grows a fourth column.
func TestReorgVerticalPaneRestacks(t *testing.T) {
	f := newFakeKitty()
	installFakeKitty(t, f)
	m := NewManager(100)

	// A full layout: maxColumns single-pane agent columns.
	if _, errs := m.ReconcileAll([]string{"a", "b", "c"}); len(errs) != 0 {
		t.Fatalf("seed reconcile: %v", errs)
	}

	// The user vsplit a pane, leaving a blank fourth column (id 500) kmux never made.
	const external = 500
	f.mu.Lock()
	f.live[external] = true
	f.mu.Unlock()

	if err := m.ReorgVerticalPane(external); err != nil {
		t.Fatalf("ReorgVerticalPane: %v", err)
	}

	live := f.liveSet()
	// The external vertical column is gone...
	if live[external] {
		t.Errorf("external pane %d still live; it should have been restacked", external)
	}
	// ...and replaced by a fresh idle slot (a fake-launched id > 1000).
	var replacement int
	for id := range live {
		if id > 1000 {
			// the agent panes are also > 1000; the replacement is the one not tracked.
			if !m.trackedWindows()[id] {
				replacement = id
			}
		}
	}
	if replacement == 0 {
		t.Fatalf("no untracked replacement idle slot was launched")
	}
	// The restacked slot is intentionally untracked (the user's spare pane).
	if m.trackedWindows()[replacement] {
		t.Errorf("replacement %d should be left untracked", replacement)
	}
	// The column model is unchanged: still maxColumns columns, no extra column.
	if len(m.columns) != maxColumns {
		t.Errorf("columns = %d, want %d", len(m.columns), maxColumns)
	}
}

// TestReorgVerticalPaneIgnoresOwnedWindow guards the reorg against ever touching a
// window kmux itself created (an agent pane or placeholder).
func TestReorgVerticalPaneIgnoresOwnedWindow(t *testing.T) {
	f := newFakeKitty()
	installFakeKitty(t, f)
	m := NewManager(100)

	if _, errs := m.ReconcileAll([]string{"a"}); len(errs) != 0 {
		t.Fatalf("seed reconcile: %v", errs)
	}
	owned, ok := m.WindowID("a")
	if !ok {
		t.Fatal("session a has no window")
	}
	before := f.liveSet()

	if err := m.ReorgVerticalPane(owned); err != nil {
		t.Fatalf("ReorgVerticalPane: %v", err)
	}

	if !f.liveSet()[owned] {
		t.Errorf("owned agent pane %d was closed; reorg must ignore kmux's own panes", owned)
	}
	if len(f.liveSet()) != len(before) {
		t.Errorf("live window count changed; reorg should have been a no-op")
	}
}
