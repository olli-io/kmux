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

// attentionMarkers is the tunable source of truth for classifying a pane: any busy
// marker means generating; else any permission marker means blocked on a prompt;
// else waiting. Matching is lowercased substring, not exact lines, because the
// wording depends on the agent TUI's version, terminal width, and the box-drawing
// characters capture-pane leaves around them.
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

// ClassifyAttention maps an agent kind and pane text to a state, order-sensitive:
// busy wins over permission wins over the waiting default. An unrecognized kind is
// AttnUnknown. Only the pane bottom is examined (see statusTailLines) so transcript
// text never spoofs the live status.
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

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
