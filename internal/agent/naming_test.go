package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// ccPre / ocPre are the claude / opencode agent prefixes spelled out for test
// literals (the [kmux] tag plus the bracketed kind marker).
const (
	ccPre = "[kmux][CC]"
	ocPre = "[kmux][OC]"
)

func homeOrSkip(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	return home
}

func TestExpectedSession(t *testing.T) {
	home := homeOrSkip(t)
	kmux := filepath.Join(home, "git", "kmux")
	if got := ExpectedSession(kmux, ""); got != ccPre+"~/git/kmux" {
		t.Errorf("main session = %q, want %q", got, ccPre+"~/git/kmux")
	}
	if got := ExpectedSession(kmux, "feat"); got != ccPre+"~/git/kmux@feat" {
		t.Errorf("worktree session = %q, want %q", got, ccPre+"~/git/kmux@feat")
	}
	// A '.' in the path is tmux-sanitized to '_'.
	dotted := filepath.Join(home, "git", "my.proj")
	if got := ExpectedSession(dotted, ""); got != ccPre+"~/git/my_proj" {
		t.Errorf("dotted session = %q, want %q", got, ccPre+"~/git/my_proj")
	}
	// Paths outside $HOME keep their absolute form.
	if got := ExpectedSession("/opt/x", ""); got != ccPre+"/opt/x" {
		t.Errorf("non-home session = %q, want %q", got, ccPre+"/opt/x")
	}
}

func TestSessionForKind(t *testing.T) {
	if got := SessionForKind(ccPre+"~/git/kmux", "claude"); got != ccPre+"~/git/kmux" {
		t.Errorf("claude = %q, want %q", got, ccPre+"~/git/kmux")
	}
	if got := SessionForKind(ccPre+"~/git/kmux", "opencode"); got != ocPre+"~/git/kmux" {
		t.Errorf("opencode = %q, want %q", got, ocPre+"~/git/kmux")
	}
	if got := SessionForKind(ccPre+"~/git/kmux@feat", "opencode"); got != ocPre+"~/git/kmux@feat" {
		t.Errorf("worktree opencode = %q, want %q", got, ocPre+"~/git/kmux@feat")
	}
}

