package agent

import (
	"strings"

	"github.com/olli-io/kmux/internal/project"
)

// ExpectedSession returns the claude session name for a project/worktree pair.
// wt is "" for the main worktree. It mirrors the naming convention parsed by
// MatchProject and tmux.ListAgentSessions (a trailing ~cl).
func ExpectedSession(proj, wt string) string {
	if wt == "" {
		return tmuxSafe(proj + "~cl")
	}
	return tmuxSafe(proj + "/" + wt + "~cl")
}

// agentSuffixes maps an agent kind to its tmux session-name suffix.
var agentSuffixes = map[string]string{"claude": "~cl", "opencode": "~oc"}

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

// SessionForKind rewrites a claude session name (ending in ~cl, as produced by
// ExpectedSession) into the session name for the given agent kind, swapping the
// trailing suffix. The ~cl suffix is invariant under tmuxSafe, so a plain
// suffix swap is safe.
func SessionForKind(claudeSession, kind string) string {
	suffix, ok := agentSuffixes[kind]
	if !ok || suffix == "~cl" {
		return claudeSession
	}
	return strings.TrimSuffix(claudeSession, "~cl") + suffix
}

// tmuxSafe rewrites a desired session name into the form tmux actually stores.
// tmux forbids '.' and ':' in session names and silently replaces them with
// '_'. Worktree directories often contain a '.' (e.g. repo.branch), so without
// this the computed name never matches the live tmux session and the row fails
// to register as active.
func tmuxSafe(name string) string {
	return strings.NewReplacer(".", "_", ":", "_").Replace(name)
}

// MatchProject finds the project whose name is the longest prefix of rem such
// that rem == name or rem starts with name+"/". It returns the project name and
// the trailing worktree segment ("" when rem == name). ok is false when no
// project matches.
func MatchProject(rem string, names []string) (proj, wt string, ok bool) {
	best := ""
	for _, n := range names {
		if rem == n || strings.HasPrefix(rem, n+"/") {
			if len(n) > len(best) {
				best = n
			}
		}
	}
	if best == "" {
		return "", "", false
	}
	if rem == best {
		return best, "", true
	}
	return best, strings.TrimPrefix(rem, best+"/"), true
}

// ProjectNames extracts the project names (for prefix matching).
func ProjectNames(ps []project.Project) []string {
	n := make([]string, len(ps))
	for i, p := range ps {
		n[i] = p.Name
	}
	return n
}
