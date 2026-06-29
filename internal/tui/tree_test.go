package tui

import (
	"strings"
	"testing"

	"github.com/olli-io/kmux/internal/agent"
	"github.com/olli-io/kmux/internal/project"
	"github.com/olli-io/kmux/internal/status"
)

// TestSessionRowOrphanGlyph checks that an orphaned (∅-marked) session row shows
// the orphan glyph after its clean basename label — e.g. "kmux ∅" — while a
// normal repo session shows no glyph.
func TestSessionRowOrphanGlyph(t *testing.T) {
	orphan := agent.OrphanSession("/tmp/kmux") // ∅/tmp/kmux‧CC
	row := rowDeco{}.session(orphan, 1, status.AttnUnknown, false, false)
	if !strings.Contains(row.label, "kmux") {
		t.Errorf("orphan label %q missing basename %q", row.label, "kmux")
	}
	if !strings.Contains(row.label, orphanGlyph) {
		t.Errorf("orphan label %q missing %q glyph", row.label, orphanGlyph)
	}

	repo := rowDeco{}.session("/g/kmux‧CC", 1, status.AttnUnknown, false, false)
	if strings.Contains(repo.label, orphanGlyph) {
		t.Errorf("repo label %q should not carry the orphan glyph", repo.label)
	}
}

func TestBuildProjectRows(t *testing.T) {
	projects := []project.Project{
		{Name: "solo", Path: "/g/solo", Branch: "main"}, // no worktrees
		{Name: "multi", Path: "/g/multi", Branch: "main", Worktrees: []project.Worktree{
			{Name: "multi-feat", Path: "/g/multi-feat", Branch: "feat"},
		}},
	}

	noSession := func(string) liveState { return liveNone }
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
	if rows[1].depth != 1 || rows[1].dir != "/g/multi" || rows[1].session != "/g/multi‧CC" {
		t.Errorf("main worktree row = %+v, want depth 1, /g/multi, /g/multi‧CC", rows[1])
	}
	if rows[2].depth != 1 || rows[2].dir != "/g/multi-feat" || rows[2].session != "/g/multi@multi-feat‧CC" {
		t.Errorf("linked worktree row = %+v, want depth 1, /g/multi-feat, /g/multi@multi-feat‧CC", rows[2])
	}

	// solo: single non-collapsible actionable leaf, after the multi folder.
	if rows[3].collapsible {
		t.Errorf("solo row should not be collapsible")
	}
	if rows[3].dir != "/g/solo" || rows[3].session != "/g/solo‧CC" {
		t.Errorf("solo row = {dir:%q session:%q}, want {/g/solo /g/solo‧CC}", rows[3].dir, rows[3].session)
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
	// Sessions are keyed by project path; names holds the projects' main paths.
	sessions := []string{"/g/kmux‧CC", "/g/gstack@feat‧CC", "/g/gstack@feat‧OC", "/g/orphan‧CC"}
	names := []string{"/g/kmux", "/g/gstack"}
	attn := map[string]status.AttentionState{}
	attached := func(string) bool { return false }
	detached := func(string) bool { return false }

	rows := buildSessionRows(sessions, names, map[string]bool{}, attn, attached, detached, rowDeco{})

	// Mirroring the Projects pane: gstack has two sessions, so it is a collapsible
	// folder (sorted to the top, at depth 1, labeled by project name) with both
	// worktree sessions hanging directly off it at depth 2. kmux has a single
	// session, so it is a bare leaf with no folder header; the ungrouped orphan is
	// likewise a single leaf, emitted last. Leaf labels follow the Projects pane: a
	// worktree session shows the worktree name ("feat"), a main session the project
	// name ("kmux").
	if !rows[0].collapsible || rows[0].key != "sess:/g/gstack" || rows[0].depth != 1 {
		t.Errorf("gstack header = {collapsible:%v key:%q depth:%d}, want {true sess:/g/gstack 1}", rows[0].collapsible, rows[0].key, rows[0].depth)
	}
	if rows[1].label != "feat" || rows[1].depth != 2 {
		t.Errorf("gstack child[0] = {label:%q depth:%d}, want {feat 2}", rows[1].label, rows[1].depth)
	}
	if rows[2].label != "feat" || rows[2].depth != 2 {
		t.Errorf("gstack child[1] = {label:%q depth:%d}, want {feat 2}", rows[2].label, rows[2].depth)
	}
	// kmux: single-session bare leaf (not collapsible, no header) at depth 1.
	if rows[3].collapsible || rows[3].label != "kmux" || rows[3].depth != 1 || rows[3].session != "/g/kmux‧CC" {
		t.Errorf("kmux leaf = %+v, want {collapsible:false label:kmux depth:1 session:/g/kmux‧CC}", rows[3])
	}
	// ungrouped orphan: single-session bare leaf, last.
	if rows[4].collapsible || rows[4].label != "orphan" || rows[4].depth != 1 {
		t.Errorf("orphan leaf = %+v, want {collapsible:false label:orphan depth:1}", rows[4])
	}
	if len(rows) != 5 {
		t.Fatalf("got %d rows, want 5: %+v", len(rows), rows)
	}

	// Worktree sessions carry their full session name (for actions) but hang
	// directly off the project at depth 2.
	for _, r := range rows {
		if r.session == "/g/gstack@feat‧CC" && r.depth != 2 {
			t.Errorf("worktree session depth = %d, want 2", r.depth)
		}
	}

	// Collapsing the gstack project hides its worktree sessions.
	rows = buildSessionRows(sessions, names, map[string]bool{"sess:/g/gstack": true}, attn, attached, detached, rowDeco{})
	for _, r := range rows {
		if r.session == "/g/gstack@feat‧CC" {
			t.Errorf("collapsed gstack should hide %q", r.session)
		}
	}
}