func TestMatchProject(t *testing.T) {
	home := homeOrSkip(t)
	kmux := filepath.Join(home, "git", "kmux")
	gstack := filepath.Join(home, "git", "gstack")
	gstackExtra := filepath.Join(home, "git", "gstack_extra")
	dotted := filepath.Join(home, "git", "my.proj")
	paths := []string{kmux, gstack, gstackExtra, dotted}

	cases := []struct {
		session  string
		proj, wt string
		ok       bool
	}{
		{ExpectedSession(kmux, ""), kmux, "", true},                               // main worktree
		{ExpectedSession(gstack, "feature"), gstack, "feature", true},             // project + worktree
		{ExpectedSession(gstackExtra, ""), gstackExtra, "", true},                 // exact, longest prefix wins over gstack
		{ExpectedSession(gstackExtra, "wt"), gstackExtra, "wt", true},             // longest prefix + worktree
		{ExpectedSession(dotted, ""), dotted, "", true},                           // '.'-in-path resolves to the real path
		{SessionForKind(ExpectedSession(kmux, "x"), "opencode"), kmux, "x", true}, // opencode suffix
		{ccPre + "~/git/unknown", "", "", false},                                  // no match
	}
	for _, c := range cases {
		proj, wt, ok := MatchProject(c.session, paths)
		if proj != c.proj || wt != c.wt || ok != c.ok {
			t.Errorf("MatchProject(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.session, proj, wt, ok, c.proj, c.wt, c.ok)
		}
	}
}

func TestExtractors(t *testing.T) {
	home := homeOrSkip(t)
	kmux := filepath.Join(home, "git", "kmux")

	main := ExpectedSession(kmux, "")
	if got := ProjectPath(main); got != kmux {
		t.Errorf("ProjectPath(main) = %q, want %q", got, kmux)
	}
	if got := ProjectName(main); got != "kmux" {
		t.Errorf("ProjectName(main) = %q, want %q", got, "kmux")
	}
	if got := WorktreeName(main); got != "" {
		t.Errorf("WorktreeName(main) = %q, want %q", got, "")
	}
	if got := AgentKind(main); got != "claude" {
		t.Errorf("AgentKind(main) = %q, want %q", got, "claude")
	}

	wt := ExpectedSession(kmux, "feat")
	if got := ProjectPath(wt); got != kmux {
		t.Errorf("ProjectPath(wt) = %q, want %q", got, kmux)
	}
	if got := WorktreeName(wt); got != "feat" {
		t.Errorf("WorktreeName(wt) = %q, want %q", got, "feat")
	}
	if got := AgentKind(SessionForKind(wt, "opencode")); got != "opencode" {
		t.Errorf("AgentKind(opencode) = %q, want %q", got, "opencode")
	}
}

func TestOrphanSession(t *testing.T) {
	home := homeOrSkip(t)
	dir := filepath.Join(home, "scratch", "notes")

	name := OrphanSession(dir)
	if want := ccPre + "[∅]~/scratch/notes"; name != want {
		t.Errorf("OrphanSession = %q, want %q", name, want)
	}
	if !IsOrphan(name) {
		t.Errorf("IsOrphan(%q) = false, want true", name)
	}
	// A normal (repo) session is not an orphan.
	if IsOrphan(ExpectedSession(dir, "")) {
		t.Errorf("IsOrphan(repo session) = true, want false")
	}
	// The marker leaves agent-kind detection and suffix swapping intact.
	if got := AgentKind(name); got != "claude" {
		t.Errorf("AgentKind = %q, want claude", got)
	}
	if got := SessionForKind(name, "opencode"); got != ocPre+"[∅]~/scratch/notes" {
		t.Errorf("SessionForKind = %q, want %q", got, ocPre+"[∅]~/scratch/notes")
	}
	// The path round-trips; no worktree; never binds to a project.
	if got := ProjectPath(name); got != dir {
		t.Errorf("ProjectPath = %q, want %q", got, dir)
	}
	if got := ProjectName(name); got != "notes" {
		t.Errorf("ProjectName = %q, want notes", got)
	}
	if got := WorktreeName(name); got != "" {
		t.Errorf("WorktreeName = %q, want \"\"", got)
	}
	if _, _, ok := MatchProject(name, []string{dir}); ok {
		t.Errorf("MatchProject(%q) ok = true, want false", name)
	}

	// An orphan directory containing '@' must not be parsed as a worktree.
	at := filepath.Join(home, "tmp", "foo@bar")
	atName := OrphanSession(at)
	if got := ProjectPath(atName); got != at {
		t.Errorf("ProjectPath(@-path) = %q, want %q", got, at)
	}
	if got := WorktreeName(atName); got != "" {
		t.Errorf("WorktreeName(@-path) = %q, want \"\"", got)
	}
}

func TestAgentKind(t *testing.T) {
	cases := map[string]string{
		ccPre + "~/git/proj@wt":   "claude",
		ocPre + "~/git/proj@wt":   "opencode",
		"[kmux][oc]~/git/proj@wt": "opencode", // case-insensitive
		"scratch":                 "",
		"~/git/proj[kmux][CC]":    "", // marker must be a prefix, not mid-name
	}
	for name, want := range cases {
		if got := AgentKind(name); got != want {
			t.Errorf("AgentKind(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestAbbrevExpandHome(t *testing.T) {
	home := homeOrSkip(t)
	p := filepath.Join(home, "git", "x")
	if got := abbrevHome(p); got != "~/git/x" {
		t.Errorf("abbrevHome(%q) = %q, want %q", p, got, "~/git/x")
	}
	if got := expandHome("~/git/x"); got != p {
		t.Errorf("expandHome = %q, want %q", got, p)
	}
	if got := abbrevHome("/opt/x"); got != "/opt/x" {
		t.Errorf("abbrevHome(/opt/x) = %q, want %q", got, "/opt/x")
	}
	if got := abbrevHome(home); got != "~" {
		t.Errorf("abbrevHome(home) = %q, want %q", got, "~")
	}
}

func TestAgentCommand(t *testing.T) {
	if got := AgentCommand("claude"); got != "claude --continue" {
		t.Errorf("claude cmd = %q, want %q", got, "claude --continue")
	}
	if got := AgentCommand("opencode"); got != "opencode --continue" {
		t.Errorf("opencode cmd = %q, want %q", got, "opencode --continue")
	}
}
