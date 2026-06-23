package main

import "testing"

func TestClassifyAttention(t *testing.T) {
	cases := []struct {
		name string
		kind string
		pane string
		want attentionState
	}{
		{"claude busy", "claude", "│ > write the code            │\n  Cogitating… (esc to interrupt)", attnBusy},
		{"claude permission", "claude", "Do you want to proceed?\n ❯ 1. Yes\n   2. No", attnPermission},
		{"claude waiting", "claude", "│ > Try \"fix the bug\"        │\n  ? for shortcuts", attnWaiting},
		{"claude busy beats permission", "claude", "1. Yes\n(esc to interrupt)", attnBusy},
		{"opencode busy", "opencode", "⬝⬝⬝■■■■■  esc interrupt          tab agents  ctrl+p commands", attnBusy},
		{"opencode permission", "opencode", "△ Permission required\n $ echo hi\n Allow once   Allow always   Reject", attnPermission},
		{"opencode waiting", "opencode", "Build · Big Pickle OpenCode Zen\n  8.4K (4%)  ctrl+p commands", attnWaiting},
		{"unknown kind", "", "anything", attnUnknown},
		{"non-agent kind", "vim", "esc to interrupt", attnUnknown},
		{"empty pane waits", "claude", "", attnWaiting},
		// A marker that appears only in the scrollback transcript (above the live
		// status region) must not spoof the live state — classification looks only
		// at the bottom statusTailLines of the pane.
		{"claude transcript marker ignored", "claude", "we discussed esc to interrupt earlier\n\n\n\n\n\n\n\n\n\n\n\n\n│ > Try \"fix the bug\"        │\n  ? for shortcuts", attnWaiting},
		// The real busy footer sits in that bottom region, so it still classifies
		// as busy even with unrelated transcript above.
		{"claude busy in tail region", "claude", "some earlier transcript line\n\n\n\n\n✻ Cogitating…\n────\n❯ \n────\n  ⏵⏵ auto mode on · esc to interrupt · ← for agents", attnBusy},
	}
	for _, c := range cases {
		if got := classifyAttention(c.kind, c.pane); got != c.want {
			t.Errorf("%s: classifyAttention(%q, …) = %d, want %d", c.name, c.kind, got, c.want)
		}
	}
}
