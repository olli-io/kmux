package main

import (
	"testing"

	"github.com/olli-io/kmux/internal/project"
	"github.com/olli-io/kmux/internal/status"
)

func TestMatchProject(t *testing.T) {
	names := []string{"kmux", "gstack", "gstack_extra", "my_proj"}
	cases := []struct {
		rem      string
		proj, wt string
		ok       bool
	}{
		{"kmux", "kmux", "", true},                      // main worktree, no segment
		{"gstack/feature", "gstack", "feature", true},   // project + worktree
		{"gstack_extra", "gstack_extra", "", true},      // exact project name (underscore in name)
		{"gstack_extra/wt", "gstack_extra", "wt", true}, // longest prefix wins over "gstack"
		{"my_proj/wt", "my_proj", "wt", true},           // project name contains underscore
		{"unknown_thing", "", "", false},                // no match
	}
	for _, c := range cases {
		proj, wt, ok := matchProject(c.rem, names)
		if proj != c.proj || wt != c.wt || ok != c.ok {
			t.Errorf("matchProject(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.rem, proj, wt, ok, c.proj, c.wt, c.ok)
		}
	}
}

func TestExpectedSession(t *testing.T) {
	if got := expectedSession("kmux", ""); got != "kmux~cl" {
		t.Errorf("main session = %q, want %q", got, "kmux~cl")
	}
	if got := expectedSession("kmux", "feat"); got != "kmux/feat~cl" {
		t.Errorf("worktree session = %q, want %q", got, "kmux/feat~cl")
	}
}

func TestSessionForKind(t *testing.T) {
	if got := sessionForKind("kmux~cl", "claude"); got != "kmux~cl" {
		t.Errorf("claude = %q, want %q", got, "kmux~cl")
	}
	if got := sessionForKind("kmux~cl", "opencode"); got != "kmux~oc" {
		t.Errorf("opencode = %q, want %q", got, "kmux~oc")
	}
	if got := sessionForKind("kmux/feat~cl", "opencode"); got != "kmux/feat~oc" {
		t.Errorf("worktree opencode = %q, want %q", got, "kmux/feat~oc")
	}
}

func TestAgentCommand(t *testing.T) {
	if got := agentCommand("claude"); got != "claude --continue" {
		t.Errorf("claude cmd = %q, want %q", got, "claude --continue")
	}
	if got := agentCommand("opencode"); got != "opencode --continue" {
		t.Errorf("opencode cmd = %q, want %q", got, "opencode --continue")
	}
}

func TestBuildProjectRows(t *testing.T) {
	projects := []project.Project{
		{Name: "solo", Path: "/g/solo", Branch: "main"}, // no worktrees
		{Name: "multi", Path: "/g/multi", Branch: "main", Worktrees: []project.Worktree{
			{Name: "multi-feat", Path: "/g/multi-feat", Branch: "feat"},
		}},
	}

	noSession := func(string) bool { return false }
	rows := buildProjectRows(projects, map[string]bool{}, noSession, rowDeco{})

	// Multi-worktree folders sort to the top, single-worktree leaves after.
	// multi: collapsible folder header (not actionable), then main worktree
	// first, then the linked worktree.
	if !rows[0].collapsible || rows[0].key != "proj:multi" {
		t.Errorf("multi header = {collapsible:%v key:%q}, want {true proj:multi}", rows[0].collapsible, rows[0].key)
	}
	if rows[0].session != "" || rows[0].dir != "" {
		t.Errorf("folder header should not be actionable: %+v", rows[0])
	}
	if rows[1].depth != 1 || rows[1].dir != "/g/multi" || rows[1].session != "multi~cl" {
		t.Errorf("main worktree row = %+v, want depth 1, /g/multi, multi~cl", rows[1])
	}
	if rows[2].depth != 1 || rows[2].dir != "/g/multi-feat" || rows[2].session != "multi/multi-feat~cl" {
		t.Errorf("linked worktree row = %+v, want depth 1, /g/multi-feat, multi/multi-feat~cl", rows[2])
	}

	// solo: single non-collapsible actionable leaf, after the multi folder.
	if rows[3].collapsible {
		t.Errorf("solo row should not be collapsible")
	}
	if rows[3].dir != "/g/solo" || rows[3].session != "solo~cl" {
		t.Errorf("solo row = {dir:%q session:%q}, want {/g/solo solo~cl}", rows[3].dir, rows[3].session)
	}
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4: %+v", len(rows), rows)
	}

	// Collapsing the multi folder hides both worktree children.
	rows = buildProjectRows(projects, map[string]bool{"proj:multi": true}, noSession, rowDeco{})
	if len(rows) != 2 {
		t.Fatalf("collapsed: got %d rows, want 2 (multi header + solo)", len(rows))
	}
}

