package status

import (
	"testing"

	"github.com/olli-io/kmux/internal/project"
)

// TestLoadProjectsMissing verifies that with no cache written yet the loader
// reports ok=false (so the idler falls back to a live scan) without an error.
func TestLoadProjectsMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	ps, ok, err := LoadProjects()
	if err != nil {
		t.Fatalf("LoadProjects() on missing cache = err %v, want nil", err)
	}
	if ok || ps != nil {
		t.Fatalf("LoadProjects() on missing cache = (%v, ok=%v), want (nil, false)", ps, ok)
	}
}

// TestSaveLoadProjectsRoundTrip verifies the project cache survives a
// save/load cycle, worktrees and status flags included.
func TestSaveLoadProjectsRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	want := []project.Project{
		{
			Name:   "alpha",
			Path:   "/home/u/git/alpha",
			Branch: "main",
			Dirty:  true,
			Ahead:  2,
			Worktrees: []project.Worktree{
				{Name: "feature", Path: "/home/u/git/alpha.feature", Branch: "feature", Upstream: true},
			},
		},
		{Name: "beta", Path: "/home/u/git/beta", Branch: "(detached)"},
	}

	if err := SaveProjects(want); err != nil {
		t.Fatalf("SaveProjects() = %v", err)
	}

	got, ok, err := LoadProjects()
	if err != nil {
		t.Fatalf("LoadProjects() = %v", err)
	}
	if !ok {
		t.Fatal("LoadProjects() ok=false after SaveProjects, want true")
	}
	if len(got) != len(want) {
		t.Fatalf("LoadProjects() returned %d projects, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].Branch != want[i].Branch || got[i].Dirty != want[i].Dirty || got[i].Ahead != want[i].Ahead {
			t.Errorf("project %d = %+v, want %+v", i, got[i], want[i])
		}
		if len(got[i].Worktrees) != len(want[i].Worktrees) {
			t.Errorf("project %d worktrees = %d, want %d", i, len(got[i].Worktrees), len(want[i].Worktrees))
		}
	}
}

// TestLoadProjectsEmpty verifies an empty cached list reports ok=false so a stale
// empty write doesn't lock the idler into showing no projects — it falls back to a
// live scan instead.
func TestLoadProjectsEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := SaveProjects(nil); err != nil {
		t.Fatalf("SaveProjects(nil) = %v", err)
	}
	if _, ok, err := LoadProjects(); err != nil || ok {
		t.Fatalf("LoadProjects() after empty save = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}
