package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseWorktrees(t *testing.T) {
	out := "worktree /home/u/git/proj\n" +
		"HEAD abc\n" +
		"branch refs/heads/main\n" +
		"\n" +
		"worktree /home/u/git/proj-feature\n" +
		"HEAD def\n" +
		"branch refs/heads/feature\n" +
		"\n" +
		"worktree /home/u/git/proj-detached\n" +
		"HEAD 123\n" +
		"detached\n"

	mainBranch, wts := parseWorktrees(out, "/home/u/git/proj")
	if mainBranch != "main" {
		t.Errorf("mainBranch = %q, want %q", mainBranch, "main")
	}
	if len(wts) != 2 {
		t.Fatalf("got %d worktrees, want 2: %+v", len(wts), wts)
	}
	// The "proj-" project prefix is stripped from each worktree's short name, so
	// the row matches the "<project>/<segment>~cl" tmux session convention.
	if wts[0].Name != "detached" || wts[0].Branch != "(detached)" {
		t.Errorf("wt[0] = %+v", wts[0]) // sorted by name, detached comes first
	}
	if wts[1].Name != "feature" || wts[1].Branch != "feature" {
		t.Errorf("wt[1] = %+v", wts[1])
	}
}

func TestWorktreeSegment(t *testing.T) {
	cases := []struct {
		base, project, want string
	}{
		{"wattery-app.migrate-user-invites", "wattery-app", "migrate-user-invites"}, // dot separator
		{"proj_feature", "proj", "feature"},                                         // underscore separator
		{"proj-feature", "proj", "feature"},                                         // hyphen separator
		{"feature", "proj", "feature"},                                              // no project prefix: unchanged
		{"projection", "proj", "projection"},                                        // prefix not followed by a separator: unchanged
		{"proj", "proj", "proj"},                                                    // exactly the project name: unchanged
		{"proj.", "proj", "proj."},                                                  // empty segment after prefix: unchanged
	}
	for _, c := range cases {
		if got := worktreeSegment(c.base, c.project); got != c.want {
			t.Errorf("worktreeSegment(%q, %q) = %q, want %q", c.base, c.project, got, c.want)
		}
	}
}

func TestParseStatus(t *testing.T) {
	cases := []struct {
		name                  string
		out                   string
		wantDirty             bool
		wantAhead, wantBehind int
		wantUpstream          bool
	}{
		{
			name: "clean with upstream",
			out: "# branch.oid abc123\n# branch.head main\n" +
				"# branch.upstream origin/main\n# branch.ab +0 -0\n",
			wantUpstream: true,
		},
		{
			name: "dirty with ahead/behind",
			out: "# branch.oid abc123\n# branch.head main\n" +
				"# branch.upstream origin/main\n# branch.ab +2 -3\n" +
				"1 .M N... 100644 100644 100644 aaa bbb internal/foo.go\n? untracked.txt\n",
			wantDirty: true, wantAhead: 2, wantBehind: 3, wantUpstream: true,
		},
		{
			name:      "dirty no upstream (linked worktree)",
			out:       "# branch.oid ff34c\n# branch.head feature\n1 .M N... 100644 100644 100644 aaa bbb pkg/x.ts\n",
			wantDirty: true,
		},
		{
			name: "detached head, clean, no upstream",
			out:  "# branch.oid abc123\n# branch.head (detached)\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dirty, ahead, behind, upstream := parseStatus(c.out)
			if dirty != c.wantDirty || ahead != c.wantAhead || behind != c.wantBehind || upstream != c.wantUpstream {
				t.Errorf("parseStatus(%q) = (dirty=%v ahead=%d behind=%d upstream=%v), want (dirty=%v ahead=%d behind=%d upstream=%v)",
					c.name, dirty, ahead, behind, upstream, c.wantDirty, c.wantAhead, c.wantBehind, c.wantUpstream)
			}
		})
	}
}

// resetTopoCache clears the package topology cache so each cache test starts
// clean regardless of order.
func resetTopoCache() {
	topoMu.Lock()
	topoCache = map[string]topoEntry{}
	topoMu.Unlock()
}

// lookupTopo hits only when the mtime matches what was stored, and returns a copy
// the caller can mutate without corrupting the cache.
func TestTopoCacheLookupStore(t *testing.T) {
	resetTopoCache()
	dir := "/g/proj"
	wts := []Worktree{{Name: "feature", Path: "/g/proj-feature", Branch: "feature"}}

	if _, _, hit := lookupTopo(dir, 100); hit {
		t.Fatalf("empty cache reported a hit")
	}

	storeTopo(dir, 100, "main", wts)

	branch, got, hit := lookupTopo(dir, 100)
	if !hit || branch != "main" || len(got) != 1 || got[0].Name != "feature" {
		t.Fatalf("hit=%v branch=%q got=%+v, want main/[feature]", hit, branch, got)
	}

	// A different mtime (the repo changed) misses, forcing a re-list.
	if _, _, hit := lookupTopo(dir, 101); hit {
		t.Errorf("stale mtime reported a hit")
	}

	// The returned slice is a copy: mutating it must not change the cached entry.
	got[0].Branch = "mutated"
	branch2, got2, _ := lookupTopo(dir, 100)
	if got2[0].Branch != "feature" || branch2 != "main" {
		t.Errorf("cache corrupted by caller mutation: %+v", got2)
	}
}

// listWorktrees skips the git spawn when .git is unchanged and re-lists once a
// topology change (worktree add) bumps .git's mtime — exercised against real
// temporary git repos.
func TestListWorktreesCacheInvalidation(t *testing.T) {
	resetTopoCache()
	// Resolve symlinks so the path matches what git reports (on macOS t.TempDir()
	// lives under /var -> /private/var, which would otherwise make the main
	// worktree look like a linked one to parseWorktrees).
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init")
	runGit(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init")

	branch, wts := listWorktrees(dir)
	if len(wts) != 0 {
		t.Fatalf("fresh repo has %d worktrees, want 0", len(wts))
	}

	// Cached: mtime unchanged, so a second call must serve from cache (same result).
	mt, ok := gitMtime(dir)
	if !ok {
		t.Fatalf("gitMtime failed for a main worktree")
	}
	if _, _, hit := lookupTopo(dir, mt); !hit {
		t.Fatalf("first listWorktrees did not populate the cache")
	}

	// Add a worktree: this bumps .git's mtime, so the cache must miss and re-list.
	wtDir := dir + "-feature"
	runGit(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "worktree", "add", wtDir, "-b", "feature")
	t.Cleanup(func() { _ = os.RemoveAll(wtDir) })

	_, wts2 := listWorktrees(dir)
	if len(wts2) != 1 || wts2[0].Branch != "feature" {
		t.Fatalf("after worktree add, listWorktrees = %+v, want one 'feature' worktree", wts2)
	}
	_ = branch
}

// runGit runs a git command in dir, failing the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
