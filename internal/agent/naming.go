package agent

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/olli-io/kmux/internal/project"
)

// Session-name structure. An agent session is named
// "[kmux]<marker><projectPath>[@<worktree>]", where <marker> is "[CC]" (claude)
// or "[OC]" (opencode), <projectPath> is the project's main worktree path with
// $HOME abbreviated to "~", and the "@<worktree>" segment is present only for
// linked worktrees. The leading "[kmux]" tag plus marker identify a
// kmux-managed agent session and its kind. Embedding the full path (not just the
// basename) keeps two projects that share a basename distinct.
//
// A session whose directory is in no git repository is "orphaned" and carries an
// orphanMark immediately after the marker ("[kmux][CC][∅]<projectPath>", never
// with an "@<worktree>" segment). stripAgent removes the "[kmux]<marker>" prefix,
// so the orphan and worktree parsing below operate on the remainder; IsOrphan and
// stripOrphan handle the mark.
const (
	sessionTag  = "[kmux]" // literal leading tag on every kmux-managed session
	worktreeSep = "@"
	orphanMark  = "[∅]" // ∅ is U+2205 EMPTY SET; marks a no-repo session (after the marker)
)

// agentMarkers maps an agent kind to its bracketed tmux session-name marker,
// which follows sessionTag at the front of the name.
var agentMarkers = map[string]string{"claude": "[CC]", "opencode": "[OC]"}

// agentPrefix returns the full leading prefix for an agent kind, e.g.
// "[kmux][CC]". It is the invariant head of every session of that kind.
func agentPrefix(kind string) string {
	return sessionTag + agentMarkers[kind]
}

// DashboardTitle is the window title for the kmux dashboard process itself. It
// carries the same sessionTag as the agent sessions, with a bracketed marker
// mirroring their "[CC]"/"[OC]" form ("[kmux][dashboard]"), so the whole
// kmux-managed family is identifiable by the leading "[kmux]".
func DashboardTitle() string {
	return sessionTag + "[dashboard]"
}

// ExpectedSession returns the claude session name for a project/worktree pair.
// projPath is the project's main-worktree path; wt is "" for the main worktree.
// It mirrors the naming convention parsed by MatchProject and
// tmux.ListAgentSessions (a leading [kmux][CC]).
func ExpectedSession(projPath, wt string) string {
	name := agentPrefix("claude") + sessionPrefix(projPath)
	if wt != "" {
		name += worktreeSep + tmuxSafe(wt)
	}
	return name
}

// OrphanSession returns the session name for an orphaned agent session — one
// whose directory dir is not inside any git repository. It is ExpectedSession's
// no-worktree form with an orphanMark inserted after the marker, so
// AgentKind/SessionForKind keep working on the (unchanged) prefix while IsOrphan
// recognises the mark. dir is the directory's own path, standing in for a project
// path; MatchProject never binds it to a real project, so the dashboard files it
// under "(ungrouped)".
func OrphanSession(dir string) string {
	return agentPrefix("claude") + orphanMark + sessionPrefix(dir)
}

// IsOrphan reports whether name is an orphaned (no-repo) session, identified by
// an orphanMark immediately after the [kmux]<marker> prefix.
func IsOrphan(name string) bool {
	return strings.HasPrefix(stripAgent(name), orphanMark)
}

// stripOrphan removes a leading orphanMark from an agent-stripped remainder,
// leaving the path that the rest of the parsers expect.
func stripOrphan(rem string) string {
	return strings.TrimPrefix(rem, orphanMark)
}

// sessionPrefix is the per-project leading portion of a session name: the main
// worktree path, home-abbreviated and tmux-sanitized. ExpectedSession and
// MatchProject both route through it so a live tmux name and a computed one
// mangle identically.
func sessionPrefix(projPath string) string {
	return tmuxSafe(abbrevHome(projPath))
}

