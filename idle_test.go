package main

import (
	"slices"
	"testing"
	"time"
)

func TestIdleTrackerReap(t *testing.T) {
	t0 := time.Unix(0, 0)
	past := idleTimeout + time.Minute // safely beyond the timeout

	t.Run("unchanged pane is reaped once it crosses the timeout", func(t *testing.T) {
		tr := newIdleTracker(idleTimeout)
		// First sighting: records the fingerprint, nothing to kill yet.
		if got := tr.reap(t0, map[string]uint64{"a~cl": 1}, nil); len(got) != 0 {
			t.Fatalf("first poll: got %v, want none", got)
		}
		// Same fingerprint a moment later: still within the window.
		if got := tr.reap(t0.Add(time.Minute), map[string]uint64{"a~cl": 1}, nil); len(got) != 0 {
			t.Fatalf("within window: got %v, want none", got)
		}
		// Same fingerprint past the timeout: reap it.
		got := tr.reap(t0.Add(past), map[string]uint64{"a~cl": 1}, nil)
		if !slices.Equal(got, []string{"a~cl"}) {
			t.Fatalf("past timeout: got %v, want [a~cl]", got)
		}
	})

	t.Run("a changing pane keeps resetting the clock", func(t *testing.T) {
		tr := newIdleTracker(idleTimeout)
		tr.reap(t0, map[string]uint64{"a~cl": 1}, nil)
		// Fingerprint changes right before the deadline: clock resets.
		tr.reap(t0.Add(past-time.Minute), map[string]uint64{"a~cl": 2}, nil)
		// Now past the original deadline but only seconds past the change.
		if got := tr.reap(t0.Add(past), map[string]uint64{"a~cl": 2}, nil); len(got) != 0 {
			t.Fatalf("got %v, want none (clock reset on change)", got)
		}
	})

	t.Run("busy agent is never reaped even with a stable pane", func(t *testing.T) {
		tr := newIdleTracker(idleTimeout)
		tr.reap(t0, map[string]uint64{"a~cl": 1}, map[string]bool{"a~cl": true})
		got := tr.reap(t0.Add(past), map[string]uint64{"a~cl": 1}, map[string]bool{"a~cl": true})
		if len(got) != 0 {
			t.Fatalf("got %v, want none (busy guard)", got)
		}
	})

	t.Run("a vanished session is dropped, not killed", func(t *testing.T) {
		tr := newIdleTracker(idleTimeout)
		tr.reap(t0, map[string]uint64{"a~cl": 1}, nil)
		// Session gone this poll: cleared from tracking.
		if got := tr.reap(t0.Add(time.Minute), map[string]uint64{}, nil); len(got) != 0 {
			t.Fatalf("got %v, want none", got)
		}
		if _, ok := tr.hash["a~cl"]; ok {
			t.Fatal("a~cl should have been dropped from tracking")
		}
		// Reappears: clock starts fresh, so it survives a poll past the old deadline.
		tr.reap(t0.Add(2*time.Minute), map[string]uint64{"a~cl": 1}, nil)
		if got := tr.reap(t0.Add(past), map[string]uint64{"a~cl": 1}, nil); len(got) != 0 {
			t.Fatalf("got %v, want none (fresh clock after reappearing)", got)
		}
	})

	t.Run("multiple idle sessions returned sorted", func(t *testing.T) {
		tr := newIdleTracker(idleTimeout)
		h := map[string]uint64{"b~cl": 1, "a~oc": 1}
		tr.reap(t0, h, nil)
		got := tr.reap(t0.Add(past), h, nil)
		if !slices.Equal(got, []string{"a~oc", "b~cl"}) {
			t.Fatalf("got %v, want [a~oc b~cl]", got)
		}
	})
}

func TestIdleTrackerDisabled(t *testing.T) {
	tr := newIdleTracker(0)
	h := map[string]uint64{"a~cl": 1}
	tr.reap(time.Unix(0, 0), h, nil)
	if got := tr.reap(time.Unix(0, 0).Add(idleTimeout*10), h, nil); got != nil {
		t.Fatalf("disabled tracker reaped %v, want nil", got)
	}
}

func TestHashPaneDistinguishesContent(t *testing.T) {
	if hashPane("idle screen") == hashPane("idle screen\n> typing") {
		t.Fatal("different pane content must hash differently")
	}
	if hashPane("same") != hashPane("same") {
		t.Fatal("identical pane content must hash identically")
	}
}
