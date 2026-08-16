package status

import "github.com/olli-io/kmux/internal/status/detect"

// AttentionState is what an agent session is doing, derived entirely from its pane
// text by the herdr-style detect engine (see internal/status/detect).
type AttentionState int

const (
	AttnUnknown    AttentionState = iota // capture failed or unrecognized agent kind
	AttnBusy                             // actively generating
	AttnPermission                       // blocked on a permission/confirmation prompt
	AttnWaiting                          // your turn: idle / finished / awaiting input
)

// Attention is detected from pane text alone: each agent (claude, opencode) ships an
// embedded manifest of prioritized rules (detect/claude.yaml, detect/opencode.yaml)
// matched against the pane's bottom region. This replaced the earlier hook/marker
// pathway (an agent's own lifecycle hooks writing marker files), which could not tell a
// turn that ended with a pending question from one that merely finished — Claude fires
// the same Stop event for both. Pane text can: a live permission/question box has a
// recognizable footer, so it reads as AttnPermission and self-clears to AttnWaiting the
// moment the box leaves the screen.
//
// Detection is confined to the pane's bottom region so scrollback echoing a phrase can't
// spoof the live state, and busy is keyed off the spinner-timer line (e.g.
// "✽ Processing… (5m · …)") rather than the "esc to interrupt" footer hint, which
// truncates off at normal pane widths. See the manifests for the rule lists.

// ClassifyAttention maps an agent kind (from AgentKind) and its captured pane text to an
// attention state via the detect engine's highest-priority matching rule:
//
//	detect working => AttnBusy       (a live spinner / "esc to interrupt" footer)
//	detect blocked => AttnPermission (a live permission / confirmation / question box)
//	detect idle or no rule matched => AttnWaiting (finished / plain prompt / nothing pending)
//
// An unrecognized or empty kind (a non-agent session) is AttnUnknown. The caller treats
// AttnUnknown from a capture FAILURE the same way — skip the session this poll, never
// kill it — so a flaky capture never flips a live state.
func ClassifyAttention(kind, paneText string) AttentionState {
	state, ok := detect.Classify(kind, paneText)
	if !ok {
		return AttnUnknown
	}
	switch state {
	case detect.StateWorking:
		return AttnBusy
	case detect.StateBlocked:
		return AttnPermission
	default: // detect.StateIdle, detect.StateUnknown (no rule matched)
		return AttnWaiting
	}
}
