package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// agentSession matches tmux session names ending in ~cl (claude) or ~oc
// (opencode), case-insensitively.
var agentSession = regexp.MustCompile(`(?i)~(cl|oc)$`)

// AgentKind returns "claude", "opencode", or "" for a session name. The agent
// suffix is matched case-insensitively.
func AgentKind(name string) string {
	switch {
	case agentSuffix(name, "cl"):
		return "claude"
	case agentSuffix(name, "oc"):
		return "opencode"
	default:
		return ""
	}
}

// agentSuffix reports whether name ends in "~"+suffix, case-insensitively.
func agentSuffix(name, suffix string) bool {
	return strings.HasSuffix(strings.ToLower(name), "~"+suffix)
}

// ListAgentSessions returns the sorted names of live tmux sessions whose names
// end in ~cl or ~oc. A missing tmux server (no sessions) yields an empty slice,
// not an error.
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
