package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// agentSession matches tmux session names beginning with the kmux agent prefix
// [kmux][CC] (claude) or [kmux][OC] (opencode), case-insensitively.
var agentSession = regexp.MustCompile(`(?i)^\[kmux\]\[(cc|oc)\]`)

// ListAgentSessions returns the sorted names of live tmux sessions whose names
// begin with [kmux][CC] or [kmux][OC]. A missing tmux server (no sessions) yields
// an empty slice, not an error.
func ListAgentSessions() ([]string, error) {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		// `tmux ls` exits non-zero when no server is running: treat as empty.
		if _, ok := err.(*exec.ExitError); ok {
			return nil, nil
		}
		return nil, err
	}

	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name := strings.TrimSpace(line)
		if name != "" && agentSession.MatchString(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// CurrentSession returns the name and active-pane working directory of the tmux
// session the caller is running inside, via `tmux display-message`. It is meant
// for code invoked from within a session (e.g. a script bound to a tmux
// keybinding), where #S and #{pane_current_path} resolve against the pane that
// triggered it. It errors when not run inside tmux ($TMUX unset), the intended
// guard for context that only makes sense within a session.
func CurrentSession() (name, paneDir string, err error) {
	if os.Getenv("TMUX") == "" {
		return "", "", fmt.Errorf("not inside a tmux session ($TMUX unset)")
	}
	out, err := exec.Command("tmux", "display-message", "-p", "#{session_name}\t#{pane_current_path}").Output()
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

// CapturePane returns the visible pane text of a session's active pane (the live
// screen, no scrollback). A missing session or dead tmux server yields empty text,
// not an error, mirroring ListAgentSessions so attention polling never fails the
// whole cycle over one gone session.
func CapturePane(session string) (string, error) {
	out, err := exec.Command("tmux", "capture-pane", "-t", session, "-p").Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return "", nil // no such session / no server: treat as empty
		}
		return "", err
	}
	return string(out), nil
}

// captureSentinel delimits the per-session sections of a batched CapturePanes
// call. Its ASCII record-separator bytes (0x1e) never appear in visible pane
// text, so a section boundary can't collide with captured screen content.
const captureSentinel = "\x1e\x1eKMUXCAP\x1e\x1e"

// CapturePanes captures the visible pane text of many sessions in a single tmux
// invocation, returning a map from session name to text. It chains one
// capture-pane per session, each preceded by a sentinel line, so the N per-poll
// capture spawns collapse to one process.
//
// Chained tmux commands abort at the first error, so a session that died since it
// was listed aborts the rest of the chain; CapturePanes then returns an error (a
// non-zero tmux exit, or a section count that no longer matches the input) rather
// than a partial map, and the caller falls back to capturing each session on its
// own. An empty session list yields an empty map and no tmux call.
func CapturePanes(sessions []string) (map[string]string, error) {
	if len(sessions) == 0 {
		return map[string]string{}, nil
	}
	// Build: display-message -p -t s <sentinel> ; capture-pane -t s -p ; ... — the
	// sentinel precedes each capture so stdout splits into ordered sections. The
	// bare ";" args are tmux command separators (no shell is involved). Targeting
	// display-message at the session avoids needing an attached client, since kmux
	// runs outside tmux ($TMUX unset).
	args := make([]string, 0, len(sessions)*8)
	for i, s := range sessions {
		if i > 0 {
			args = append(args, ";")
		}
		args = append(args, "display-message", "-p", "-t", s, captureSentinel,
			";", "capture-pane", "-t", s, "-p")
	}
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return nil, err
	}
	return parseCapturePanes(string(out), sessions)
}

// parseCapturePanes splits batched capture output into per-session text. stdout is
// <sentinel>\n<pane>\n<sentinel>\n<pane>… ; splitting on the sentinel line yields
// an empty leading section then one section per session, in order. A count that no
// longer matches the input (a truncated/aborted chain, or the sentinel appearing
// in captured text) is an error so the caller falls back to per-session capture.
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

// KillSession kills the tmux session named `name` outright, ending the agent
// process running in it. A missing session is treated as success (it is already
// gone). The next poll drops it from the list and reconcile closes its pane.
func KillSession(name string) error {
	cmd := exec.Command("tmux", "kill-session", "-t", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "can't find session") {
			return nil // already gone
		}
		return fmt.Errorf("tmux kill-session %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// NewDetachedSession creates a detached tmux session named `name` whose first
// window runs `agentCmd` (e.g. "claude" or "opencode") in dir. If the session
// already exists, tmux reports a duplicate and we treat it as success so the
// caller can attach to it.
func NewDetachedSession(name, dir, agentCmd string) error {
	cmd := exec.Command("tmux", "new-session", "-d", "-s", name, "-c", dir, agentCmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "duplicate session") {
			return nil // already exists; caller attaches
		}
		return fmt.Errorf("tmux new-session %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
