package layout

import "testing"

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
