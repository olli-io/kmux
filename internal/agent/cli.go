package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/olli-io/kmux/internal/config"
	"github.com/olli-io/kmux/internal/grid"
	"github.com/olli-io/kmux/internal/project"
	"github.com/olli-io/kmux/internal/tmux"
)

// ParsedArgs is the routed kmux command line: Agent is "" for the dashboard mode,
// and the Print* flags print a value instead of launching.
type ParsedArgs struct {
	Path         string
	Agent        string
	PrintSession bool
	PrintProject bool
	// PrintPane prints the tmux pane id of the running agent, for tools that send
	// text to the agent. The agent can be in the grid or in its own session.
	PrintPane bool
	Help      bool
	// Config launches a config session: the agent (claude by default) in the user
	// config dir (~/.config), sandboxed with the config settings that let it write
	// there.
	Config bool
}

// ParseArgs routes the kmux command line. Flags accept either `--flag claude` or
// `--flag=claude`, and the path and flag may appear in either order. An agent kind is
// validated against the registry (agent.Kinds), so a new kind needs no change here.
func ParseArgs(args []string) (ParsedArgs, error) {
	var pa ParsedArgs
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--help", a == "-help", a == "-h":
			pa.Help = true
		case a == "--agent", a == "-agent":
			if i+1 >= len(args) {
				return pa, fmt.Errorf("--agent requires a value (%s)", KindList())
			}
			i++
			pa.Agent = args[i]
		case strings.HasPrefix(a, "--agent="):
			pa.Agent = strings.TrimPrefix(a, "--agent=")
		case a == "--session", a == "-session":
			if i+1 >= len(args) {
				return pa, fmt.Errorf("--session requires a value (%s)", KindList())
			}
			i++
			pa.Agent, pa.PrintSession = args[i], true
		case strings.HasPrefix(a, "--session="):
			pa.Agent, pa.PrintSession = strings.TrimPrefix(a, "--session="), true
		case a == "--project", a == "-project":
			pa.PrintProject = true
		case a == "--pane", a == "-pane":
			if i+1 >= len(args) {
				return pa, fmt.Errorf("--pane requires a value (%s)", KindList())
			}
			i++
			pa.Agent, pa.PrintPane = args[i], true
		case strings.HasPrefix(a, "--pane="):
			pa.Agent, pa.PrintPane = strings.TrimPrefix(a, "--pane="), true
		case a == "--config", a == "-config":
			pa.Config = true
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
	if pa.Config {
		if pa.Path != "" {
			return pa, fmt.Errorf("--config takes no path: it always opens %s", "~/.config")
		}
		if pa.PrintSession || pa.PrintProject || pa.PrintPane {
			return pa, fmt.Errorf("--config only combines with --agent")
		}
		base, err := config.UserConfigBase()
		if err != nil {
			return pa, err
		}
		pa.Path = base
		if pa.Agent == "" {
			pa.Agent = kinds[0].Name
		}
	}
	if pa.Agent != "" && !ValidKind(pa.Agent) {
		return pa, fmt.Errorf("agent must be %s, got %q", KindList(), pa.Agent)
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
	return attachAgentSession(name, dir, AgentCommand(kind, dir))
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

// PaneID returns the tmux pane id of the running agent for path.
func PaneID(path, kind string) (string, error) {
	name, _, err := sessionPlan(path, kind)
	if err != nil {
		return "", err
	}
	snap, err := tmux.Snapshot()
	if err != nil {
		return "", err
	}
	p, ok := tmux.Agents(snap)[name]
	if !ok {
		return "", fmt.Errorf("no running agent %s", name)
	}
	return p.ID, nil
}

// attachAgentSession shows only the agent name in the calling terminal, in a
// tmux session of its own. It creates the agent (agentCmd in dir) if there is
// none. An agent that is in a kmux grid moves out of the grid first. A dashboard
// leaves an agent alone while a client shows its session, and takes it back into
// the grid when that client detaches.
//
// The tmux client always runs in the calling terminal. If that terminal is a
// tmux pane, the client is a nested one: the caller asked for the agent at this
// terminal, so the outer client must not switch away.
func attachAgentSession(name, dir, agentCmd string) error {
	snap, err := tmux.Snapshot()
	if err != nil {
		return err
	}
	// Each case is one tmux invocation that ends with the attach, so no dashboard
	// poll can see the session without its client.
	var cmds [][]string
	p, ok := tmux.Agents(snap)[name]
	switch {
	case !ok:
		cmds = [][]string{{"new-session", "-A", "-s", name, "-c", dir, agentCmd}}
	case p.ID == os.Getenv("TMUX_PANE"):
		return fmt.Errorf("this terminal is the pane of %s", name)
	case p.Grid != "":
		dummy, err := grid.NewDummy(p)
		if err != nil {
			return err
		}
		cmds = append(grid.ParkCommands(p, dummy), []string{"attach-session", "-t", p.ID})
	default:
		cmds = [][]string{{"attach-session", "-t", p.ID}}
	}
	var args []string
	for i, c := range cmds {
		if i > 0 {
			args = append(args, ";")
		}
		args = append(args, c...)
	}
	cmd := exec.Command("tmux", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// Without TMUX, tmux permits the nested client. The socket stays the default.
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TMUX=") && !strings.HasPrefix(kv, "TMUX_PANE=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tmux: %w", err)
	}
	return nil
}
