package agent

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/olli-io/kmux/internal/project"
)

// Session-name separators. An agent session is named
// "<projectPath>@<worktree>‧<CC|OC>", where <projectPath> is the project's main
// worktree path with $HOME abbreviated to "~", the "@<worktree>" segment is
// present only for linked worktrees, and the trailing "‧CC"/"‧OC" identifies the
// agent kind. Embedding the full path (not just the basename) keeps two projects
// that share a basename distinct.
const (
	worktreeSep = "@"
	agentSep    = "‧" // U+2027 HYPHENATION POINT
)

// agentSuffixes maps an agent kind to its tmux session-name suffix.
var agentSuffixes = map[string]string{"claude": agentSep + "CC", "opencode": agentSep + "OC"}

// ExpectedSession returns the claude session name for a project/worktree pair.
// projPath is the project's main-worktree path; wt is "" for the main worktree.
// It mirrors the naming convention parsed by MatchProject and
// tmux.ListAgentSessions (a trailing ‧CC).
func ExpectedSession(projPath, wt string) string {
	name := sessionPrefix(projPath)
	if wt != "" {
		name += worktreeSep + tmuxSafe(wt)
	}
	return name + agentSuffixes["claude"]
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

// SessionForKind rewrites a claude session name (ending in ‧CC, as produced by
// ExpectedSession) into the session name for the given agent kind, swapping the
// trailing suffix. The ‧CC suffix is invariant under tmuxSafe, so a plain suffix
// swap is safe.
func SessionForKind(claudeSession, kind string) string {
	suffix, ok := agentSuffixes[kind]
	if !ok || kind == "claude" {
		return claudeSession
	}
	return strings.TrimSuffix(claudeSession, agentSuffixes["claude"]) + suffix
}

// AgentKind returns "claude", "opencode", or "" for a session name. The agent
// suffix (‧CC / ‧OC) is matched case-insensitively.
func AgentKind(name string) string {
	lower := strings.ToLower(name)
	for kind, suffix := range agentSuffixes {
		if strings.HasSuffix(lower, strings.ToLower(suffix)) {
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
	before, _, _ := strings.Cut(stripAgent(session), worktreeSep)
	return expandHome(before)
}

// ProjectName extracts the project name (the base name of the project path).
func ProjectName(session string) string {
	return filepath.Base(ProjectPath(session))
}

// WorktreeName extracts the worktree segment of a session name, or "" for a
// main-worktree session.
func WorktreeName(session string) string {
	_, after, found := strings.Cut(stripAgent(session), worktreeSep)
	if found {
		return after
	}
	return ""
}

// stripAgent removes the trailing agent suffix (‧CC / ‧OC, case-insensitive)
// from a session name, leaving "<projectPath>[@<worktree>]".
func stripAgent(session string) string {
	lower := strings.ToLower(session)
	for _, suffix := range agentSuffixes {
		if strings.HasSuffix(lower, strings.ToLower(suffix)) {
			return session[:len(session)-len(suffix)]
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
