package agent

import "testing"

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
		proj, wt, ok := MatchProject(c.rem, names)
		if proj != c.proj || wt != c.wt || ok != c.ok {
			t.Errorf("MatchProject(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.rem, proj, wt, ok, c.proj, c.wt, c.ok)
		}
	}
}

func TestExpectedSession(t *testing.T) {
	if got := ExpectedSession("kmux", ""); got != "kmux~cl" {
		t.Errorf("main session = %q, want %q", got, "kmux~cl")
	}
	if got := ExpectedSession("kmux", "feat"); got != "kmux/feat~cl" {
		t.Errorf("worktree session = %q, want %q", got, "kmux/feat~cl")
	}
}

func TestSessionForKind(t *testing.T) {
	if got := SessionForKind("kmux~cl", "claude"); got != "kmux~cl" {
		t.Errorf("claude = %q, want %q", got, "kmux~cl")
	}
	if got := SessionForKind("kmux~cl", "opencode"); got != "kmux~oc" {
		t.Errorf("opencode = %q, want %q", got, "kmux~oc")
	}
	if got := SessionForKind("kmux/feat~cl", "opencode"); got != "kmux/feat~oc" {
		t.Errorf("worktree opencode = %q, want %q", got, "kmux/feat~oc")
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
