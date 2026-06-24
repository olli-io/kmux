package main

import (
	"hash/fnv"
	"sort"
	"time"
)

// idleTimeout is the default for how long an agent session may sit completely
// unchanged before kmux kills it to free the memory its agent process holds; the
// `idle_timeout:` config key overrides it (see Config.IdleDuration). Idleness is
// measured by pane stability, not the attention state: a session counts as idle
// only while its captured pane is byte-for-byte identical across polls. A
// generating agent (animated spinner), or one a user is actively typing into,
// keeps changing its pane and so never accrues idle time, while a finished agent
// left waiting at a static screen does. Detached sessions are tracked too — tmux
// keeps their buffers and the agent process alive, so they cost memory just the
// same.
const idleTimeout = 2 * time.Hour

// hashPane reduces a captured pane to a 64-bit fingerprint so the idle tracker
// can detect "unchanged since last poll" without retaining the full text.
func hashPane(text string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(text))
	return h.Sum64()
}

// idleTracker remembers, per session, the last pane fingerprint seen and the
// time that fingerprint last changed. It is the only mutable state behind the
// idle-kill policy; everything else (which sessions exist, their busy state) is
// supplied each poll by reap.
type idleTracker struct {
	timeout    time.Duration        // idle threshold; <= 0 disables reaping
	hash       map[string]uint64    // session -> last pane fingerprint
	lastChange map[string]time.Time // session -> when the fingerprint last changed
}

func newIdleTracker(timeout time.Duration) idleTracker {
	return idleTracker{
		timeout:    timeout,
		hash:       map[string]uint64{},
		lastChange: map[string]time.Time{},
	}
}

// idleRecord is the persisted, per-session shape of the idle clock: the last pane
// fingerprint and when it last changed. Persisting it lets idle tracking survive
// a kmux restart, so a session that sat unchanged across runs is reaped on the
// next launch instead of having its clock reset to zero (see sweepIdleAtLaunch).
type idleRecord struct {
	Hash    uint64    `json:"hash"`
	Changed time.Time `json:"changed"`
}

// newIdleTrackerFrom seeds a tracker with idle records persisted by a previous
// run, so the idle clock continues from where it left off rather than restarting
// at launch. A nil/empty map yields a fresh tracker.
func newIdleTrackerFrom(timeout time.Duration, persisted map[string]idleRecord) idleTracker {
	t := newIdleTracker(timeout)
	for name, rec := range persisted {
		t.hash[name] = rec.Hash
		t.lastChange[name] = rec.Changed
	}
	return t
}

// snapshot exports the tracker's per-session idle records for persistence. The
// returned map is a copy, safe to hand to a writer goroutine.
func (t idleTracker) snapshot() map[string]idleRecord {
	out := make(map[string]idleRecord, len(t.hash))
	for name, h := range t.hash {
		out[name] = idleRecord{Hash: h, Changed: t.lastChange[name]}
	}
	return out
}

// reap advances idle tracking by one poll and returns the names of sessions that
// have been idle (pane unchanged) for at least the tracker's timeout, sorted. A
// non-positive timeout disables reaping entirely (returns nil). hashes maps
// each currently-live session to its pane fingerprint; busy reports which of them
// are actively generating. reap mutates the tracker in place: a session whose
// fingerprint changed (or is newly seen, or is busy) has its clock reset to now;
// a session that has disappeared from hashes is dropped so tracking can't leak.
//
// The busy guard is belt-and-suspenders — a generating agent's pane already
// changes every poll via its spinner — but it guarantees an agent mid-turn is
// never reaped even if its pane momentarily hashes stable.
func (t *idleTracker) reap(now time.Time, hashes map[string]uint64, busy map[string]bool) []string {
	if t.timeout <= 0 {
		return nil // reaping disabled
	}
	for name := range t.hash {
		if _, live := hashes[name]; !live {
			delete(t.hash, name)
			delete(t.lastChange, name)
		}
	}

	var kill []string
	for name, h := range hashes {
		prev, seen := t.hash[name]
		switch {
		case !seen || prev != h || busy[name]:
			// New, changed, or actively generating: active now.
			t.hash[name] = h
			t.lastChange[name] = now
		case now.Sub(t.lastChange[name]) >= t.timeout:
			kill = append(kill, name)
		}
	}
	sort.Strings(kill)
	return kill
}

// sweepIdleAtLaunch kills sessions that were already idle past the timeout before
// kmux started, so they are gone before the dashboard attaches panes to them. It
// captures each live session's pane once and compares it against the idle records
// persisted by the previous run: a session whose pane still hashes to the stored
// fingerprint, and whose fingerprint last changed at least timeout ago, is reaped.
//
// Crucially, idleness is decided by the pane fingerprint, not a tmux timestamp:
// tmux freezes session_activity while a session is detached, so a detached agent
// that is actively generating would look idle by that clock. Its pane, however,
// keeps changing, so its fresh hash won't match the persisted one and it is
// spared. A session with no persisted record (first run, or one kmux never saw)
// is likewise spared — there is no evidence it has been idle. A non-positive
// timeout disables the sweep. All tmux calls are best-effort: a capture or kill
// failure skips that session rather than aborting the launch.
func sweepIdleAtLaunch(now time.Time, timeout time.Duration, persisted map[string]idleRecord) {
	if timeout <= 0 || len(persisted) == 0 {
		return
	}
	names, err := ListAgentSessions()
	if err != nil {
		return
	}
	hashes := make(map[string]uint64, len(names))
	for _, name := range names {
		text, err := CapturePane(name)
		if err != nil {
			continue // flaky capture: treat as unseen this launch, never kill
		}
		hashes[name] = hashPane(text)
	}
	// reap applies the exact spare/kill rules used during normal polling: a
	// preloaded tracker reaped once against the launch hashes kills only sessions
	// whose pane is unchanged and stale, and resets (spares) everything else.
	t := newIdleTrackerFrom(timeout, persisted)
	for _, name := range t.reap(now, hashes, nil) {
		_ = KillSession(name) // best-effort; a missing session is already gone
	}
}