func TestBuildSessionRows(t *testing.T) {
	sessions := []string{"kmux~cl", "gstack/feat~cl", "gstack/feat~oc", "orphan~cl"}
	names := []string{"kmux", "gstack"}
	attn := map[string]status.AttentionState{}
	attached := func(string) bool { return false }
	detached := func(string) bool { return false }

	rows := buildSessionRows(sessions, names, map[string]bool{}, attn, attached, detached, rowDeco{})

	// Mirroring the Projects pane: gstack has two sessions, so it is a collapsible
	// folder (sorted to the top) with both worktree sessions hanging directly off
	// it at depth 1 (no worktree node). kmux has a single session, so it is a bare
	// leaf with no folder header; the ungrouped orphan is likewise a single leaf,
	// emitted last. Leaf labels drop the agent suffix (the kind shows as a trailing
	// badge), so both gstack worktree sessions read as "gstack/feat".
	if !rows[0].collapsible || rows[0].key != "sess:gstack" {
		t.Errorf("gstack header = {collapsible:%v key:%q}, want {true sess:gstack}", rows[0].collapsible, rows[0].key)
	}
	if rows[1].label != "gstack/feat" || rows[1].depth != 1 {
		t.Errorf("gstack child[0] = {label:%q depth:%d}, want {gstack/feat 1}", rows[1].label, rows[1].depth)
	}
	if rows[2].label != "gstack/feat" || rows[2].depth != 1 {
		t.Errorf("gstack child[1] = {label:%q depth:%d}, want {gstack/feat 1}", rows[2].label, rows[2].depth)
	}
	// kmux: single-session bare leaf (not collapsible, no header) at depth 0.
	if rows[3].collapsible || rows[3].label != "kmux" || rows[3].depth != 0 || rows[3].session != "kmux~cl" {
		t.Errorf("kmux leaf = %+v, want {collapsible:false label:kmux depth:0 session:kmux~cl}", rows[3])
	}
	// ungrouped orphan: single-session bare leaf, last.
	if rows[4].collapsible || rows[4].label != "orphan" || rows[4].depth != 0 {
		t.Errorf("orphan leaf = %+v, want {collapsible:false label:orphan depth:0}", rows[4])
	}
	if len(rows) != 5 {
		t.Fatalf("got %d rows, want 5: %+v", len(rows), rows)
	}

	// Worktree sessions carry their full session name (for actions) but hang
	// directly off the project at depth 1, with no intermediate "feat" header.
	for _, r := range rows {
		if r.label == "feat" {
			t.Errorf("worktree node %q should not be rendered", r.label)
		}
		if r.session == "gstack/feat~cl" && r.depth != 1 {
			t.Errorf("worktree session depth = %d, want 1", r.depth)
		}
	}

	// Collapsing the gstack project hides its worktree sessions.
	rows = buildSessionRows(sessions, names, map[string]bool{"sess:gstack": true}, attn, attached, detached, rowDeco{})
	for _, r := range rows {
		if r.session == "gstack/feat~cl" {
			t.Errorf("collapsed gstack should hide %q", r.session)
		}
	}
}
