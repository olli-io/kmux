package tui

import (
	"testing"

	"github.com/olli-io/kmux/internal/status"
)

// TestApplyAttnMarkers exercises the permission clearing guard: a hook-set
// permission marker must survive while the pane still shows the prompt (or capture
// failed), but be dropped — and the marker file removed — once the pane is back to a
// busy or idle non-prompt state (the Esc-dismissal fix). The waiting marker is
// applied unguarded, and a marker for an unlisted session is left alone.
func TestApplyAttnMarkers(t *testing.T) {
	const sess = "[kmux][CC]~/git/kmux"

	cases := []struct {
		name       string
		cur        status.AttentionState // pane-derived state for sess
		text       string                // captured pane text for sess
		marker     status.AttentionState // hook marker for sess
		wantState  status.AttentionState // expected states[sess] after overlay
		wantRemove bool                  // expected remove(sess) call
	}{
		{
			name:      "permission marker + prompt visible keeps permission",
			cur:       status.AttnWaiting,
			text:      "Do you want to proceed?\n ❯ 1. Yes\n   2. No",
			marker:    status.AttnPermission,
			wantState: status.AttnPermission,
		},
		{
			name:       "permission marker + idle pane clears (esc dismissal)",
			cur:        status.AttnWaiting,
			text:       "│ > Try \"fix the bug\"        │\n  ? for shortcuts",
			marker:     status.AttnPermission,
			wantState:  status.AttnWaiting,
			wantRemove: true,
		},
		{
			name:       "permission marker + busy pane clears",
			cur:        status.AttnBusy,
			text:       "  Cogitating… (esc to interrupt)",
			marker:     status.AttnPermission,
			wantState:  status.AttnBusy,
			wantRemove: true,
		},
		{
			name:      "permission marker + capture failed keeps permission",
			cur:       status.AttnUnknown,
			text:      "",
			marker:    status.AttnPermission,
			wantState: status.AttnPermission,
		},
		{
			name:      "waiting marker applied unguarded",
			cur:       status.AttnWaiting,
			text:      "│ > Try \"fix the bug\"        │\n  ? for shortcuts",
			marker:    status.AttnWaiting,
			wantState: status.AttnWaiting,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			states := map[string]status.AttentionState{sess: c.cur}
			texts := map[string]string{sess: c.text}
			markers := map[string]status.AttentionState{sess: c.marker}

			var removed []string
			applyAttnMarkers(states, texts, markers, func(s string) { removed = append(removed, s) })

			if states[sess] != c.wantState {
				t.Errorf("states[%q] = %d, want %d", sess, states[sess], c.wantState)
			}
			if got := len(removed) > 0; got != c.wantRemove {
				t.Errorf("remove called = %v (removed=%v), want %v", got, removed, c.wantRemove)
			}
		})
	}
}

// TestApplyAttnMarkersUnlistedSession: a marker for a session not in this poll's
// state map (e.g. killed/dead) is left to the TTL — never applied, never removed.
func TestApplyAttnMarkersUnlistedSession(t *testing.T) {
	states := map[string]status.AttentionState{}
	texts := map[string]string{}
	markers := map[string]status.AttentionState{"[kmux][CC]~/git/gone": status.AttnPermission}

	var removed []string
	applyAttnMarkers(states, texts, markers, func(s string) { removed = append(removed, s) })

	if len(states) != 0 {
		t.Errorf("states mutated for unlisted session: %v", states)
	}
	if len(removed) != 0 {
		t.Errorf("remove called for unlisted session: %v", removed)
	}
}
