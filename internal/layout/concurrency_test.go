package layout

import (
	"sync"
	"testing"
	"time"

	"github.com/olli-io/kmux/internal/kitty"
)

// fakeKitty is an in-memory stand-in for the kitty backend used to exercise the
// layout transactions without a real terminal. It models just the live window
// set: launch assigns a fresh id and records it, close removes it. It is shared
// across goroutines, so it guards its own state with a mutex.
type fakeKitty struct {
	mu     sync.Mutex
	nextID int
	live   map[int]bool
}

func newFakeKitty() *fakeKitty {
	return &fakeKitty{nextID: 1000, live: map[int]bool{}}
}

func (f *fakeKitty) launch(_ kitty.SplitLocation, _, _ int, _ string, _ ...string) (int, error) {
	f.mu.Lock()
	f.nextID++
	id := f.nextID
	f.live[id] = true
	f.mu.Unlock()
	// A short delay widens the window for overlapping transactions to interleave,
	// so the race detector and the orphan assertion have a chance to fire if the
	// Manager's serialization is ever removed.
	time.Sleep(time.Millisecond)
	return id, nil
}

func (f *fakeKitty) close(id int) error {
	f.mu.Lock()
	delete(f.live, id)
	f.mu.Unlock()
	return nil
}

// liveIDs returns a snapshot of the current live window set.
func (f *fakeKitty) liveIDs() (map[int]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[int]bool, len(f.live))
	for id := range f.live {
		out[id] = true
	}
	return out, nil
}

// liveSet returns the live ids as a plain set for assertions.
func (f *fakeKitty) liveSet() map[int]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[int]bool, len(f.live))
	for id := range f.live {
		out[id] = true
	}
	return out
}

// installFakeKitty points the layout seams at f for the duration of the test and
// returns a restore func. windowColumns returns an empty map so rebalance sees a
// zero total and returns immediately (a no-op), and resizeHoriz is inert.
func installFakeKitty(t *testing.T, f *fakeKitty) {
	t.Helper()
	origLaunch, origClose := launchWindow, closeWindow
	origLive, origCols, origResize := liveWindowIDs, windowColumns, resizeHoriz
	origSessions := tmuxSessionByWindow
	launchWindow = f.launch
	closeWindow = f.close
	liveWindowIDs = f.liveIDs
	windowColumns = func() (map[int]int, error) { return map[int]int{}, nil }
	resizeHoriz = func(_, _ int) error { return nil }
	// No in-place adoptions in these tests: every launch goes through the fake's
	// own pane creation, so no window reports a tmux foreground client.
	tmuxSessionByWindow = func() (map[int]string, error) { return nil, nil }
	t.Cleanup(func() {
		launchWindow, closeWindow = origLaunch, origClose
		liveWindowIDs, windowColumns, resizeHoriz = origLive, origCols, origResize
		tmuxSessionByWindow = origSessions
	})
}

// trackedWindows returns the set of every window id the Manager believes it owns
// (real agent panes plus placeholders).
func (m *Manager) trackedWindows() map[int]bool {
	out := map[int]bool{}
	for _, col := range m.columns {
		for _, id := range col {
			out[id] = true
		}
	}
	for _, id := range m.placeholders {
		out[id] = true
	}
	return out
}

// TestReconcileAllConcurrent reproduces the mass-idle-reap scenario: many
// ReconcileAll passes run at once (one per reaped session, plus the poll tick),
// all driving the layout toward zero agents. The transaction lock must serialize
// them so they converge to exactly maxColumns placeholder slots with no orphaned
// (untracked-but-live) windows. Run under -race to also catch the unsynchronized
// map access this fix removed.
func TestReconcileAllConcurrent(t *testing.T) {
	f := newFakeKitty()
	installFakeKitty(t, f)

	m := NewManager(100) // sidebar window id 100 (not launched via the fake)

	// Seed three live agent columns, as a steady state would have.
	if _, errs := m.ReconcileAll([]string{"a", "b", "c"}); len(errs) > 0 {
		t.Fatalf("seed reconcile errors: %v", errs)
	}
	if len(m.columns) != maxColumns || len(m.placeholders) != 0 {
		t.Fatalf("seed state: columns=%d placeholders=%d, want %d/0", len(m.columns), len(m.placeholders), maxColumns)
	}

	// All three sessions reaped at once: the reaper batches one kill per session,
	// each re-lists to the empty set and fires its own reconcile, and the poll
	// tick adds more — all concurrent.
	const goroutines = 8
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.ReconcileAll(nil)
		}()
	}
	wg.Wait()

	// Converged to exactly maxColumns idle slots, no real columns.
	if len(m.columns) != 0 {
		t.Errorf("columns = %d, want 0", len(m.columns))
	}
	if len(m.placeholders) != maxColumns {
		t.Errorf("placeholders = %d, want %d", len(m.placeholders), maxColumns)
	}

	// No orphans: every live window is tracked, and every tracked window is live.
	tracked := m.trackedWindows()
	live := f.liveSet()
	if len(live) != maxColumns {
		t.Errorf("live windows = %d, want %d (extra = orphaned idle slots)", len(live), maxColumns)
	}
	for id := range live {
		if !tracked[id] {
			t.Errorf("live window %d is not tracked by the Manager (orphan)", id)
		}
	}
	for id := range tracked {
		if !live[id] {
			t.Errorf("tracked window %d is not live", id)
		}
	}
}
