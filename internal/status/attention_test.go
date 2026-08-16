package status

import "testing"

// TestClassifyAttention pins the full pane-text → AttentionState mapping now that
// detection is hook-free: busy (working), permission/question box (blocked), and
// finished/idle (waiting) are all derived from the pane alone.
func TestClassifyAttention(t *testing.T) {
	cases := []struct {
		name string
		kind string
		pane string
		want AttentionState
	}{
		// Busy: the spinner-timer line is width-robust; the esc-interrupt footer is a
		// fallback that truncates off at narrow widths but still matches when present.
		{"claude busy spinner", "claude", "  ✽ Processing… (5m 44s · ↓ 15.4k tokens)", AttnBusy},
		{"claude busy esc hint", "claude", "│ > write the code            │\n  Cogitating… (esc to interrupt)", AttnBusy},
		{"claude busy truncated", "claude", "  ⏵⏵ auto mode on (shift+tab to cycle) · esc to int…", AttnBusy},

		// Permission / question box → AttnPermission (previously these read as waiting
		// because detection was hook-driven; pane text now catches them directly).
		{"claude bash permission", "claude", "Do you want to proceed?\n ❯ 1. Yes\n   2. No", AttnPermission},
		{"claude question box", "claude", "● How is Claude doing?\n  1: Bad  2: Fine\n────\n  esc to cancel   enter to confirm", AttnPermission},
		{"claude question box select", "claude", "● Pick a color\n  red\n  blue\n────\n  esc to cancel   enter to select", AttnPermission},

		// Finished / idle → AttnWaiting.
		{"claude idle prompt", "claude", "│ > Try \"fix the bug\"        │\n  ? for shortcuts", AttnWaiting},
		{"claude idle accept-edits footer", "claude", "✻ Cooked for 1m 53s\n❯ next thing\n  ⏵⏵ accept edits on (shift+tab to cycle) · ← for a…", AttnWaiting},
		{"empty pane waits", "claude", "", AttnWaiting},

		// Non-agent / unknown kind.
		{"unknown kind", "", "anything", AttnUnknown},
		{"non-agent kind", "vim", "esc to interrupt", AttnUnknown},

		// opencode analogues.
		{"opencode busy", "opencode", "⬝⬝⬝■■■■■  esc interrupt          tab agents  ctrl+p commands", AttnBusy},
		{"opencode busy truncated", "opencode", "⬝⬝⬝■■■■■  esc int…   tab agents", AttnBusy},
		{"opencode permission", "opencode", "△ Permission required\n $ echo hi\n Allow once   Allow always   Reject", AttnPermission},
		{"opencode waiting", "opencode", "Build · Big Pickle OpenCode Zen\n  8.4K (4%)  ctrl+p commands", AttnWaiting},

		// Tail confinement: a marker only in scrollback (above the live bottom region)
		// must not spoof the live state — classification looks only at the bottom lines.
		{"claude busy in scrollback ignored", "claude", "we discussed esc to interrupt earlier\n\n\n\n\n\n\n\n\n\n\n\n\n│ > Try \"fix the bug\"        │\n  ? for shortcuts", AttnWaiting},
		{"claude permission in scrollback ignored", "claude", "do you want to proceed earlier\n\n\n\n\n\n\n\n\n\n\n\n\n│ > Try \"fix the bug\"        │\n  ? for shortcuts", AttnWaiting},
		// A real busy footer in the bottom region still classifies as busy despite
		// unrelated transcript above.
		{"claude busy in tail region", "claude", "some earlier transcript line\n\n\n\n\n✻ Cogitating…\n────\n❯ \n────\n  ⏵⏵ auto mode on · esc to interrupt · ← for agents", AttnBusy},
	}
	for _, c := range cases {
		if got := ClassifyAttention(c.kind, c.pane); got != c.want {
			t.Errorf("%s: ClassifyAttention(%q, …) = %d, want %d", c.name, c.kind, got, c.want)
		}
	}
}
