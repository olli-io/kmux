package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// agentSession matches the kmux agent prefix — [kmux] followed by an agent kind's
// marker ([CC] claude, [OC] opencode, [PI] pi) — case-insensitively, allowing an
// optional leading "[!!]" attention marker that a blocked session carries in its live
// name. The alternation is duplicated from agent.kinds (the agent package imports
// tmux, so tmux can't import it back) and must stay in sync; agent's naming tests
// guard the pairing.
var agentSession = regexp.MustCompile(`(?i)^(?:\[!!\])?\[kmux\]\[(cc|oc|pi)\]`)

// IsAgentSession reports whether a tmux session name is a kmux-managed agent session,
// i.e. carries a known agent marker. It is the grammar's one gate: adopt uses it to
// decide which old sessions the dashboard takes over, and TestAgentMarkersAreListed pins it
// against agent.Kinds so a kind added to the registry can't be silently unlisted.
func IsAgentSession(name string) bool { return agentSession.MatchString(name) }

// attnMarkStr is the leading marker a blocked session's live tmux name carries. It is
// duplicated from agent.attnMark (the agent package imports tmux, so tmux can't import
// it back) and must stay in sync.
const attnMarkStr = "[!!]"

// stripAttnMark removes a leading "[!!]" so a live session name collapses to its
// canonical identity. Idempotent on an already-canonical name.
func stripAttnMark(name string) string { return strings.TrimPrefix(name, attnMarkStr) }

// CurrentSession returns the agent name (or, for a pane that is no agent, the
// session name) and the active-pane directory of the tmux pane the caller runs inside, for code invoked from within a session (e.g. a tmux keybinding).
// It errors when not run inside tmux ($TMUX unset).
func CurrentSession() (name, paneDir string, err error) {
	if os.Getenv("TMUX") == "" {
		return "", "", fmt.Errorf("not inside a tmux session ($TMUX unset)")
	}
	out, err := exec.Command("tmux", "display-message", "-p", "#{?@kmux,#{@kmux},#{session_name}}\t#{pane_current_path}").Output()
	if err != nil {
		return "", "", fmt.Errorf("tmux display-message: %w", err)
	}
	fields := strings.SplitN(strings.TrimRight(string(out), "\n"), "\t", 2)
	name = fields[0]
	if len(fields) > 1 {
		paneDir = fields[1]
	}
	return name, paneDir, nil
}

// captureSentinel delimits the per-session sections of a batched CapturePanes call.
// Its record-separator bytes never appear in visible pane text, so a boundary can't
// collide with captured content.
const captureSentinel = "\x1e\x1eKMUXCAP\x1e\x1e"

// parseCapturePanes splits batched output on the sentinel line into per-session
// text. A count that no longer matches the input (a truncated chain, or the sentinel
// appearing in captured text) is an error so the caller falls back to per-session.
func parseCapturePanes(out string, sessions []string) (map[string]string, error) {
	parts := strings.Split(out, captureSentinel+"\n")
	if len(parts) != len(sessions)+1 {
		return nil, fmt.Errorf("tmux batch capture: %d sections for %d sessions", len(parts)-1, len(sessions))
	}
	texts := make(map[string]string, len(sessions))
	for i, s := range sessions {
		texts[s] = parts[i+1]
	}
	return texts, nil
}

// RenameSession renames the session of the target to newName. It is the primitive
// behind the "[!!]" attention marker on a parked agent session. A missing session
// is not an error.
func RenameSession(target, to string) error {
	if _, err := run("rename-session", "-t", target, to); err != nil && !strings.Contains(err.Error(), "can't find") {
		return err
	}
	return nil
}
