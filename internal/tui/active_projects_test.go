package tui

import (
	"testing"

	"github.com/olli-io/kmux/internal/agent"
	"github.com/olli-io/kmux/internal/project"
)

// TestActiveProjectPaths verifies that only projects with a running session are
// returned, each once, and that a session matching no project (orphan) is
// skipped.
func TestActiveProjectPaths(t *testing.T) {
	alpha := project.Project{Name: "alpha", Path: "/home/u/git/alpha"}
	beta := project.Project{Name: "beta", Path: "/home/u/git/beta"}
	gamma := project.Project{Name: "gamma", Path: "/home/u/git/gamma"}

	m := model{projects: []project.Project{alpha, beta, gamma}}
	m.sessions = []string{
		agent.ExpectedSession(alpha.Path, ""),        // alpha, main
		agent.ExpectedSession(alpha.Path, "feature"), // alpha again, a worktree
		agent.ExpectedSession(gamma.Path, ""),        // gamma
		agent.OrphanSession("/tmp/scratch"),          // matches no project
	}

	got := m.activeProjectPaths()
	want := map[string]bool{alpha.Path: true, gamma.Path: true}
	if len(got) != len(want) {
		t.Fatalf("activeProjectPaths() = %v, want the %d distinct active paths %v", got, len(want), want)
	}
	seen := map[string]bool{}
	for _, p := range got {
		if !want[p] {
			t.Errorf("activeProjectPaths() returned unexpected path %q", p)
		}
		if seen[p] {
			t.Errorf("activeProjectPaths() returned %q more than once", p)
		}
		seen[p] = true
	}
	// beta has no session, so it must be absent.
	if seen[beta.Path] {
		t.Errorf("activeProjectPaths() included %q which has no session", beta.Path)
	}
}

// TestActiveProjectPathsNoSessions confirms an idle model (no sessions) yields no
// active paths, so the ticker skips its scan entirely.
func TestActiveProjectPathsNoSessions(t *testing.T) {
	m := model{projects: []project.Project{{Name: "alpha", Path: "/home/u/git/alpha"}}}
	if got := m.activeProjectPaths(); len(got) != 0 {
		t.Fatalf("activeProjectPaths() = %v, want empty for an idle model", got)
	}
}

// TestMergeProjects verifies that a partial update patches only matching projects
// by path, preserves the rest untouched, and keeps order.
func TestMergeProjects(t *testing.T) {
	existing := []project.Project{
		{Name: "alpha", Path: "/a", Dirty: false, Ahead: 0},
		{Name: "beta", Path: "/b", Dirty: false, Ahead: 0},
		{Name: "gamma", Path: "/c", Dirty: true, Ahead: 3},
	}
	// Fresh status for alpha and gamma only; beta must be left as-is.
	updates := []project.Project{
		{Name: "alpha", Path: "/a", Dirty: true, Ahead: 1},
		{Name: "gamma", Path: "/c", Dirty: false, Ahead: 0},
	}

	got := mergeProjects(existing, updates)
	if len(got) != 3 {
		t.Fatalf("mergeProjects returned %d projects, want 3", len(got))
	}
	if !got[0].Dirty || got[0].Ahead != 1 {
		t.Errorf("alpha not patched: %+v", got[0])
	}
	if got[1].Dirty || got[1].Ahead != 0 {
		t.Errorf("beta should be untouched: %+v", got[1])
	}
	if got[2].Dirty || got[2].Ahead != 0 {
		t.Errorf("gamma not patched: %+v", got[2])
	}
	// Order preserved.
	if got[0].Name != "alpha" || got[1].Name != "beta" || got[2].Name != "gamma" {
		t.Errorf("order changed: %q, %q, %q", got[0].Name, got[1].Name, got[2].Name)
	}
}

// TestMergeProjectsEmpty confirms an empty update set is a no-op returning the
// original slice (the idle-tick case where nothing was rescanned).
func TestMergeProjectsEmpty(t *testing.T) {
	existing := []project.Project{{Name: "alpha", Path: "/a", Dirty: true}}
	got := mergeProjects(existing, nil)
	if len(got) != 1 || !got[0].Dirty {
		t.Fatalf("mergeProjects(existing, nil) = %+v, want existing unchanged", got)
	}
}
