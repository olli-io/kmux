// Package tmux_test holds the cross-package guard for the agent-marker grammar.
// It lives in the external test package because internal/agent imports internal/tmux:
// a same-package test could not import agent back, but a _test package can.
package tmux_test

import (
	"testing"

	"github.com/olli-io/kmux/internal/agent"
	"github.com/olli-io/kmux/internal/tmux"
)

// TestAgentMarkersAreListed pins tmux's session-name regex to the agent registry. The
// regex spells its marker alternation out (tmux can't import agent), so a kind added
// to agent.Kinds without touching the regex would produce sessions the dashboard never
// lists — a silent, hard-to-spot failure this test turns into a build failure.
func TestAgentMarkersAreListed(t *testing.T) {
	for _, k := range agent.Kinds() {
		name := agent.SessionForKind(agent.ExpectedSession("/g/proj", ""), k.Name)
		if !tmux.IsAgentSession(name) {
			t.Errorf("kind %q: session %q not recognized; add %q to tmux.agentSession", k.Name, name, k.Marker)
		}
		// A blocked session's live name carries the "[!!]" prefix and must still match.
		if live := agent.AttnSession(name, true); !tmux.IsAgentSession(live) {
			t.Errorf("kind %q: attention-marked session %q not recognized", k.Name, live)
		}
	}
}

// TestNonAgentSessionsRejected guards the other direction: the regex must not adopt
// sessions kmux does not manage.
func TestNonAgentSessionsRejected(t *testing.T) {
	for _, name := range []string{"", "work", "[kmux][dashboard]", "[kmux][dash]~/git/x", "[kmux][zz]~/git/x", "x[kmux][CC]~/git/x"} {
		if tmux.IsAgentSession(name) {
			t.Errorf("session %q wrongly recognized as an agent session", name)
		}
	}
}
