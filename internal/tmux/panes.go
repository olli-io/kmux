package tmux

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Pane and window user options that hold all kmux grid state. tmux keeps a pane
// option with the pane when the pane moves, so tmux is the only store.
const (
	OptName = "@kmux"      // pane: canonical agent name
	OptDir  = "@kmux_dir"  // pane: project directory of the agent
	OptRole = "@kmux_role" // pane: RoleDash or RoleFiller
	OptAttn = "@kmux_attn" // pane: "1" while the agent is blocked
	OptCol  = "@kmux_col"  // pane: grid column, 1-based
	OptGrid = "@kmux_grid" // window: set on a grid window

	RoleDash   = "dash"
	RoleFiller = "filler"
)

// run executes one tmux invocation and returns stdout. Tests replace it.
var run = func(args ...string) (string, error) {
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return string(out), fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(string(ee.Stderr)))
		}
		return string(out), err
	}
	return string(out), nil
}

// Pane is one line of the server-wide pane snapshot.
type Pane struct {
	ID      string // "%12"
	Name    string // OptName; "" for a pane that is not an agent
	Dir     string // OptDir
	Role    string // OptRole
	Attn    bool   // OptAttn
	Col     int    // OptCol; 0 = not set
	Index   int    // position in the pane list of its window
	Dead    bool
	Session string // live session name
	SessDir string // session_path
	Window  string // "@3"
	Grid    string // OptGrid of the window
	WinW    int
	WinH    int
	Zoomed  bool
	Panes   int // number of panes in the window
	X, Y    int // pane position in the window
	W, H    int // pane size
}

// snapshotFields is the list-panes format. The fields are tab-separated.
var snapshotFields = []string{
	"#{pane_id}", "#{" + OptName + "}", "#{" + OptDir + "}", "#{" + OptRole + "}",
	"#{" + OptAttn + "}", "#{" + OptCol + "}", "#{pane_index}", "#{pane_dead}",
	"#{session_name}", "#{session_path}", "#{window_id}", "#{" + OptGrid + "}",
	"#{window_width}", "#{window_height}", "#{window_zoomed_flag}", "#{window_panes}",
	"#{pane_left}", "#{pane_top}", "#{pane_width}", "#{pane_height}",
}

// Snapshot returns all panes on the server in one tmux call. No server gives an
// empty result, not an error.
func Snapshot() ([]Pane, error) {
	out, err := run("list-panes", "-a", "-F", strings.Join(snapshotFields, "\t"))
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "error connecting") {
			return nil, nil
		}
		return nil, err
	}
	return adopt(parseSnapshot(out)), nil
}

func parseSnapshot(out string) []Pane {
	var panes []Pane
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != len(snapshotFields) || f[0] == "" {
			continue
		}
		atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
		panes = append(panes, Pane{
			ID: f[0], Name: f[1], Dir: f[2], Role: f[3],
			Attn: f[4] == "1", Col: atoi(f[5]), Index: atoi(f[6]), Dead: f[7] == "1",
			Session: f[8], SessDir: f[9], Window: f[10], Grid: f[11],
			WinW: atoi(f[12]), WinH: atoi(f[13]), Zoomed: f[14] == "1", Panes: atoi(f[15]),
			X: atoi(f[16]), Y: atoi(f[17]), W: atoi(f[18]), H: atoi(f[19]),
		})
	}
	return panes
}

// adopt gives an identity to a pane that has none but is the only pane of a
// session with an agent name. Such sessions come from an older kmux or from
// `kmux --agent`. The options are written to tmux, so this occurs one time.
func adopt(panes []Pane) []Pane {
	for i := range panes {
		p := &panes[i]
		if p.Name != "" || p.Role != "" || p.Panes != 1 || !IsAgentSession(p.Session) {
			continue
		}
		p.Name, p.Dir = stripAttnMark(p.Session), p.SessDir
		_ = SetPaneOptions(p.ID, OptName, p.Name, OptDir, p.Dir)
	}
	return panes
}

// SetPaneOptions sets option/value pairs on one pane in one invocation. An empty
// value removes the option.
func SetPaneOptions(pane string, kv ...string) error {
	var cmds [][]string
	for i := 0; i+1 < len(kv); i += 2 {
		cmds = append(cmds, SetPaneOption(pane, kv[i], kv[i+1]))
	}
	return Chain(cmds)
}

// SetPaneOption returns the command that sets (or, for "", removes) a pane option.
func SetPaneOption(pane, key, value string) []string {
	if value == "" {
		return []string{"set-option", "-p", "-u", "-t", pane, key}
	}
	return []string{"set-option", "-p", "-t", pane, key, value}
}

// Chain runs the commands as one tmux invocation. tmux stops at the first error
// and draws one time after the last command.
func Chain(cmds [][]string) error {
	if len(cmds) == 0 {
		return nil
	}
	var args []string
	for i, c := range cmds {
		if i > 0 {
			args = append(args, ";")
		}
		args = append(args, c...)
	}
	_, err := run(args...)
	return err
}

// NewAgentSession creates a detached session that runs agentCmd in dir, with the
// agent identity on its pane, and returns the pane id.
func NewAgentSession(name, dir, agentCmd string) (string, error) {
	out, err := run("new-session", "-d", "-s", name, "-c", dir, "-P", "-F", "#{pane_id}", agentCmd)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(out)
	return id, SetPaneOptions(id, OptName, name, OptDir, dir)
}

// NewHiddenPane creates a detached session with one pane that runs cmd and
// returns the pane id. The grid uses it to make a pane out of view, then moves
// the pane into view in the layout chain. session "" lets tmux select the name.
func NewHiddenPane(session, dir, cmd string) (string, error) {
	args := []string{"new-session", "-d", "-P", "-F", "#{pane_id}"}
	if session != "" {
		args = append(args, "-s", session)
	}
	if dir != "" {
		args = append(args, "-c", dir)
	}
	out, err := run(append(args, cmd)...)
	return strings.TrimSpace(out), err
}

// CapturePaneIDs captures the visible text of many panes in one invocation and
// returns the text for each pane id. One dead pane makes the full call fail; the
// caller then uses CapturePaneID for each pane.
func CapturePaneIDs(ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	var args []string
	for i, id := range ids {
		if i > 0 {
			args = append(args, ";")
		}
		args = append(args, "display-message", "-p", "-t", id, captureSentinel,
			";", "capture-pane", "-t", id, "-p")
	}
	out, err := run(args...)
	if err != nil {
		return nil, err
	}
	return parseCapturePanes(out, ids)
}

// CapturePaneID captures one pane. A pane that is gone gives empty text.
func CapturePaneID(id string) string {
	out, _ := run("capture-pane", "-t", id, "-p")
	return out
}

// KillPane removes a pane. A pane that is gone is not an error.
func KillPane(id string) error {
	if _, err := run("kill-pane", "-t", id); err != nil && !strings.Contains(err.Error(), "can't find") {
		return err
	}
	return nil
}

// Display returns a format string expanded for the target (pane, window or session).
func Display(target, format string) (string, error) {
	return run("display-message", "-p", "-t", target, format)
}

// Agents returns the live agent panes of a snapshot by agent name. If two panes
// have the same name, the first one is used.
func Agents(snap []Pane) map[string]Pane {
	m := make(map[string]Pane)
	for _, p := range snap {
		if p.Name == "" || p.Dead {
			continue
		}
		if _, dup := m[p.Name]; !dup {
			m[p.Name] = p
		}
	}
	return m
}
