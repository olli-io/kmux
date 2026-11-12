package layout

import (
	"errors"
	"testing"

	"github.com/olli-io/kmux/internal/kitty"
)

// errSnapshot stands in for a kitten invocation that failed.
var errSnapshot = errors.New("kitten @ ls failed")

// TestReconcileDefersWhileUnfocused is the core of the focus gate: opening a kitty
// pane drags the whole terminal to the foreground and resizes the splits around
// whatever the user is doing, so a session that appears while the cursor is outside
// the dashboard's tab must wait. The pane arrives on the first pass after focus
// returns — no extra event needed, the poll is the retry.
func TestReconcileDefersWhileUnfocused(t *testing.T) {
	f := newFakeKitty()
	installFakeKitty(t, f)
	m := NewManager(100)

	// Steady state, built while focused: one agent plus its padding.
	if _, _, _, errs := m.ReconcileAll([]string{"a"}); len(errs) > 0 {
		t.Fatalf("seed reconcile errors: %v", errs)
	}
	baseline := len(f.liveSet())
	if _, ok := m.bySession["a"]; !ok {
		t.Fatal("seed: session a should have a pane")
	}

	// The user switches away, then a new session shows up.
	f.unfocused = true
	_, deferred, _, errs := m.ReconcileAll([]string{"a", "b"})
	if len(errs) > 0 {
		t.Fatalf("unfocused reconcile errors: %v", errs)
	}
	if !deferred {
		t.Fatal("deferred = false, want true (b had no pane and kmux was unfocused)")
	}
	if _, ok := m.bySession["b"]; ok {
		t.Fatal("b got a pane while unfocused; the gate did not hold")
	}
	if got := len(f.liveSet()); got != baseline {
		t.Fatalf("live windows = %d, want %d — nothing should have been launched", got, baseline)
	}

	// Focus comes back: b is attached on the very next pass.
	f.unfocused = false
	_, deferred, _, errs = m.ReconcileAll([]string{"a", "b"})
	if len(errs) > 0 {
		t.Fatalf("refocused reconcile errors: %v", errs)
	}
	if deferred {
		t.Fatal("deferred = true after refocus, want false")
	}
	if _, ok := m.bySession["b"]; !ok {
		t.Fatal("b still has no pane after refocus")
	}
}

// TestReconcileClosesWhileUnfocused: only launching is gated. Closing a dead
// session's pane steals nothing, and leaving stale panes up until the user happens
// to look would be worse than the churn.
func TestReconcileClosesWhileUnfocused(t *testing.T) {
	f := newFakeKitty()
	installFakeKitty(t, f)
	m := NewManager(100)

	if _, _, _, errs := m.ReconcileAll([]string{"a", "b"}); len(errs) > 0 {
		t.Fatalf("seed reconcile errors: %v", errs)
	}
	gone := m.bySession["b"]

	f.unfocused = true
	if _, _, _, errs := m.ReconcileAll([]string{"a"}); len(errs) > 0 {
		t.Fatalf("unfocused reconcile errors: %v", errs)
	}
	if _, ok := m.bySession["b"]; ok {
		t.Fatal("b's pane survived its session dying; closes must not be gated")
	}
	if f.liveSet()[gone] {
		t.Fatalf("window %d still live after its session died", gone)
	}
}

// TestReconcileDeferredIsPurelyPending guards against the gate reporting itself as
// deferred forever: with nothing to launch, an unfocused pass is an ordinary no-op,
// not a deferral the UI would flag.
func TestReconcileDeferredIsPurelyPending(t *testing.T) {
	f := newFakeKitty()
	installFakeKitty(t, f)
	m := NewManager(100)

	if _, _, _, errs := m.ReconcileAll([]string{"a"}); len(errs) > 0 {
		t.Fatalf("seed reconcile errors: %v", errs)
	}

	f.unfocused = true
	if _, deferred, _, errs := m.ReconcileAll([]string{"a"}); len(errs) > 0 || deferred {
		t.Fatalf("steady-state unfocused pass: deferred = %v errs = %v, want false/none", deferred, errs)
	}
}

// TestOpenAndSyncIgnoresGate: the gate exists to protect focus the user did not
// give up. Pressing a key in the dashboard means they did, so a manual launch goes
// through even if the focus read says otherwise.
func TestOpenAndSyncIgnoresGate(t *testing.T) {
	f := newFakeKitty()
	installFakeKitty(t, f)
	m := NewManager(100)

	if _, _, _, errs := m.ReconcileAll([]string{"a"}); len(errs) > 0 {
		t.Fatalf("seed reconcile errors: %v", errs)
	}

	f.unfocused = true
	if errs := m.OpenAndSync("b", "/tmp", ""); len(errs) > 0 {
		t.Fatalf("OpenAndSync errors: %v", errs)
	}
	if _, ok := m.bySession["b"]; !ok {
		t.Fatal("a user-initiated launch was blocked by the focus gate")
	}
}

// TestFirstPassIgnoresGate: kmux builds its initial layout behind a splash tab that
// is deliberately opened focused, so the sidebar never holds focus while the first
// panes go up. Gating that pass would strand a freshly started dashboard empty
// until the user clicked into it, so the gate only arms from the second pass on.
func TestFirstPassIgnoresGate(t *testing.T) {
	f := newFakeKitty()
	installFakeKitty(t, f)
	m := NewManager(100)

	f.unfocused = true // the splash tab has the focus, not the sidebar
	_, deferred, _, errs := m.ReconcileAll([]string{"a", "b"})
	if len(errs) > 0 {
		t.Fatalf("initial reconcile errors: %v", errs)
	}
	if deferred {
		t.Fatal("deferred = true on the very first pass, want false")
	}
	for _, s := range []string{"a", "b"} {
		if _, ok := m.bySession[s]; !ok {
			t.Fatalf("%s got no pane during the initial build", s)
		}
	}

	// The exemption is one pass only: the next unfocused pass defers as usual.
	if _, deferred, _, _ := m.ReconcileAll([]string{"a", "b", "c"}); !deferred {
		t.Fatal("deferred = false on the second unfocused pass, want true")
	}
	if _, ok := m.bySession["c"]; ok {
		t.Fatal("c got a pane while unfocused; the gate never armed")
	}
}

// TestReconcileLaunchesWhenSnapshotFails: a kitten hiccup must not be read as "not
// focused" and wedge the dashboard into never attaching anything again. Failure
// assumes focus — the worst case is one stolen focus, not a permanently dead panel.
func TestReconcileLaunchesWhenSnapshotFails(t *testing.T) {
	f := newFakeKitty()
	installFakeKitty(t, f)
	m := NewManager(100)

	// Arm the gate first: the initial pass is exempt for its own reasons, so a fresh
	// manager would pass this test without the fail-open behavior existing at all.
	if _, _, _, errs := m.ReconcileAll([]string{"a"}); len(errs) > 0 {
		t.Fatalf("seed reconcile errors: %v", errs)
	}

	origSnap := snapshot
	snapshot = func(int) (map[int]bool, []kitty.BlankPane, bool, error) {
		return nil, nil, false, errSnapshot
	}
	t.Cleanup(func() { snapshot = origSnap })

	// Unfocused *and* unreadable: the error must win, or a kitten hiccup while the
	// user is elsewhere would look exactly like a deferral.
	f.unfocused = true
	_, deferred, _, _ := m.ReconcileAll([]string{"a", "b"})
	if deferred {
		t.Fatal("deferred = true on a failed snapshot, want false (assume focus)")
	}
	if _, ok := m.bySession["b"]; !ok {
		t.Fatal("b got no pane on a failed snapshot; the gate must fail open")
	}
}
