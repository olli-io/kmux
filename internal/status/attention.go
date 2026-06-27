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

// attentionMarkers is the single, tunable source of truth for classifying an
// agent's pane. For each agent kind, any busy marker present means the agent is
// generating; otherwise any permission marker present means it is blocked on a
// prompt; otherwise it is waiting on the user.
//
// These strings depend on the agent TUI's wording, version, terminal width (which
// can wrap or truncate the footer), and the box-drawing/ANSI characters tmux
// capture-pane leaves around them — hence lowercased substring matching, not exact
// lines. The OpenCode markers are verified against opencode 1.17.5: while
// generating, the footer reads "esc interrupt" (note: not claude's "esc to
// interrupt"); a permission prompt shows "△ Permission required" with
// "Allow once" / "Allow always" / "Reject" choices.
//
// claude's bottom hint bar shows "· esc to interrupt ·" only while a turn is
// interruptible (i.e. busy); idle it reads just "auto mode on … · ← for agents".
// So the bare phrase is a reliable busy signal — the only false positive is an
// agent's own transcript echoing it, which ClassifyAttention rules out by matching
// only the pane's bottom region (the live chrome), not the scrollback above.
var attentionMarkers = map[string]struct{ busy, permission []string }{
	"claude": {
		busy:       []string{"esc to interrupt"},
		permission: []string{"do you want to proceed", "❯ 1. yes", "1. yes"},
	},
	"opencode": {
		busy:       []string{"esc interrupt"},
		permission: []string{"permission required", "allow once", "allow always"},
	},
}

// statusTailLines bounds marker matching to the bottom of the captured pane —
// the agent's live status/prompt region (spinner + input box + hint bar, ~6 lines;
// a permission prompt occupies the same region). Without it, a marker substring
// sitting higher up in the scrollback transcript (e.g. the agent or user merely
// discussing "esc to interrupt") would be misread as the live state.
const statusTailLines = 6

// paneTail returns the last n non-blank-anchored lines of text: trailing blank
// lines are dropped first so the window anchors on the agent's real bottom
// content (status footer / prompt box) rather than empty padding rows.
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
// to an attention state. It is pure and order-sensitive: a busy marker wins over a
// permission marker, which wins over the waiting default. An unrecognized kind (or
// empty kind, e.g. a non-agent session) is AttnUnknown. Only the bottom of the
// pane is examined (see statusTailLines) so transcript text never spoofs the live
// status.
func ClassifyAttention(kind, paneText string) AttentionState {
	mk, ok := attentionMarkers[kind]
	if !ok {
		return AttnUnknown
	}
	t := strings.ToLower(paneTail(paneText, statusTailLines))
	switch {
	case containsAny(t, mk.busy):
		return AttnBusy
	case containsAny(t, mk.permission):
		return AttnPermission
	default:
		return AttnWaiting
	}
}

// containsAny reports whether s contains any of subs.
func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
