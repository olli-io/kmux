package main

import "testing"

func TestMatchProject(t *testing.T) {
	names := []string{"kmux", "gstack", "gstack_extra", "my_proj"}
	cases := []struct {
		rem      string
		proj, wt string
		ok       bool
	}{
		{"kmux", "kmux", "", true},                    // main worktree, no segment
		{"gstack_feature", "gstack", "feature", true}, // project + worktree
		{"gstack_extra", "gstack_extra", "", true},    // longest prefix wins over "gstack"
		{"gstack_extra_wt", "gstack_extra", "wt", true},
		{"my_proj_wt", "my_proj", "wt", true}, // project name contains underscore
		{"unknown_thing", "", "", false},      // no match
	}
	for _, c := range cases {
		proj, wt, ok := matchProject(c.rem, names)
		if proj != c.proj || wt != c.wt || ok != c.ok {
			t.Errorf("matchProject(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.rem, proj, wt, ok, c.proj, c.wt, c.ok)
		}
	}
}

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
	// the row matches the "<project>_<segment>_cl" tmux session convention.
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
		{"proj", "proj", "proj"},                                                     // exactly the project name: unchanged
		{"proj.", "proj", "proj."},                                                   // empty segment after prefix: unchanged
	}
	for _, c := range cases {
		if got := worktreeSegment(c.base, c.project); got != c.want {
			t.Errorf("worktreeSegment(%q, %q) = %q, want %q", c.base, c.project, got, c.want)
		}
	}
}

func TestExpectedSession(t *testing.T) {
	if got := expectedSession("kmux", ""); got != "kmux_cl" {
		t.Errorf("main session = %q, want %q", got, "kmux_cl")
	}
	if got := expectedSession("kmux", "feat"); got != "kmux_feat_cl" {
		t.Errorf("worktree session = %q, want %q", got, "kmux_feat_cl")
	}
}

func TestSessionForKind(t *testing.T) {
	if got := sessionForKind("kmux_cl", "claude"); got != "kmux_cl" {
		t.Errorf("claude = %q, want %q", got, "kmux_cl")
	}
	if got := sessionForKind("kmux_cl", "opencode"); got != "kmux_oc" {
		t.Errorf("opencode = %q, want %q", got, "kmux_oc")
	}
	if got := sessionForKind("kmux_feat_cl", "opencode"); got != "kmux_feat_oc" {
		t.Errorf("worktree opencode = %q, want %q", got, "kmux_feat_oc")
	}
}

func TestAgentCommand(t *testing.T) {
	if got := agentCommand("claude"); got != "claude" {
		t.Errorf("claude cmd = %q, want %q", got, "claude")
	}
	if got := agentCommand("opencode"); got != "opencode" {
		t.Errorf("opencode cmd = %q, want %q", got, "opencode")
	}
}

func TestBuildProjectRows(t *testing.T) {
	projects := []Project{
		{Name: "solo", Path: "/g/solo", Branch: "main"}, // no worktrees
		{Name: "multi", Path: "/g/multi", Branch: "main", Worktrees: []Worktree{
			{Name: "multi-feat", Path: "/g/multi-feat", Branch: "feat"},
		}},
	}

	noSession := func(string) bool { return false }
	rows := buildProjectRows(projects, map[string]bool{}, noSession, rowDeco{})

	// solo: single non-collapsible actionable leaf.
	if rows[0].collapsible {
		t.Errorf("solo row should not be collapsible")
	}
	if rows[0].dir != "/g/solo" || rows[0].session != "solo_cl" {
		t.Errorf("solo row = {dir:%q session:%q}, want {/g/solo solo_cl}", rows[0].dir, rows[0].session)
	}

	// multi: collapsible folder header (not actionable), then main worktree
	// first, then the linked worktree.
	if !rows[1].collapsible || rows[1].key != "proj:multi" {
		t.Errorf("multi header = {collapsible:%v key:%q}, want {true proj:multi}", rows[1].collapsible, rows[1].key)
	}
	if rows[1].session != "" || rows[1].dir != "" {
		t.Errorf("folder header should not be actionable: %+v", rows[1])
	}
	if rows[2].depth != 1 || rows[2].dir != "/g/multi" || rows[2].session != "multi_cl" {
		t.Errorf("main worktree row = %+v, want depth 1, /g/multi, multi_cl", rows[2])
	}
	if rows[3].depth != 1 || rows[3].dir != "/g/multi-feat" || rows[3].session != "multi_multi-feat_cl" {
		t.Errorf("linked worktree row = %+v, want depth 1, /g/multi-feat, multi_multi-feat_cl", rows[3])
	}
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4: %+v", len(rows), rows)
	}

	// Collapsing the multi folder hides both worktree children.
	rows = buildProjectRows(projects, map[string]bool{"proj:multi": true}, noSession, rowDeco{})
	if len(rows) != 2 {
		t.Fatalf("collapsed: got %d rows, want 2 (solo + multi header)", len(rows))
	}
}

func TestBuildSessionRows(t *testing.T) {
	sessions := []string{"kmux_cl", "gstack_feat_cl", "gstack_feat_oc", "orphan_cl"}
	names := []string{"kmux", "gstack"}
	attached := func(string) bool { return false }
	detached := func(string) bool { return false }

	rows := buildSessionRows(sessions, names, map[string]bool{}, attached, detached, rowDeco{})

	// Expect: gstack(0) > 2 worktree sessions(1) directly under it (no worktree
	// node); kmux(0) > kmux_cl(1); (ungrouped)(0) > orphan_cl(1). Projects sort
	// before ungrouped.
	var labels []string
	for _, r := range rows {
		labels = append(labels, r.label)
	}
	want := []string{"gstack", "gstack_feat_cl", "gstack_feat_oc", "kmux", "kmux_cl", ungrouped, "orphan_cl"}
	if len(labels) != len(want) {
		t.Fatalf("got %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("row %d = %q, want %q (all: %v)", i, labels[i], want[i], labels)
		}
	}

	// Worktree sessions hang directly off the project at depth 1, with no
	// intermediate "feat" worktree header.
	for _, r := range rows {
		if r.label == "feat" {
			t.Errorf("worktree node %q should not be rendered", r.label)
		}
		if r.label == "gstack_feat_cl" && r.depth != 1 {
			t.Errorf("worktree session depth = %d, want 1", r.depth)
		}
	}

	// Collapsing the gstack project hides its worktree sessions.
	rows = buildSessionRows(sessions, names, map[string]bool{"sess:gstack": true}, attached, detached, rowDeco{})
	for _, r := range rows {
		if r.label == "gstack_feat_cl" {
			t.Errorf("collapsed gstack should hide %q", r.label)
		}
	}
}
