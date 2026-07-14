package status

import "testing"

func TestClassifyAttention(t *testing.T) {
	cases := []struct {
		name string
		kind string
		pane string
		want AttentionState
	}{
		{"claude busy", "claude", "│ > write the code            │\n  Cogitating… (esc to interrupt)", AttnBusy},
		// Pane text no longer detects permission — a permission prompt reads as
		// waiting from pane text alone; the agent's hook supplies AttnPermission on
		// top (see LoadAttnMarkers). So the old permission prompt now classifies as
		// waiting here.
		{"claude permission prompt reads as waiting", "claude", "Do you want to proceed?\n ❯ 1. Yes\n   2. No", AttnWaiting},
		{"claude waiting", "claude", "│ > Try \"fix the bug\"        │\n  ? for shortcuts", AttnWaiting},
		{"opencode busy", "opencode", "⬝⬝⬝■■■■■  esc interrupt          tab agents  ctrl+p commands", AttnBusy},
		{"opencode permission reads as waiting", "opencode", "△ Permission required\n $ echo hi\n Allow once   Allow always   Reject", AttnWaiting},
		{"opencode waiting", "opencode", "Build · Big Pickle OpenCode Zen\n  8.4K (4%)  ctrl+p commands", AttnWaiting},
		{"unknown kind", "", "anything", AttnUnknown},
		{"non-agent kind", "vim", "esc to interrupt", AttnUnknown},
		{"empty pane waits", "claude", "", AttnWaiting},
		// Regression: the busy footer TRUNCATES at narrow pane widths to an ellipsis
		// ("esc to int…" / "esc int…"). The marker must still match, or the spinner
		// and busy state never register. These are the real captured narrow footers.
		{"claude busy truncated", "claude", "  ⏵⏵ auto mode on (shift+tab to cycle) · esc to int…", AttnBusy},
		{"claude idle not busy", "claude", "  ⏵⏵ accept edits on (shift+tab to cycle) · ← for a…", AttnWaiting},
		{"opencode busy truncated", "opencode", "⬝⬝⬝■■■■■  esc int…   tab agents", AttnBusy},
		// A marker that appears only in the scrollback transcript (above the live
		// status region) must not spoof the live state — classification looks only
		// at the bottom statusTailLines of the pane.
		{"claude transcript marker ignored", "claude", "we discussed esc to interrupt earlier\n\n\n\n\n\n\n\n\n\n\n\n\n│ > Try \"fix the bug\"        │\n  ? for shortcuts", AttnWaiting},
		// The real busy footer sits in that bottom region, so it still classifies
		// as busy even with unrelated transcript above.
		{"claude busy in tail region", "claude", "some earlier transcript line\n\n\n\n\n✻ Cogitating…\n────\n❯ \n────\n  ⏵⏵ auto mode on · esc to interrupt · ← for agents", AttnBusy},
	}
	for _, c := range cases {
		if got := ClassifyAttention(c.kind, c.pane); got != c.want {
			t.Errorf("%s: ClassifyAttention(%q, …) = %d, want %d", c.name, c.kind, got, c.want)
		}
	}
}

// TestPromptVisible covers the clearing-guard signature: PromptVisible must be true
// only while a permission prompt is actually on screen, so the dashboard poll keeps
// a hook-set permission marker then and drops it once the prompt is gone.
func TestPromptVisible(t *testing.T) {
	cases := []struct {
		name string
		kind string
		pane string
		want bool
	}{
		{"claude prompt visible", "claude", "Do you want to proceed?\n ❯ 1. Yes\n   2. No", true},
		{"claude idle box", "claude", "│ > Try \"fix the bug\"        │\n  ? for shortcuts", false},
		// The busy footer has no permission substring — a resumed generation is not a
		// visible prompt, so the guard clears the marker (busy owns the state anyway).
		{"claude busy footer", "claude", "│ > write the code            │\n  Cogitating… (esc to interrupt)", false},
		// A prompt substring sitting only in the scrollback transcript (above the live
		// bottom region) must not read as a visible prompt — matching is tail-confined.
		{"claude prompt in transcript ignored", "claude", "do you want to proceed earlier\n\n\n\n\n\n\n\n\n\n\n\n\n│ > Try \"fix the bug\"        │\n  ? for shortcuts", false},
		{"opencode prompt visible", "opencode", "△ Permission required\n $ echo hi\n Allow once   Allow always   Reject", true},
		{"opencode idle", "opencode", "Build · Big Pickle OpenCode Zen\n  8.4K (4%)  ctrl+p commands", false},
		{"unknown kind", "", "Do you want to proceed?", false},
	}
	for _, c := range cases {
		if got := PromptVisible(c.kind, c.pane); got != c.want {
			t.Errorf("%s: PromptVisible(%q, …) = %v, want %v", c.name, c.kind, got, c.want)
		}
	}
}
