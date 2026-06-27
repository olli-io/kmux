package tmux

import "testing"

func TestAgentKind(t *testing.T) {
	cases := map[string]string{
		"proj/wt~cl": "claude",
		"proj/wt~oc": "opencode",
		"scratch":    "",
		"foo~clx":    "",
	}
	for name, want := range cases {
		if got := AgentKind(name); got != want {
			t.Errorf("AgentKind(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestAgentSessionRegex(t *testing.T) {
	match := []string{"a~cl", "a/b~oc", "x/y/z~cl"}
	noMatch := []string{"scratch", "cl", "~clx", "oc_thing"}
	for _, s := range match {
		if !agentSession.MatchString(s) {
			t.Errorf("expected %q to match", s)
		}
	}
	for _, s := range noMatch {
		if agentSession.MatchString(s) {
			t.Errorf("expected %q NOT to match", s)
		}
	}
}
