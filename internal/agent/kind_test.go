package agent

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
