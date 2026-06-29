package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/olli-io/kmux/internal/project"
	"github.com/olli-io/kmux/internal/tmux"
)

// ParsedArgs is the routed kmux command line. Agent is "" for the default
// dashboard mode and "claude"/"opencode" for the agent modes; PrintSession is set
// by --session to print the resolved session name instead of launching it;
// PrintProject is set by --project to print the project directory of the current
// tmux session instead of launching anything; Path is the optional directory
// argument ("" means the current directory).
type ParsedArgs struct {
	Path         string
	Agent        string
	PrintSession bool
	PrintProject bool
}

// ParseArgs routes the kmux command line. With no agent flag it selects the
// dashboard (the historical behaviour); --agent selects the agent launcher, and
// --session prints the session name that --agent would create (for scripting)
// and exits. --project prints the project directory of the tmux session the
// caller is inside (for scripts bound to a tmux keybinding) and exits; it takes
// no value and ignores any path. The --agent/--session flags take a kind (claude
// or opencode) and accept either `--flag claude` or `--flag=claude`. The path and
// the flag may appear in either order, so `kmux PATH --agent claude` and
// `kmux --agent claude PATH` parse the same.
func ParseArgs(args []string) (ParsedArgs, error) {
	var pa ParsedArgs
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--agent", a == "-agent":
			if i+1 >= len(args) {
				return pa, fmt.Errorf("--agent requires a value (claude or opencode)")
			}
			i++
			pa.Agent = args[i]
		case strings.HasPrefix(a, "--agent="):
			pa.Agent = strings.TrimPrefix(a, "--agent=")
		case a == "--session", a == "-session":
			if i+1 >= len(args) {
				return pa, fmt.Errorf("--session requires a value (claude or opencode)")
			}
			i++
			pa.Agent, pa.PrintSession = args[i], true
		case strings.HasPrefix(a, "--session="):
			pa.Agent, pa.PrintSession = strings.TrimPrefix(a, "--session="), true
		case a == "--project", a == "-project":
			pa.PrintProject = true
		case strings.HasPrefix(a, "-"):
			return pa, fmt.Errorf("unknown flag: %s", a)
		default:
			if pa.Path != "" {
				return pa, fmt.Errorf("unexpected argument: %s", a)
			}
			pa.Path = a
		}
	}
	if pa.Agent != "" && pa.Agent != "claude" && pa.Agent != "opencode" {
		return pa, fmt.Errorf("agent must be 'claude' or 'opencode', got %q", pa.Agent)
	}
	return pa, nil
}

// RunAgent creates (if needed) and attaches the current terminal to the tmux
// session for the given agent kind in the project containing path. The session
// name follows kmux's convention (ExpectedSession + SessionForKind), so the
// session the dashboard would spawn and the one this launches are one and the
// same — launching here, then opening the dashboard, focuses the same agent.
func RunAgent(path, kind string) error {
	name, dir, err := sessionPlan(path, kind)
	if err != nil {
		return err
	}
	return attachAgentSession(name, dir, AgentCommand(kind))
}

// SessionName returns the tmux session name kmux uses for the given agent kind
// in the project/worktree containing path ("" = the current directory). It is
// the exact name RunAgent would create, so other tools can target the same
// session (e.g. `tmux send-keys -t "$(kmux --session claude)"`).
func SessionName(path, kind string) (string, error) {
	name, _, err := sessionPlan(path, kind)
	return name, err
}

// CurrentProjectDir resolves the project (or worktree) directory of the kmux
// agent session the caller is currently inside. It is meant for scripts bound to
// tmux keybindings within an agent pane (e.g. "open this session's project in an
// editor"): it reads the current tmux session, requires it to be a kmux agent
// session (a ‧CC/‧OC name), and prints the directory to launch tooling in.
//
// The directory is taken from the git worktree root of the pane's current path —
// that is where the session was anchored and stays correct even if the agent has
// cd'd into a subdirectory. A pane that is in no git repository (an orphaned
// session) falls back to the project path encoded in the session name. The
// session name is preferred only as a fallback because it is tmux-sanitized
// (any '.' became '_'), so it may not be byte-identical to the real path.
func CurrentProjectDir() (string, error) {
	name, paneDir, err := tmux.CurrentSession()
	if err != nil {
		return "", err
	}
	if AgentKind(name) == "" {
		return "", fmt.Errorf("current tmux session %q is not a kmux agent session", name)
	}
	if paneDir != "" {
		if top, err := gitToplevel(paneDir); err == nil && top != "" {
			return top, nil
		}
	}
	if dir := ProjectPath(name); dir != "" {
		return dir, nil
	}
	return "", fmt.Errorf("could not resolve a project directory for session %q", name)
}

// sessionPlan resolves the session name and working directory for an agent kind
// in the project/worktree containing path ("" = the current directory). A path
// that lives in no git repository is not an error: it falls back to orphanPlan,
// which anchors the session at the directory itself.
func sessionPlan(path, kind string) (name, dir string, err error) {
	if path == "" {
		path = "."
	}
	proj, err := project.ScanProject(path)
	if err != nil {
		return orphanPlan(path, kind)
	}
	dir, wt := resolveWorktree(path, proj)
	return SessionForKind(ExpectedSession(proj.Path, wt), kind), dir, nil
}

// orphanPlan resolves the session name and working directory for a path that is
// not inside any git repository. The directory's own absolute path stands in for
// a project path, marked with a leading orphanMark (see OrphanSession) — it won't
// bind to any ~/git project (MatchProject returns ok=false), and the dashboard
// files it under "(ungrouped)". The path is resolved to an absolute, symlink-free
// form so the name is stable regardless of how the directory was addressed (e.g.
// "." vs its full path).
func orphanPlan(path, kind string) (name, dir string, err error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("%s is not a directory", abs)
	}
	return SessionForKind(OrphanSession(abs), kind), abs, nil
}

// resolveWorktree locates which of a project's worktrees contains path, returning
// that worktree's root directory and its session-name segment ("" for the main
// worktree). ScanProject always anchors proj at the main worktree regardless of
// which worktree path lives in, so the actual checkout is resolved separately
// here from git's toplevel. A path that resolves to no known worktree (or an
// unreadable toplevel) falls back to the main worktree.
func resolveWorktree(path string, proj *project.Project) (dir, wt string) {
	top, err := gitToplevel(path)
	if err != nil || top == "" || top == proj.Path {
		return proj.Path, ""
	}
	for _, w := range proj.Worktrees {
		if w.Path == top {
			return top, w.Name
		}
	}
	return top, ""
}

// gitToplevel returns the root directory of the git worktree containing dir.
func gitToplevel(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// attachAgentSession attaches the current terminal to tmux session `name`,
// creating it first (running agentCmd in dir) when it doesn't already exist.
// `tmux new-session -A` does both: it attaches to an existing session, or
// creates and attaches otherwise (in which case -c/the command take effect).
// stdio is inherited so the agent runs in the foreground of the calling terminal.
func attachAgentSession(name, dir, agentCmd string) error {
	cmd := exec.Command("tmux", "new-session", "-A", "-s", name, "-c", dir, agentCmd)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
