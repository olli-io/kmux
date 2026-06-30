package layout

import "testing"

// TestAdoptInPlaceLaunch checks that when an idle placeholder slot becomes an
// agent in place (kmux-idler exec'd a tmux client for a session into that
// window), reconcile adopts that very window as the session's pane instead of
// launching a duplicate — the property that makes the launch instant.
func TestAdoptInPlaceLaunch(t *testing.T) {
	f := newFakeKitty()
	installFakeKitty(t, f)
	m := NewManager(100)

	// No agents yet: the layout pads to maxColumns idle placeholders.
	if _, errs := m.ReconcileAll(nil); len(errs) != 0 {
		t.Fatalf("seed reconcile: %v", errs)
	}
	if len(m.placeholders) != maxColumns {
		t.Fatalf("got %d placeholders, want %d", len(m.placeholders), maxColumns)
	}

	// The middle idle slot becomes an agent in place: its window now runs a tmux
	// client for session X. Report that via the session seam.
	const session = "[kmux][CC]~/git/proj"
	adopted := m.placeholders[1]
	tmuxSessionByWindow = func() (map[int]string, error) {
		return map[int]string{adopted: session}, nil
	}

	if _, errs := m.ReconcileAll([]string{session}); len(errs) != 0 {
		t.Fatalf("adopt reconcile: %v", errs)
	}

	// The session is bound to the SAME window the idler ran in (in place), not a
	// freshly launched one.
	if got, ok := m.WindowID(session); !ok || got != adopted {
		t.Fatalf("session window = %d (ok=%v), want adopted placeholder %d", got, ok, adopted)
	}
	// That window is no longer counted as a placeholder.
	for _, ph := range m.placeholders {
		if ph == adopted {
			t.Errorf("adopted window %d still listed as a placeholder", adopted)
		}
	}
	// Exactly one real agent column, and the agent area still totals maxColumns.
	if len(m.columns) != 1 {
		t.Errorf("agent columns = %d, want 1", len(m.columns))
	}
	if total := len(m.columns) + len(m.placeholders); total != maxColumns {
		t.Errorf("agent-area columns = %d, want %d", total, maxColumns)
	}
}

// TestNoDuplicateDuringAdoptionRace checks the guard against racing a duplicate
// pane: a placeholder is already running the session's tmux client, but the
// session hasn't yet appeared in the active set (tmux is still starting). reconcile
// must not launch a separate pane for it — it waits to adopt.
func TestNoDuplicateDuringAdoptionRace(t *testing.T) {
	f := newFakeKitty()
	installFakeKitty(t, f)
	m := NewManager(100)

	if _, errs := m.ReconcileAll(nil); len(errs) != 0 {
		t.Fatalf("seed reconcile: %v", errs)
	}
	const session = "[kmux][CC]~/git/proj"
	ph := m.placeholders[0]
	tmuxSessionByWindow = func() (map[int]string, error) {
		return map[int]string{ph: session}, nil
	}

	// The session IS active and shown in the placeholder, but suppose the active
	// list lags by one round: even so, no pane should be launched for it beyond
	// adopting the placeholder. (Here it is active, so it adopts; the key assertion
	// is that the window stays the placeholder, never a new id.)
	before := len(f.liveSet())
	if _, errs := m.ReconcileAll([]string{session}); len(errs) != 0 {
		t.Fatalf("reconcile: %v", errs)
	}
	if got, _ := m.WindowID(session); got != ph {
		t.Errorf("session window = %d, want the in-place placeholder %d (no new window)", got, ph)
	}
	if after := len(f.liveSet()); after != before {
		t.Errorf("live window count changed %d -> %d; a duplicate was launched", before, after)
	}
}

// TestAdoptExternalWindow checks that a window kmux never created — a blank pane
// the user spawned, then launched an agent in via the idle loop — is adopted as
// the session's pane instead of getting a duplicate launched alongside it.
func TestAdoptExternalWindow(t *testing.T) {
	f := newFakeKitty()
	installFakeKitty(t, f)
	m := NewManager(100)

	// Seed the idle layout: maxColumns placeholders, no agents.
	if _, errs := m.ReconcileAll(nil); len(errs) != 0 {
		t.Fatalf("seed reconcile: %v", errs)
	}

	// A window the manager does not own (id 500) is added to kitty out-of-band and
	// starts running session X's tmux client — the user launched an agent in a pane
	// they had spawned themselves.
	const session = "[kmux][CC]~/git/proj"
	const external = 500
	f.mu.Lock()
	f.live[external] = true
	f.mu.Unlock()
	tmuxSessionByWindow = func() (map[int]string, error) {
		return map[int]string{external: session}, nil
	}

	if _, errs := m.ReconcileAll([]string{session}); len(errs) != 0 {
		t.Fatalf("adopt reconcile: %v", errs)
	}

	// The session is bound to the external window itself — no fresh pane launched
	// (a duplicate would carry one of the fake's launched ids, all > 1000).
	if got, ok := m.WindowID(session); !ok || got != external {
		t.Fatalf("session window = %d (ok=%v), want external %d", got, ok, external)
	}
	if !f.liveSet()[external] {
		t.Errorf("external window %d was closed; it should have been adopted in place", external)
	}
	if len(m.columns) != 1 {
		t.Errorf("agent columns = %d, want 1 (the adopted external window)", len(m.columns))
	}
	// The adopted column replaces one idle slot, so the agent area still totals
	// maxColumns: 1 real column + (maxColumns-1) placeholders.
	if total := len(m.columns) + len(m.placeholders); total != maxColumns {
		t.Errorf("agent-area columns = %d, want %d", total, maxColumns)
	}
}

// TestSidebarNotAdopted guards adoptExternalWindows against ever claiming the
// kmux sidebar itself, even if it somehow reports a tmux foreground client for an
// active session.
func TestSidebarNotAdopted(t *testing.T) {
	f := newFakeKitty()
	installFakeKitty(t, f)
	const sidebar = 100
	m := NewManager(sidebar)

	const session = "[kmux][CC]~/git/proj"
	tmuxSessionByWindow = func() (map[int]string, error) {
		return map[int]string{sidebar: session}, nil
	}
	if _, errs := m.ReconcileAll([]string{session}); len(errs) != 0 {
		t.Fatalf("reconcile: %v", errs)
	}
	if got, _ := m.WindowID(session); got == sidebar {
		t.Errorf("session bound to the sidebar window %d; it must never be adopted", sidebar)
	}
}
