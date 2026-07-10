package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// agentSession matches the kmux agent prefix [kmux][CC] (claude) or [kmux][OC]
// (opencode), case-insensitively.
var agentSession = regexp.MustCompile(`(?i)^\[kmux\]\[(cc|oc)\]`)

// AgentSession is a live agent session with its anchor directory. Dir is the tmux
// session_path (the `-c` dir), which unlike a pane's path doesn't drift when a shell
// cds elsewhere; empty means tmux reported none.
type AgentSession struct {
	Name string
	Dir  string
}

// sessionListFmt requests the session name and anchor directory, tab-separated.
// Neither field contains a tab, so a single Cut splits them.
const sessionListFmt = "#{session_name}\t#{session_path}"

// ListAgentSessions returns just the sorted session names. A missing tmux server
// yields an empty slice, not an error.
func ListAgentSessions() ([]string, error) {
	full, err := ListAgentSessionsFull()
	if err != nil {
		return nil, err
	}
	names := make([]string, len(full))
	for i, s := range full {
		names[i] = s.Name
	}
	return names, nil
}

// ListAgentSessionsFull returns the agent sessions sorted by name, each with its
// anchor directory, in one tmux call so the poll can spot orphaned sessions without
// a second round-trip. A missing tmux server yields an empty slice, not an error.
func ListAgentSessionsFull() ([]AgentSession, error) {
	out, err := exec.Command("tmux", "list-sessions", "-F", sessionListFmt).Output()
	if err != nil {
		// `tmux ls` exits non-zero when no server is running: treat as empty.
		if _, ok := err.(*exec.ExitError); ok {
			return nil, nil
		}
		return nil, err
	}
	return parseSessionList(string(out)), nil
}

// parseSessionList parses the -F output into sorted agent sessions. Non-agent and
// blank lines are dropped; a line missing the tab separator is skipped rather than
// misread as a name with no directory.
func parseSessionList(out string) []AgentSession {
	var sessions []AgentSession
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		name, dir, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name != "" && agentSession.MatchString(name) {
			sessions = append(sessions, AgentSession{Name: name, Dir: dir})
		}
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Name < sessions[j].Name })
	return sessions
}

// CurrentSession returns the name and active-pane directory of the tmux session the
// caller runs inside, for code invoked from within a session (e.g. a tmux keybinding).
// It errors when not run inside tmux ($TMUX unset).
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

// CapturePane returns the visible pane text of a session's active pane (no
// scrollback). A missing session or dead server yields empty text, not an error, so
// attention polling never fails the whole cycle over one gone session.
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

// captureSentinel delimits the per-session sections of a batched CapturePanes call.
// Its record-separator bytes never appear in visible pane text, so a boundary can't
// collide with captured content.
const captureSentinel = "\x1e\x1eKMUXCAP\x1e\x1e"

// CapturePanes captures many sessions' panes in a single tmux invocation, mapping
// name to text, so the N per-poll spawns collapse to one. Chained tmux commands
// abort at the first error, so a session that died since it was listed returns an
// error (not a partial map) and the caller falls back to per-session capture.
func CapturePanes(sessions []string) (map[string]string, error) {
	if len(sessions) == 0 {
		return map[string]string{}, nil
	}
	// The sentinel precedes each capture so stdout splits into ordered sections. The
	// bare ";" args are tmux command separators (no shell). Targeting display-message
	// at the session avoids needing an attached client, since kmux runs outside tmux.
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

// KillSession kills the named tmux session outright. A missing session is treated
// as success (already gone).
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

// NewDetachedSession creates a detached session running agentCmd in dir. A
// duplicate (session already exists) is treated as success so the caller can attach.
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
