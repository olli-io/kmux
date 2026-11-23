package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/olli-io/kmux/internal/config"
	"github.com/olli-io/kmux/internal/project"
)

// Session-name grammar: "[kmux]<marker><projectPath>[@<worktree>]", where
// <marker> is the agent kind's marker from the kinds registry below ("[CC]" claude,
// "[OC]" opencode, "[PI]" pi) and <projectPath> is the main worktree path with $HOME
// abbreviated to "~". The full path (not the basename) keeps two projects that share
// a basename distinct. A no-repo session is
// "orphaned": an orphanMark sits right after the marker and it never carries an
// "@<worktree>" segment. A session blocked on a prompt is renamed to carry an
// optional leading attnMark ("[!!]<...>"); it is transient UI state, so every parser
// strips it (via CanonicalSession/stripAgent) and it never reaches the identity/map key.
const (
	sessionTag  = "[kmux]" // literal leading tag on every kmux-managed session
	worktreeSep = "@"
	orphanMark  = "[∅]"  // ∅ is U+2205 EMPTY SET; marks a no-repo session (after the marker)
	attnMark    = "[!!]" // optional leading marker on a session blocked on a prompt
)

// Kind describes one supported agent. kinds below is the single ordered source of
// truth: the session-name marker, the binary kmux launches, and the labels the
// pickers and badges show all come from here, so adding an agent is one entry plus a
// detect manifest (internal/status/detect) and a UI color.
//
// Key is the default launch key of the kind (see config.DefaultKeybindings).
type Kind struct {
	Name   string // canonical name, as spelled on the CLI and used as a map key
	Marker string // session-name marker, e.g. "[CC]"
	Bin    string // executable kmux runs to start the agent (need not match Name)
	Label  string // display name in the agent picker
	Badge  string // two-letter code for the dashboard badge
	Key    string // default launch key of this kind

	// SandboxBin replaces Bin when kmux wraps the agent in the sandbox runner, for a
	// Bin that is itself a sandbox (pi-sandbox) and cannot nest inside srt. Empty
	// means Bin.
	SandboxBin string
	// SandboxArgs are flags added only inside the sandbox runner: the runner is the
	// security boundary there, so the agent's own permission prompts are skipped.
	SandboxArgs string
}

// kinds is the agent registry in display order — the order the pickers list and the
// order every kind-iterating loop in kmux observes. Markers must stay distinct: the
// name grammar identifies a kind by its marker prefix alone.
var kinds = []Kind{
	{Name: "claude", Marker: "[CC]", Bin: "claude", Label: "Claude", Badge: "CC", Key: "c",
		SandboxArgs: "--dangerously-skip-permissions"},
	{Name: "opencode", Marker: "[OC]", Bin: "opencode", Label: "OpenCode", Badge: "OC", Key: "o",
		SandboxArgs: "--auto"},
	// Unsandboxed, pi launches through the pi-sandbox wrapper, which runs the agent in
	// a rootless container that can see only the project repo. It takes pi's own
	// flags verbatim, so AgentCommand's --continue still applies. Under the sandbox
	// runner the container can't nest, so the runner replaces it and pi runs bare.
	// pi has no permission prompts, so it needs no SandboxArgs.
	{Name: "pi", Marker: "[PI]", Bin: "pi-sandbox", Label: "Pi", Badge: "PI", Key: "p",
		SandboxBin: "pi"},
}

// kindByName indexes kinds for O(1) lookup; built once at init.
var kindByName = func() map[string]Kind {
	m := make(map[string]Kind, len(kinds))
	for _, k := range kinds {
		m[k.Name] = k
	}
	return m
}()

// Kinds returns the supported agent kinds in display order. The slice is a copy, so
// callers can't mutate the registry.
func Kinds() []Kind { return append([]Kind(nil), kinds...) }

// KindNames returns just the kind names, in display order.
func KindNames() []string {
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = k.Name
	}
	return names
}

// LookupKind returns the registry entry for a kind name.
func LookupKind(name string) (Kind, bool) {
	k, ok := kindByName[name]
	return k, ok
}

// ValidKind reports whether name is a supported agent kind.
func ValidKind(name string) bool {
	_, ok := kindByName[name]
	return ok
}

