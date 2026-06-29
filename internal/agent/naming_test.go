package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// cc is the claude agent suffix (‧CC, ‧ = U+2027) spelled out for test literals.
const cc = "‧CC"

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
	if got := ExpectedSession(kmux, ""); got != "~/git/kmux"+cc {
		t.Errorf("main session = %q, want %q", got, "~/git/kmux"+cc)
	}
	if got := ExpectedSession(kmux, "feat"); got != "~/git/kmux@feat"+cc {
		t.Errorf("worktree session = %q, want %q", got, "~/git/kmux@feat"+cc)
	}
	// A '.' in the path is tmux-sanitized to '_'.
	dotted := filepath.Join(home, "git", "my.proj")
	if got := ExpectedSession(dotted, ""); got != "~/git/my_proj"+cc {
		t.Errorf("dotted session = %q, want %q", got, "~/git/my_proj"+cc)
	}
	// Paths outside $HOME keep their absolute form.
	if got := ExpectedSession("/opt/x", ""); got != "/opt/x"+cc {
		t.Errorf("non-home session = %q, want %q", got, "/opt/x"+cc)
	}
}

func TestSessionForKind(t *testing.T) {
	if got := SessionForKind("~/git/kmux"+cc, "claude"); got != "~/git/kmux"+cc {
		t.Errorf("claude = %q, want %q", got, "~/git/kmux"+cc)
	}
	if got := SessionForKind("~/git/kmux"+cc, "opencode"); got != "~/git/kmux‧OC" {
		t.Errorf("opencode = %q, want %q", got, "~/git/kmux‧OC")
	}
	if got := SessionForKind("~/git/kmux@feat"+cc, "opencode"); got != "~/git/kmux@feat‧OC" {
		t.Errorf("worktree opencode = %q, want %q", got, "~/git/kmux@feat‧OC")
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
		{"~/git/unknown" + cc, "", "", false},                                     // no match
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

func TestAgentKind(t *testing.T) {
	cases := map[string]string{
		"~/git/proj@wt" + cc:    "claude",
		"~/git/proj@wt‧OC":      "opencode",
		"~/git/proj@wt‧oc":      "opencode", // case-insensitive
		"scratch":               "",
		"~/git/proj" + cc + "x": "",
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
