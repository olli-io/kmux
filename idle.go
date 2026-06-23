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
