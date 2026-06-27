package status

import (
	"slices"
	"testing"
	"time"
)

// testTimeout is a representative idle timeout for the tracker tests, matching
// the production default (config.DefaultIdleTimeout) without depending on it.
const testTimeout = 2 * time.Hour

func TestIdleTrackerReap(t *testing.T) {
	t0 := time.Unix(0, 0)
	past := testTimeout + time.Minute // safely beyond the timeout

	t.Run("unchanged pane is reaped once it crosses the timeout", func(t *testing.T) {
		tr := newIdleTracker(testTimeout)
		// First sighting: records the fingerprint, nothing to kill yet.
		if got := tr.Reap(t0, map[string]uint64{"a~cl": 1}, nil); len(got) != 0 {
			t.Fatalf("first poll: got %v, want none", got)
		}
		// Same fingerprint a moment later: still within the window.
		if got := tr.Reap(t0.Add(time.Minute), map[string]uint64{"a~cl": 1}, nil); len(got) != 0 {
			t.Fatalf("within window: got %v, want none", got)
		}
		// Same fingerprint past the timeout: Reap it.
		got := tr.Reap(t0.Add(past), map[string]uint64{"a~cl": 1}, nil)
		if !slices.Equal(got, []string{"a~cl"}) {
			t.Fatalf("past timeout: got %v, want [a~cl]", got)
		}
	})

	t.Run("a changing pane keeps resetting the clock", func(t *testing.T) {
		tr := newIdleTracker(testTimeout)
		tr.Reap(t0, map[string]uint64{"a~cl": 1}, nil)
		// Fingerprint changes right before the deadline: clock resets.
		tr.Reap(t0.Add(past-time.Minute), map[string]uint64{"a~cl": 2}, nil)
		// Now past the original deadline but only seconds past the change.
		if got := tr.Reap(t0.Add(past), map[string]uint64{"a~cl": 2}, nil); len(got) != 0 {
			t.Fatalf("got %v, want none (clock reset on change)", got)
		}
	})

	t.Run("busy agent is never reaped even with a stable pane", func(t *testing.T) {
		tr := newIdleTracker(testTimeout)
		tr.Reap(t0, map[string]uint64{"a~cl": 1}, map[string]bool{"a~cl": true})
		got := tr.Reap(t0.Add(past), map[string]uint64{"a~cl": 1}, map[string]bool{"a~cl": true})
		if len(got) != 0 {
			t.Fatalf("got %v, want none (busy guard)", got)
		}
	})

	t.Run("a vanished session is dropped, not killed", func(t *testing.T) {
		tr := newIdleTracker(testTimeout)
		tr.Reap(t0, map[string]uint64{"a~cl": 1}, nil)
		// Session gone this poll: cleared from tracking.
		if got := tr.Reap(t0.Add(time.Minute), map[string]uint64{}, nil); len(got) != 0 {
			t.Fatalf("got %v, want none", got)
		}
		if _, ok := tr.hash["a~cl"]; ok {
			t.Fatal("a~cl should have been dropped from tracking")
		}
		// Reappears: clock starts fresh, so it survives a poll past the old deadline.
		tr.Reap(t0.Add(2*time.Minute), map[string]uint64{"a~cl": 1}, nil)
		if got := tr.Reap(t0.Add(past), map[string]uint64{"a~cl": 1}, nil); len(got) != 0 {
			t.Fatalf("got %v, want none (fresh clock after reappearing)", got)
		}
	})

	t.Run("multiple idle sessions returned sorted", func(t *testing.T) {
		tr := newIdleTracker(testTimeout)
		h := map[string]uint64{"b~cl": 1, "a~oc": 1}
		tr.Reap(t0, h, nil)
		got := tr.Reap(t0.Add(past), h, nil)
		if !slices.Equal(got, []string{"a~oc", "b~cl"}) {
			t.Fatalf("got %v, want [a~oc b~cl]", got)
		}
	})
}

// TestIdleTrackerFromPersisted covers the launch-sweep decision: a tracker
// seeded with records from a previous run, reaped once against freshly captured
// pane hashes (exactly what SweepIdleAtLaunch does), kills only sessions whose
// pane is unchanged and stale.
func TestIdleTrackerFromPersisted(t *testing.T) {
	t0 := time.Unix(0, 0)
	old := t0.Add(-testTimeout - time.Minute) // changed safely before the timeout
	persisted := map[string]IdleRecord{
		"stale~cl":  {Hash: 1, Changed: old}, // unchanged + past timeout -> kill
		"worked~cl": {Hash: 1, Changed: old}, // pane changed since -> spare
		"recent~cl": {Hash: 1, Changed: t0},  // unchanged but fresh -> spare
	}
	// "unknown~cl" has no persisted record -> spared (no idle evidence).
	hashes := map[string]uint64{
		"stale~cl":   1, // matches persisted fingerprint
		"worked~cl":  2, // differs: agent produced output (e.g. detached + working)
		"recent~cl":  1,
		"unknown~cl": 9,
	}

	tr := NewIdleTrackerFrom(testTimeout, persisted)
	got := tr.Reap(t0, hashes, nil)
	if !slices.Equal(got, []string{"stale~cl"}) {
		t.Fatalf("launch sweep: got %v, want [stale~cl]", got)
	}
}

// TestIdleTrackerSnapshotRoundTrip checks that snapshotting then reseeding a
// tracker preserves the idle clock, so persistence does not reset idle time.
func TestIdleTrackerSnapshotRoundTrip(t *testing.T) {
	t0 := time.Unix(0, 0)
	past := testTimeout + time.Minute

	tr := newIdleTracker(testTimeout)
	tr.Reap(t0, map[string]uint64{"a~cl": 7}, nil) // clock starts at t0

	snap := tr.Snapshot()
	if rec := snap["a~cl"]; rec.Hash != 7 || !rec.Changed.Equal(t0) {
		t.Fatalf("Snapshot: got %+v, want {Hash:7 Changed:%v}", rec, t0)
	}

	// A new run reseeded from the Snapshot must Reap on the original deadline,
	// not restart the clock from launch.
	tr2 := NewIdleTrackerFrom(testTimeout, snap)
	got := tr2.Reap(t0.Add(past), map[string]uint64{"a~cl": 7}, nil)
	if !slices.Equal(got, []string{"a~cl"}) {
		t.Fatalf("reseeded tracker: got %v, want [a~cl] (clock preserved)", got)
	}
}

func TestIdleTrackerDisabled(t *testing.T) {
	tr := newIdleTracker(0)
	h := map[string]uint64{"a~cl": 1}
	tr.Reap(time.Unix(0, 0), h, nil)
	if got := tr.Reap(time.Unix(0, 0).Add(testTimeout*10), h, nil); got != nil {
		t.Fatalf("disabled tracker reaped %v, want nil", got)
	}
}

func TestHashPaneDistinguishesContent(t *testing.T) {
	if HashPane("idle screen") == HashPane("idle screen\n> typing") {
		t.Fatal("different pane content must hash differently")
	}
	if HashPane("same") != HashPane("same") {
		t.Fatal("identical pane content must hash identically")
	}
}
