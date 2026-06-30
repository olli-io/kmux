package tmux

import "testing"

func TestAgentSessionRegex(t *testing.T) {
	match := []string{"[kmux][CC]~/git/a", "[kmux][OC]~/git/a@b", "[kmux][cc]/x/y/z"}
	noMatch := []string{"scratch", "CC", "kmux[CC]~/git/a", "~/git/a[kmux][CC]", "oc_thing"}
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
