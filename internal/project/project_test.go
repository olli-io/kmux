package project

import "testing"

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
