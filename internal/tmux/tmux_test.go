package tmux

import "testing"

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
