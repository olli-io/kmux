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

// ParsedArgs is the routed kmux command line: Agent is "" for the dashboard mode,
// the Print* flags print a value instead of launching, and Splash renders the
// launch-overlay animation.
type ParsedArgs struct {
	Path         string
	Agent        string
	PrintSession bool
	PrintProject bool
	Splash       bool
	Help         bool
}

// ParseArgs routes the kmux command line. Flags accept either `--flag claude` or
// `--flag=claude`, and the path and flag may appear in either order.
func ParseArgs(args []string) (ParsedArgs, error) {
	var pa ParsedArgs
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--help", a == "-help", a == "-h":
			pa.Help = true
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
		case a == "--splash", a == "-splash":
			pa.Splash = true
		case strings.HasPrefix(a, "-"):
			return pa, fmt.Errorf("unknown flag: %s", a)
		default:
			if pa.Path != "" {
				return pa, fmt.Errorf("unexpected argument: %s", a)
			}
			pa.Path = a
		}
	}
	if pa.Help {
		return pa, nil // --help wins over any other flag or validation error
	}
	if pa.Agent != "" && pa.Agent != "claude" && pa.Agent != "opencode" {
		return pa, fmt.Errorf("agent must be 'claude' or 'opencode', got %q", pa.Agent)
	}
	return pa, nil
}

// RunAgent attaches the current terminal to the agent session for path, creating
// it if needed. The name follows kmux's convention, so launching here and opening
// the dashboard focus the same agent.
func RunAgent(path, kind string) error {
	name, dir, err := sessionPlan(path, kind)
	if err != nil {
		return err
	}
	return attachAgentSession(name, dir, AgentCommand(kind))
}

// SessionName returns the exact name RunAgent would create for path, so other
// tools can target the same session.
func SessionName(path, kind string) (string, error) {
	name, _, err := sessionPlan(path, kind)
	return name, err
}

// CurrentProjectDir resolves the directory of the agent session the caller is
// inside, for scripts bound to tmux keybindings. It takes the git worktree root of
// the pane's path, which stays correct even after the agent cd's into a
// subdirectory; an orphaned (no-repo) pane falls back to the sanitized path encoded
// in the session name.
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

// sessionPlan resolves the session name and directory for path. A path in no git
// repository is not an error: it falls back to orphanPlan.
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

// orphanPlan resolves the session for a path in no git repository. The path is
// resolved to an absolute, symlink-free form so the name is stable regardless of
// how the directory was addressed.
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

// resolveWorktree locates which of proj's worktrees contains path. ScanProject
// always anchors proj at the main worktree, so the actual checkout is resolved
// separately here from git's toplevel; an unknown worktree falls back to the main.
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

func gitToplevel(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// attachAgentSession attaches to tmux session name, creating it (running agentCmd
// in dir) if absent — `new-session -A` does both. stdio is inherited so the agent
// runs in the foreground of the calling terminal.
func attachAgentSession(name, dir, agentCmd string) error {
	cmd := exec.Command("tmux", "new-session", "-A", "-s", name, "-c", dir, agentCmd)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
