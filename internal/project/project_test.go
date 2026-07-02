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