// AgentCommand returns the command launched for an agent kind. Both agents are
// launched with --continue so a respawned session (e.g. after idle reap)
// resumes the most recent conversation in that directory; on a first-ever
// launch with no prior conversation, --continue starts a fresh session.
func AgentCommand(kind string) string {
	if kind == "opencode" {
		return "opencode --continue"
	}
	return "claude --continue"
}

// SessionForKind rewrites a claude session name (starting with [kmux][CC], as
// produced by ExpectedSession) into the session name for the given agent kind,
// swapping the leading marker. The [kmux][CC] prefix is invariant under tmuxSafe,
// so a plain prefix swap is safe.
func SessionForKind(claudeSession, kind string) string {
	if _, ok := agentMarkers[kind]; !ok || kind == "claude" {
		return claudeSession
	}
	return agentPrefix(kind) + strings.TrimPrefix(claudeSession, agentPrefix("claude"))
}

// AgentKind returns "claude", "opencode", or "" for a session name. The agent
// prefix ([kmux][CC] / [kmux][OC]) is matched case-insensitively.
func AgentKind(name string) string {
	lower := strings.ToLower(name)
	for kind := range agentMarkers {
		if strings.HasPrefix(lower, strings.ToLower(agentPrefix(kind))) {
			return kind
		}
	}
	return ""
}

// ProjectPath extracts the project path from a session name: the portion before
// the worktree separator, with the leading "~" expanded back to $HOME. Note the
// path is the tmux-sanitized form (any '.' became '_'), so it is not guaranteed
// byte-identical to the original directory; MatchProject is the authoritative way
// to resolve a session back to a real project.
func ProjectPath(session string) string {
	rem := stripAgent(session)
	// An orphan's whole remainder (after the mark) is the path: it has no worktree
	// segment, and the path itself may legitimately contain '@', so don't cut on
	// worktreeSep.
	if strings.HasPrefix(rem, orphanMark) {
		return expandHome(stripOrphan(rem))
	}
	before, _, _ := strings.Cut(rem, worktreeSep)
	return expandHome(before)
}

// ProjectName extracts the project name (the base name of the project path).
func ProjectName(session string) string {
	return filepath.Base(ProjectPath(session))
}

// WorktreeName extracts the worktree segment of a session name, or "" for a
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

// stripAgent removes the leading agent prefix ([kmux][CC] / [kmux][OC],
// case-insensitive) from a session name, leaving "[∅]<projectPath>[@<worktree>]".
func stripAgent(session string) string {
	lower := strings.ToLower(session)
	for kind := range agentMarkers {
		if p := agentPrefix(kind); strings.HasPrefix(lower, strings.ToLower(p)) {
			return session[len(p):]
		}
	}
	return session
}

// tmuxSafe rewrites a desired session name into the form tmux actually stores.
// tmux forbids '.' and ':' in session names and silently replaces them with
// '_'. Project paths and worktree directories often contain a '.' (e.g.
// repo.branch), so without this the computed name never matches the live tmux
// session and the row fails to register as active.
func tmuxSafe(name string) string {
	return strings.NewReplacer(".", "_", ":", "_").Replace(name)
}

// WorktreeMatches reports whether a session's worktree segment (as it appears in
// a session name, i.e. tmux-sanitized) refers to the worktree named name.
func WorktreeMatches(segment, name string) bool {
	return segment == tmuxSafe(name)
}

// abbrevHome rewrites a leading $HOME in path to "~", keeping session names short
// and stable. Paths outside $HOME (or when $HOME is unknown) are returned
// unchanged.
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

// MatchProject matches a session name to one of projPaths (the projects' main
// worktree paths). It compares the session's path portion against each project's
// sessionPrefix — so both sides are home-abbreviated and tmux-sanitized
// identically — and returns the matched real path, the worktree segment ("" for
// the main worktree), and ok. The longest matching prefix wins, resolving the
// rare nested or '@'-in-path case deterministically.
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

// ProjectPaths extracts the projects' main-worktree paths (for matching sessions).
func ProjectPaths(ps []project.Project) []string {
	n := make([]string, len(ps))
	for i, p := range ps {
		n[i] = p.Path
	}
	return n
}
