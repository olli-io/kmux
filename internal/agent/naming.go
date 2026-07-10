package agent

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/olli-io/kmux/internal/project"
)

// Session-name grammar: "[kmux]<marker><projectPath>[@<worktree>]", where
// <marker> is "[CC]" (claude) or "[OC]" (opencode) and <projectPath> is the main
// worktree path with $HOME abbreviated to "~". The full path (not the basename)
// keeps two projects that share a basename distinct. A no-repo session is
// "orphaned": an orphanMark sits right after the marker and it never carries an
// "@<worktree>" segment.
const (
	sessionTag  = "[kmux]" // literal leading tag on every kmux-managed session
	worktreeSep = "@"
	orphanMark  = "[∅]" // ∅ is U+2205 EMPTY SET; marks a no-repo session (after the marker)
)

var agentMarkers = map[string]string{"claude": "[CC]", "opencode": "[OC]"}

func agentPrefix(kind string) string {
	return sessionTag + agentMarkers[kind]
}

// DashboardTitle carries the same sessionTag as the agent sessions, so the whole
// kmux-managed family is identifiable by the leading "[kmux]".
func DashboardTitle() string {
	return sessionTag + "[dashboard]"
}

// ExpectedSession returns the claude session name for a project/worktree pair;
// wt is "" for the main worktree.
func ExpectedSession(projPath, wt string) string {
	name := agentPrefix("claude") + sessionPrefix(projPath)
	if wt != "" {
		name += worktreeSep + tmuxSafe(wt)
	}
	return name
}

// OrphanSession names a session whose directory is in no git repository: an
// orphanMark after the marker leaves the prefix intact for AgentKind/SessionForKind,
// while dir stands in for a project path that MatchProject never binds.
func OrphanSession(dir string) string {
	return agentPrefix("claude") + orphanMark + sessionPrefix(dir)
}

func IsOrphan(name string) bool {
	return strings.HasPrefix(stripAgent(name), orphanMark)
}

func stripOrphan(rem string) string {
	return strings.TrimPrefix(rem, orphanMark)
}

// sessionPrefix home-abbreviates and tmux-sanitizes a path. ExpectedSession and
// MatchProject both route through it so a live and a computed name mangle identically.
func sessionPrefix(projPath string) string {
	return tmuxSafe(abbrevHome(projPath))
}

// AgentCommand launches with --continue so a respawned session resumes the last
// conversation, falling back to a plain launch (`<agent> --continue || <agent>`)
// because --continue exits non-zero when there's no prior conversation.
func AgentCommand(kind string) string {
	bin := "claude"
	if kind == "opencode" {
		bin = "opencode"
	}
	return bin + " --continue || " + bin
}

// SessionForKind swaps a claude session name's leading marker for another kind's.
// The [kmux][CC] prefix is invariant under tmuxSafe, so a plain prefix swap is safe.
func SessionForKind(claudeSession, kind string) string {
	if _, ok := agentMarkers[kind]; !ok || kind == "claude" {
		return claudeSession
	}
	return agentPrefix(kind) + strings.TrimPrefix(claudeSession, agentPrefix("claude"))
}

// AgentKind returns "claude", "opencode", or "" for a session name; the prefix is
// matched case-insensitively.
func AgentKind(name string) string {
	lower := strings.ToLower(name)
	for kind := range agentMarkers {
		if strings.HasPrefix(lower, strings.ToLower(agentPrefix(kind))) {
			return kind
		}
	}
	return ""
}

// ProjectPath extracts the project path from a session name. The result is
// tmux-sanitized, not byte-identical to the original directory; MatchProject is the
// authoritative way to resolve a session back to a real project.
func ProjectPath(session string) string {
	rem := stripAgent(session)
	// An orphan's whole remainder is the path — it may contain '@', so don't cut on
	// worktreeSep.
	if strings.HasPrefix(rem, orphanMark) {
		return expandHome(stripOrphan(rem))
	}
	before, _, _ := strings.Cut(rem, worktreeSep)
	return expandHome(before)
}

func ProjectName(session string) string {
	return filepath.Base(ProjectPath(session))
}

// WorktreeName returns the worktree segment of a session name, or "" for a
// main-worktree session.
func WorktreeName(session string) string {
	rem := stripAgent(session)
	// Orphan sessions never have a worktree; a '@' in the remainder is part of
	// the directory path, not a separator.
	if strings.HasPrefix(rem, orphanMark) {
		return ""
	}
	_, after, found := strings.Cut(rem, worktreeSep)
	if found {
		return after
	}
	return ""
}

// stripAgent removes the leading agent prefix (case-insensitive), leaving
// "[∅]<projectPath>[@<worktree>]".
func stripAgent(session string) string {
	lower := strings.ToLower(session)
	for kind := range agentMarkers {
		if p := agentPrefix(kind); strings.HasPrefix(lower, strings.ToLower(p)) {
			return session[len(p):]
		}
	}
	return session
}

// tmuxSafe rewrites '.' and ':' to '_' as tmux does silently, so a computed name
// matches the live session instead of failing to register as active.
func tmuxSafe(name string) string {
	return strings.NewReplacer(".", "_", ":", "_").Replace(name)
}

// WorktreeMatches compares name against a session's already-sanitized worktree
// segment.
func WorktreeMatches(segment, name string) bool {
	return segment == tmuxSafe(name)
}

// abbrevHome rewrites a leading $HOME to "~", keeping session names short and stable.
func abbrevHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+"/") {
		return "~" + path[len(home):]
	}
	return path
}

// expandHome is the inverse of abbrevHome: a leading "~" is replaced with $HOME.
func expandHome(name string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return name
	}
	if name == "~" {
		return home
	}
	if strings.HasPrefix(name, "~/") {
		return home + name[1:]
	}
	return name
}

// MatchProject resolves a session name to one of projPaths, comparing via
// sessionPrefix so both sides are mangled identically. The longest matching prefix
// wins, resolving the nested or '@'-in-path case deterministically.
func MatchProject(session string, projPaths []string) (projPath, wt string, ok bool) {
	rem := stripAgent(session)
	bestLen := -1
	for _, p := range projPaths {
		f := sessionPrefix(p)
		switch {
		case rem == f:
			if len(f) > bestLen {
				bestLen, projPath, wt = len(f), p, ""
			}
		case strings.HasPrefix(rem, f+worktreeSep):
			if len(f) > bestLen {
				bestLen, projPath, wt = len(f), p, rem[len(f)+len(worktreeSep):]
			}
		}
	}
	if bestLen < 0 {
		return "", "", false
	}
	return projPath, wt, true
}

func ProjectPaths(ps []project.Project) []string {
	n := make([]string, len(ps))
	for i, p := range ps {
		n[i] = p.Path
	}
	return n
}
