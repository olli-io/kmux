package tmux

import "testing"

func TestAgentSessionRegex(t *testing.T) {
	match := []string{"~/git/a‧CC", "~/git/a@b‧OC", "/x/y/z‧cc"}
	noMatch := []string{"scratch", "CC", "~/git/a‧CCx", "oc_thing", "a~cl", "a~oc"}
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
