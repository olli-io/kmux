package status

import (
	"hash/fnv"
	"sort"
	"time"

	"github.com/olli-io/kmux/internal/tmux"
)

// Idleness is measured by pane stability, not the attention state: a session counts
// as idle only while its captured pane is byte-for-byte identical across polls. A
// generating or actively-typed session keeps changing and never accrues idle time;
// a finished agent at a static screen does. Detached sessions are tracked too, since
// tmux keeps their buffers and process alive.

// HashPane reduces a captured pane to a 64-bit fingerprint so the idle tracker
// can detect "unchanged since last poll" without retaining the full text.
func HashPane(text string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(text))
	return h.Sum64()
}

// IdleTracker remembers, per session, the last pane fingerprint and when it last
// changed — the only mutable state behind the idle-kill policy.
type IdleTracker struct {
	timeout    time.Duration        // idle threshold; <= 0 disables reaping
	hash       map[string]uint64    // session -> last pane fingerprint
	lastChange map[string]time.Time // session -> when the fingerprint last changed
}

func newIdleTracker(timeout time.Duration) IdleTracker {
	return IdleTracker{
		timeout:    timeout,
		hash:       map[string]uint64{},
		lastChange: map[string]time.Time{},
	}
}

// IdleRecord is the persisted per-session idle clock. Persisting it lets idle
// tracking survive a restart, so a session unchanged across runs is reaped on the
// next launch instead of having its clock reset (see SweepIdleAtLaunch).
type IdleRecord struct {
	Hash    uint64    `json:"hash"`
	Changed time.Time `json:"changed"`
}

// NewIdleTrackerFrom seeds a tracker with persisted records so the idle clock
// continues where it left off. A nil/empty map yields a fresh tracker.
func NewIdleTrackerFrom(timeout time.Duration, persisted map[string]IdleRecord) IdleTracker {
	t := newIdleTracker(timeout)
	for name, rec := range persisted {
		t.hash[name] = rec.Hash
		t.lastChange[name] = rec.Changed
	}
	return t
}

// Snapshot exports the tracker's per-session idle records for persistence. The
// returned map is a copy, safe to hand to a writer goroutine.
func (t IdleTracker) Snapshot() map[string]IdleRecord {
	out := make(map[string]IdleRecord, len(t.hash))
	for name, h := range t.hash {
		out[name] = IdleRecord{Hash: h, Changed: t.lastChange[name]}
	}
	return out
}

// Reap advances idle tracking by one poll and returns the sorted names idle (pane
// unchanged) for at least the timeout; a non-positive timeout disables reaping.
// hashes maps live sessions to fingerprints, busy reports which are generating. A
// session that changed, is new, or is busy has its clock reset; one gone from hashes
// is dropped. The busy guard is belt-and-suspenders — a generating pane already
// changes each poll — but guarantees a mid-turn agent is never reaped.
func (t *IdleTracker) Reap(now time.Time, hashes map[string]uint64, busy map[string]bool) []string {
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

// SweepIdleAtLaunch kills sessions already idle past the timeout before kmux
// started, so they're gone before the dashboard attaches panes. It compares each
// live session's freshly captured pane against the persisted records.
//
// Idleness is decided by the fingerprint, not a tmux timestamp: tmux freezes
// session_activity while detached, so a detached-but-generating agent looks idle by
// that clock, yet its pane keeps changing so its hash won't match and it is spared.
// A session with no persisted record is spared too. All tmux calls are best-effort.
func SweepIdleAtLaunch(now time.Time, timeout time.Duration, persisted map[string]IdleRecord) {
	if timeout <= 0 || len(persisted) == 0 {
		return
	}
	names, err := tmux.ListAgentSessions()
	if err != nil {
		return
	}
	hashes := make(map[string]uint64, len(names))
	for _, name := range names {
		text, err := tmux.CapturePane(name)
		if err != nil {
			continue // flaky capture: treat as unseen this launch, never kill
		}
		hashes[name] = HashPane(text)
	}
	// Reap applies the same spare/kill rules as normal polling against the launch
	// hashes: it kills only sessions whose pane is unchanged and stale.
	t := NewIdleTrackerFrom(timeout, persisted)
	for _, name := range t.Reap(now, hashes, nil) {
		_ = tmux.KillSession(name) // best-effort; a missing session is already gone
	}
}
