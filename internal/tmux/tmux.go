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
// (opencode), case-insensitively, allowing an optional leading "[!!]" attention marker
// that a blocked session carries in its live name.
var agentSession = regexp.MustCompile(`(?i)^(?:\[!!\])?\[kmux\]\[(cc|oc)\]`)

// attnMarkStr is the leading marker a blocked session's live tmux name carries. It is
// duplicated from agent.attnMark (the agent package imports tmux, so tmux can't import
// it back) and must stay in sync.
const attnMarkStr = "[!!]"

// stripAttnMark removes a leading "[!!]" so a live session name collapses to its
// canonical identity. Idempotent on an already-canonical name.
func stripAttnMark(name string) string { return strings.TrimPrefix(name, attnMarkStr) }

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
			// A blocked session's live name carries a leading "[!!]" marker; strip it so
			// Name is the canonical identity every downstream map/parser keys on. The
			// live name is re-derived by LiveName only when a tmux command must address it.
			sessions = append(sessions, AgentSession{Name: stripAttnMark(name), Dir: dir})
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
	out, err := exec.Command("tmux", "capture-pane", "-t", LiveName(session), "-p").Output()
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
	// A blocked session's live name carries "[!!]"; tmux only addresses it by that live
	// name, so resolve canonical -> live once (one list-sessions) and target the live
	// names. Results stay keyed by the canonical input via parseCapturePanes.
	live := liveNames(sessions)
	// The sentinel precedes each capture so stdout splits into ordered sections. The
	// bare ";" args are tmux command separators (no shell). Targeting display-message
	// at the session avoids needing an attached client, since kmux runs outside tmux.
	args := make([]string, 0, len(sessions)*8)
	for i, s := range sessions {
		if i > 0 {
			args = append(args, ";")
		}
		args = append(args, "display-message", "-p", "-t", live[s], captureSentinel,
			";", "capture-pane", "-t", live[s], "-p")
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
	cmd := exec.Command("tmux", "kill-session", "-t", LiveName(name))
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

// LiveName resolves a canonical session name to the actual name tmux currently holds,
// which differs when the session is blocked and carries a leading "[!!]" marker. tmux
// can only address a renamed session by its live name, so every command that targets a
// session (capture, kill, attach, rename) routes its canonical name through here first.
// One list-sessions call; on any error or no match it returns canonical unchanged, so a
// steady, unmarked session pays no penalty in practice (callers pass live names when
// known).
func LiveName(canonical string) string {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return canonical
	}
	for _, name := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if stripAttnMark(name) == canonical {
			return name
		}
	}
	return canonical
}

// liveNames resolves many canonical names to live names in one list-sessions call,
// returning a map keyed by every input canonical (defaulting to itself when tmux has no
// matching session). Used by batched capture so a marked "[!!]" session is still
// addressable without one list-sessions per session.
func liveNames(canonicals []string) map[string]string {
	m := make(map[string]string, len(canonicals))
	for _, c := range canonicals {
		m[c] = c // default: unmarked, live == canonical
	}
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return m
	}
	for _, name := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if canon := stripAttnMark(name); canon != name {
			if _, ok := m[canon]; ok {
				m[canon] = name // marked session: target the live "[!!]" name
			}
		}
	}
	return m
}

// RenameSession renames a session from its current (live) name to newName. It is the
// primitive behind the "[!!]" attention marker: kmux renames a blocked session to
// "[!!]<name>" and back. A missing session (killed mid-poll) is not an error. Renaming
// to the name it already has is a tmux no-op error we also swallow.
func RenameSession(from, to string) error {
	if from == to {
		return nil
	}
	cmd := exec.Command("tmux", "rename-session", "-t", from, to)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "can't find") {
			return nil // session gone; nothing to rename
		}
		return fmt.Errorf("tmux rename-session %s -> %s: %w: %s", from, to, err, strings.TrimSpace(string(out)))
	}
	return nil
}