// KindList renders the kind names for a human-facing message, e.g.
// "claude, opencode or pi".
func KindList() string {
	names := KindNames()
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

func agentPrefix(kind string) string {
	return sessionTag + kindByName[kind].Marker
}

// stripAttn removes a leading attnMark ("[!!]") so the rest of the grammar sees the
// canonical name. A live session blocked on a prompt is renamed to "[!!]<name>"; every
// parser routes through here (or CanonicalSession) so the marker never reaches the
// identity/map key.
func stripAttn(name string) string {
	return strings.TrimPrefix(name, attnMark)
}

// HasAttn reports whether a live session name carries the leading "[!!]" marker.
func HasAttn(name string) bool {
	return strings.HasPrefix(name, attnMark)
}

// CanonicalSession strips a leading "[!!]" attention marker, yielding the stable name
// kmux uses as its identity and map key. It is idempotent on an already-canonical name.
func CanonicalSession(name string) string {
	return stripAttn(name)
}

// AttnSession returns the live session name for a canonical session given its attention
// state: "[!!]<session>" when blocked, the plain session otherwise. The input is first
// canonicalized so re-marking an already-marked name can't double the prefix.
func AttnSession(session string, needsAttention bool) string {
	session = CanonicalSession(session)
	if needsAttention {
		return attnMark + session
	}
	return session
}

// DashTabTitle is the name of the dashboard's tmux session and grid window:
// "[kmux][dash]<path>", with the path home-abbreviated and made safe for a tmux
// target. scopeDir is the scoped project's main worktree, or "" in unscoped mode,
// where the ~/git scan root (project.Root) stands in. When needsAttention is set
// (some agent is blocked on a prompt), the name has the "[!!]" prefix.
func DashTabTitle(scopeDir string, needsAttention bool) string {
	path := scopeDir
	if path == "" {
		path = project.Root()
	}
	title := sessionTag + "[dash]" + tmuxSafe(abbrevHome(path))
	if needsAttention {
		title = attnMark + title
	}
	return title
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

// AgentCommand is the shell command that starts kind's agent in dir, wrapped in
// the configured sandbox runner (see BuildAgentCommand). The config is read once
// per process.
func AgentCommand(kind, dir string) string {
	return BuildAgentCommand(kind, dir, sandboxConfig())
}

var sandboxConfig = sync.OnceValue(func() config.Sandbox {
	cfg, _ := config.LoadConfig()
	sb := cfg.Sandbox
	sb.Command = resolveRunner(sb.Command)
	return sb
})

// resolveRunner makes a bare runner name absolute using kmux's own PATH. The tmux
// server's PATH can lack the runner's directory (e.g. pnpm's bin, added only by
// an interactive shell rc), and then the session would exit at once.
func resolveRunner(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if !(config.Sandbox{Command: cmd}).Enabled() || strings.ContainsAny(cmd, " /") {
		return cmd
	}
	if p, err := exec.LookPath(cmd); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	return cmd
}

// BuildAgentCommand launches with --continue so a respawned session resumes the
// last conversation, falling back to a plain launch (`<agent> --continue ||
// <agent>`) because --continue exits non-zero when there's no prior conversation.
//
// With the sandbox enabled the whole line runs inside the runner, as
// `<runner> [-s <settings>] -c '<agent line>'`, and the agent gets its
// SandboxBin and SandboxArgs. A config session (dir in ~/.config) uses the config
// settings file, which lets the agent write ~/.config; when that file is missing
// it falls back to the normal, stricter settings rather than to no sandbox.
func BuildAgentCommand(kind, dir string, sb config.Sandbox) string {
	k, ok := LookupKind(kind)
	if !ok {
		k = kinds[0] // unknown kind falls back to claude, as SessionForKind does
	}
	if !sb.Enabled() {
		return k.Bin + " --continue || " + k.Bin
	}
	bin := k.Bin
	if k.SandboxBin != "" {
		bin = k.SandboxBin
	}
	if k.SandboxArgs != "" {
		bin += " " + k.SandboxArgs
	}
	line := bin + " --continue || " + bin

	settings := sb.Settings
	if IsConfigSession(dir) {
		if p := sb.ConfigSettingsPath(); fileExists(p) {
			settings = p
		}
	}
	cmd := strings.TrimSpace(sb.Command)
	if settings != "" {
		cmd += " -s " + shellQuote(settings)
	}
	return cmd + " -c " + shellQuote(line)
}

// IsConfigSession reports whether dir is the user config dir (~/.config) or below it.
func IsConfigSession(dir string) bool {
	base, err := config.UserConfigBase()
	if err != nil || dir == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(dir))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// shellQuote single-quotes s for sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// SessionForKind swaps a claude session name's leading marker for another kind's.
// The [kmux][CC] prefix is invariant under tmuxSafe, so a plain prefix swap is safe.
func SessionForKind(claudeSession, kind string) string {
	if !ValidKind(kind) || kind == "claude" {
		return claudeSession
	}
	return agentPrefix(kind) + strings.TrimPrefix(claudeSession, agentPrefix("claude"))
}

// AgentKind returns the session name's agent kind, or "" when it is not a kmux agent
// session; the prefix is matched case-insensitively.
func AgentKind(name string) string {
	lower := strings.ToLower(stripAttn(name))
	for _, k := range kinds {
		if strings.HasPrefix(lower, strings.ToLower(sessionTag+k.Marker)) {
			return k.Name
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
	session = stripAttn(session)
	lower := strings.ToLower(session)
	for _, k := range kinds {
		if p := sessionTag + k.Marker; strings.HasPrefix(lower, strings.ToLower(p)) {
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
