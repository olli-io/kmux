package status

import "strings"

// AttentionState is what an agent session is doing, derived from its pane text.
type AttentionState int

const (
	AttnUnknown    AttentionState = iota // capture failed or unrecognized agent kind
	AttnBusy                             // actively generating
	AttnPermission                       // blocked on a permission/confirmation prompt
	AttnWaiting                          // your turn: idle / finished / awaiting input
)

// attentionMarkers classifies an agent's pane into busy vs not-busy from its
// bottom-region text. Only the BUSY state is derived from pane text now: the
// permission and waiting states come from the agent's own lifecycle hooks/plugins,
// written to the attention marker dir and folded in by the dashboard poll (see
// LoadAttnMarkers and tui.attentionCmd). Busy stays pane-text-based because neither
// agent emits a fast, reliable "generating" hook, and the spinner needs sub-second
// freshness that only the poll gives.
//
// The markers must survive footer TRUNCATION at narrow pane widths: tmux
// capture-pane records the footer exactly as drawn, and a narrow pane cuts the hint
// bar with an ellipsis. Empirically claude's busy footer reads
// "⏵⏵ auto mode on (shift+tab to cycle) · esc to int…" (truncated from "esc to
// interrupt"), while its idle footer reads "· ← for a…" — so the truncation-robust
// prefix "esc to int" is present when busy and absent when idle. OpenCode's busy
// hint is "esc interrupt", which truncates similarly; "esc int" is the robust
// prefix. Matching is lowercased substring, and confined to the pane's bottom
// region (see statusTailLines) so scrollback echoing the phrase can't spoof it.
//
// `perm` is a permission-prompt signature. It is NOT used to DETECT permission
// prompts — that is hook-driven (see LoadAttnMarkers), because a prompt that renders
// oddly or scrolls would be missed and the alert dropped. It is used ONLY as a
// CLEARING GUARD (see PromptVisible): a hook-set permission marker is trusted until
// the pane positively shows the prompt is GONE, so a missed match here can only
// DELAY a clear, never drop an alert. Do NOT promote it back into ClassifyAttention
// — pane-text permission detection was deliberately removed in favor of hooks. The
// substrings are the ones the removed detector proved detectable (git 350d7f3).
var attentionMarkers = map[string]struct {
	busy []string
	perm []string
}{
	"claude":   {busy: []string{"esc to int"}, perm: []string{"do you want", "❯ 1. yes", "1. yes"}},
	"opencode": {busy: []string{"esc int", "esc interrupt"}, perm: []string{"permission required", "allow once", "allow always"}},
}

// statusTailLines bounds marker matching to the bottom of the pane — the live
// status/prompt region. Without it a marker sitting higher in the scrollback
// transcript would be misread as the live state.
const statusTailLines = 6

// paneTail returns the last n lines, dropping trailing blank lines first so the
// window anchors on the agent's real bottom content, not empty padding.
func paneTail(text string, n int) string {
	lines := strings.Split(text, "\n")
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	start := end - n
	if start < 0 {
		start = 0
	}
	return strings.Join(lines[start:end], "\n")
}

// ClassifyAttention maps an agent kind (from AgentKind) and its captured pane text
// to an attention state from pane text alone: AttnBusy when a busy marker is
// present, else AttnWaiting. Permission (and a hook-confirmed waiting) are layered
// on top of this by the poll from the agent's lifecycle markers (see
// LoadAttnMarkers) — pane text is only the busy/idle discriminator now. An
// unrecognized or empty kind (a non-agent session) is AttnUnknown. Only the bottom
// of the pane is examined (see statusTailLines) so transcript text never spoofs the
// live status.
func ClassifyAttention(kind, paneText string) AttentionState {
	mk, ok := attentionMarkers[kind]
	if !ok {
		return AttnUnknown
	}
	t := strings.ToLower(paneTail(paneText, statusTailLines))
	if containsAny(t, mk.busy) {
		return AttnBusy
	}
	return AttnWaiting
}

// PromptVisible reports whether kind's captured pane still shows a permission
// prompt in its live bottom region (see statusTailLines). It is the CLEARING GUARD
// for hook-set permission markers: when a Claude prompt is dismissed with Esc no
// hook fires (Claude emits no dismissal event), so the permission marker would
// otherwise strand the ! glyph / [!!] tab flag until the 30-min TTL sweep. The
// dashboard poll keeps a permission marker only while this returns true, and drops+
// removes it once the pane is positively back to a non-prompt state (see
// tui.applyAttnMarkers).
//
// It is a guard, not a detector: a false negative here only DELAYS a clear by a poll
// or two, it never drops an alert (the alert came from the hook). So an
// unrecognized/empty kind returns false. Capture FAILURE is not this function's
// concern — the caller detects it (AttnUnknown) and keeps the marker rather than
// asking here, so an unreadable pane never clears a live prompt.
func PromptVisible(kind, paneText string) bool {
	mk, ok := attentionMarkers[kind]
	if !ok {
		return false
	}
	t := strings.ToLower(paneTail(paneText, statusTailLines))
	return containsAny(t, mk.perm)
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
