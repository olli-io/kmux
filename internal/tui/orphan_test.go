package tui

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// isMissingDir flags a path only when it definitively does not exist: an existing
// directory and an empty path (tmux reported none) both read as present, so a
// live session is never mistaken for orphaned.
func TestIsMissingDir(t *testing.T) {
	dir := t.TempDir()
	if isMissingDir(dir) {
		t.Errorf("isMissingDir(%q) = true for an existing dir, want false", dir)
	}
	if isMissingDir("") {
		t.Errorf("isMissingDir(\"\") = true, want false (no path reported)")
	}

	gone := filepath.Join(dir, "removed")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if !isMissingDir(gone) {
		t.Errorf("isMissingDir(%q) = false for a removed dir, want true", gone)
	}
}

// A freshly-orphaned session is killed only after its directory has been missing
// for orphanConfirmPolls consecutive polls, so a single transient stat miss can't
// reap a live agent.
func TestTrackOrphansConfirmsBeforeKill(t *testing.T) {
	m := &model{sessions: []string{"a", "b"}, orphanStrikes: map[string]int{}}

	// First poll: a is missing — one strike, no kill yet (confirm threshold is 2).
	if kill := m.trackOrphans([]string{"a"}); len(kill) != 0 {
		t.Fatalf("first miss killed %v, want none", kill)
	}
	if m.orphanStrikes["a"] != 1 {
		t.Errorf("strike for a = %d, want 1", m.orphanStrikes["a"])
	}

	// Second consecutive poll: a is killed and its counter cleared.
	kill := m.trackOrphans([]string{"a"})
	if !slices.Equal(kill, []string{"a"}) {
		t.Fatalf("second miss killed %v, want [a]", kill)
	}
	if _, ok := m.orphanStrikes["a"]; ok {
		t.Errorf("strike for a not cleared after kill")
	}
}

// A directory that reappears resets the counter, so an intermittent miss never
// accumulates into a kill.
func TestTrackOrphansResetsOnRecovery(t *testing.T) {
	m := &model{sessions: []string{"a"}, orphanStrikes: map[string]int{}}
	m.trackOrphans([]string{"a"}) // strike 1
	if kill := m.trackOrphans(nil); len(kill) != 0 {
		t.Fatalf("recovery killed %v, want none", kill)
	}
	if _, ok := m.orphanStrikes["a"]; ok {
		t.Errorf("strike for a not reset after its dir reappeared")
	}
	// A later miss must start over from one strike, not resume at the old count.
	if kill := m.trackOrphans([]string{"a"}); len(kill) != 0 || m.orphanStrikes["a"] != 1 {
		t.Errorf("post-recovery miss killed %v / strike %d, want none / 1", kill, m.orphanStrikes["a"])
	}
}

// Only managed (in-list) sessions are tracked: an orphaned name kmux doesn't list
// — e.g. another project's session in scoped mode — is never counted or killed.
func TestTrackOrphansIgnoresUnlistedSessions(t *testing.T) {
	m := &model{sessions: []string{"a"}, orphanStrikes: map[string]int{}}
	m.trackOrphans([]string{"other"})
	m.trackOrphans([]string{"other"})
	if len(m.orphanStrikes) != 0 {
		t.Errorf("tracked unlisted session: strikes %v", m.orphanStrikes)
	}
}

// A tracked session that leaves the list (its agent exited on its own) is dropped
// from the tracker rather than leaking a stale counter.
func TestTrackOrphansDropsGoneSessions(t *testing.T) {
	m := &model{sessions: []string{"a"}, orphanStrikes: map[string]int{}}
	m.trackOrphans([]string{"a"}) // strike 1 for a
	m.sessions = nil              // a is gone from tmux
	m.trackOrphans(nil)
	if len(m.orphanStrikes) != 0 {
		t.Errorf("stale strike left after session gone: %v", m.orphanStrikes)
	}
}
