package kitty

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// InKitty reports whether the current process is running inside the kitty
// terminal. kitty exports KITTY_PID and KITTY_WINDOW_ID into every window's
// environment (independent of whether remote control is enabled) and sets
// TERM=xterm-kitty; any of these is a reliable signal that the host terminal is
// kitty. Used to fail fast with a compatibility error in other terminals before
// kmux tries to drive kitty over remote control.
func InKitty() bool {
	return os.Getenv("KITTY_PID") != "" ||
		os.Getenv("KITTY_WINDOW_ID") != "" ||
		os.Getenv("TERM") == "xterm-kitty"
}

// SplitLocation is the kitty `--location` value for the splits layout.
type SplitLocation string

const (
	VSplit SplitLocation = "vsplit" // side-by-side (left/right)
	HSplit SplitLocation = "hsplit" // stacked (top/bottom)
)

// kittenAt runs `kitten @ <args...>` and returns trimmed stdout.
// KITTY_LISTEN_ON in the environment makes the socket implicit.
func kittenAt(args ...string) (string, error) {
	cmd := exec.Command("kitten", append([]string{"@"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("kitten @ %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// GotoLayoutSplits switches the current tab to the splits layout.
func GotoLayoutSplits() error {
	_, err := kittenAt("goto-layout", "splits")
	return err
}

// Launch creates a new window by splitting nextToID along loc and running
// `cmd...`. It returns the new window id. bias (0 disables) is the percentage of
// the split given to the new window.
//
// --next-to picks the window to split relative to, but kitty *ignores* it unless
// the matched window lives in the target tab — which defaults to the currently
// active tab. Since kmux also opens unrelated kitty tabs (lazygit, agent attach,
// project sessions, nvim), the active tab is often not the dashboard's, and a
// reconcile firing then would drop the new pane into the wrong tab / under the
// sidebar. So we pin the target tab to the one containing nextToID via
// --match window_id:..., making --next-to reliable no matter which tab is focused.
func Launch(loc SplitLocation, nextToID, bias int, title string, cmd ...string) (int, error) {
	args := []string{
		"launch",
		"--type=window",
		"--location=" + string(loc),
		"--match", "window_id:" + strconv.Itoa(nextToID),
		"--next-to", "id:" + strconv.Itoa(nextToID),
		"--title", title,
		"--keep-focus",
		"--cwd", "current",
	}
	if bias > 0 {
		args = append(args, "--bias", strconv.Itoa(bias))
	}
	args = append(args, cmd...)

	out, err := kittenAt(args...)
	if err != nil {
		return 0, err
	}
	id, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("parse launch window id from %q: %w", out, err)
	}
	return id, nil
}

// OpenTab launches a new kitty tab in the current OS window running a fresh kmux
// scoped to dir, and focuses it. exe is the running kmux executable's path; the
// new tab is an independent kmux session (own sidebar and panes) sharing the
// same terminal. kitty populates KITTY_LISTEN_ON / KITTY_WINDOW_ID for the new
// window, so the child kmux finds its socket and sidebar id.
func OpenTab(exe, dir, title string) error {
	_, err := kittenAt(
		"launch",
		"--type=tab",
		"--cwd", dir,
		"--tab-title", title,
		exe, dir)
	return err
}

// OpenAgentTab attaches the tmux session `name` in a new kitty tab in the
// current OS window and focuses it. Unlike a managed agent pane, this tab is
// fire-and-forget: Manager/Reconcile/Rebalance never see it, so it stays out of
// the splits layout. Closing the tab only detaches tmux; the session keeps
// running.
func OpenAgentTab(name, title string) error {
	_, err := kittenAt(
		"launch",
		"--type=tab",
		"--tab-title", title,
		"tmux", "attach", "-t", name)
	return err
}

// OpenCommandTab runs runline (via `sh -c`) in a new kitty tab in the current OS
// window with its cwd set to dir, and focuses it. It backs the user-configurable
// command keybindings (editor, lazygit, …). Like the other tab launchers it is
// fire-and-forget: it is NOT a managed pane, so Manager/Reconcile/Rebalance never
// see it; closing the tab closes the command.
func OpenCommandTab(dir, title, runline string) error {
	_, err := kittenAt(
		"launch",
		"--type=tab",
		"--cwd", dir,
		"--tab-title", title,
		"sh", "-c", runline)
	return err
}

// OpenCommandWindow is like OpenCommandTab but opens runline in a new kitty OS
// window (a separate kitty instance) instead of a tab.
func OpenCommandWindow(dir, title, runline string) error {
	_, err := kittenAt(
		"launch",
		"--type=os-window",
		"--cwd", dir,
		"--window-title", title,
		"sh", "-c", runline)
	return err
}

// RunInWindow types `command` followed by Enter into the window with the given
// id via kitty's send-text, so the shell already running there executes it. kmux
// uses it to turn a blank pane the user spawned into a kmux idle launcher (it
// sends an `exec` of the idle-slot loop). The trailing carriage return is the byte
// the Enter key produces, submitting the line. It is the only way to start a
// process in an *existing* kitty window — `launch` always makes a new one.
func RunInWindow(id int, command string) error {
	_, err := kittenAt("send-text",
		"--match", "id:"+strconv.Itoa(id),
		command+"\r")
	return err
}

// SetWindowTitle sets the title of the window with the given id, and pins it so
// the running program can't overwrite it (--temporary would let the next title
// escape from the shell win). kmux uses it to label its own sidebar window
// "[kmux]dashboard", matching the [kmux]… naming its agent sessions carry.
func SetWindowTitle(id int, title string) error {
	_, err := kittenAt("set-window-title",
		"--match", "id:"+strconv.Itoa(id),
		title)
	return err
}

// FocusWindow gives keyboard focus to the window with the given id, switching
// the active tab and OS window as needed.
func FocusWindow(id int) error {
	_, err := kittenAt("focus-window",
		"--match", "id:"+strconv.Itoa(id))
	return err
}

// CloseWindow closes the window with the given id. Closing a window running
// `tmux attach` only detaches; the tmux session keeps running.
func CloseWindow(id int) error {
	_, err := kittenAt("close-window",
		"--match", "id:"+strconv.Itoa(id),
		"--ignore-no-match")
	return err
}

// ResizeWindowHoriz widens (positive) or narrows (negative) the window by
// `increment` cells along the horizontal axis. A zero increment is a no-op.
func ResizeWindowHoriz(id, increment int) error {
	if increment == 0 {
		return nil
	}
	_, err := kittenAt("resize-window",
		"--match", "id:"+strconv.Itoa(id),
		"--axis", "horizontal",
		"--increment", strconv.Itoa(increment))
	return err
}

// lsWindow is the subset of `kitten @ ls` window fields we care about.
type lsWindow struct {
	ID                  int         `json:"id"`
	Title               string      `json:"title"`
	Columns             int         `json:"columns"` // text width in cells
	ForegroundProcesses []lsProcess `json:"foreground_processes"`
	Neighbors           Neighbors   `json:"neighbors"`
}

// Neighbors are the window ids directly adjacent to a window on each edge, as
// reported by `kitten @ ls` (computed by kitty from the live splits tree). kmux
// reads the vertical edges (Top/Bottom) to learn which panes share a column, so it
// can recognize a pane the user stacked into an existing column with a manual
// horizontal split rather than mistaking it for a separate new column. Any edge
// may be absent (kitty omits empty edges), leaving that slice nil.
type Neighbors struct {
	Left   []int `json:"left"`
	Top    []int `json:"top"`
	Right  []int `json:"right"`
	Bottom []int `json:"bottom"`
}

// lsProcess is the subset of a window's foreground-process record we read: its
// argv, used to recognize a pane sitting at a bare shell prompt (see isBareShell).
type lsProcess struct {
	Cmdline []string `json:"cmdline"`
}

// lsTabs runs `kitten @ ls` and returns every tab's window list, grouped by tab
// (the structure kitty reports), across all OS windows. Callers that don't care
// about tab boundaries flatten it (see lsWindows); callers that need to stay
// inside one tab scan for the tab holding a known window id (see tabWindows).
func lsTabs() ([][]lsWindow, error) {
	out, err := kittenAt("ls")
	if err != nil {
		return nil, err
	}
	var osWindows []struct {
		Tabs []struct {
			Windows []lsWindow `json:"windows"`
		} `json:"tabs"`
	}
	if err := json.Unmarshal([]byte(out), &osWindows); err != nil {
		return nil, fmt.Errorf("decode kitten @ ls: %w", err)
	}
	var tabs [][]lsWindow
	for _, ow := range osWindows {
		for _, t := range ow.Tabs {
			tabs = append(tabs, t.Windows)
		}
	}
	return tabs, nil
}

// lsWindows returns every window kitty knows about, flattened across all OS
// windows and tabs.
func lsWindows() ([]lsWindow, error) {
	tabs, err := lsTabs()
	if err != nil {
		return nil, err
	}
	var windows []lsWindow
	for _, t := range tabs {
		windows = append(windows, t...)
	}
	return windows, nil
}

// tabWindows returns the windows in the kitty tab that contains the window with
// the given id (the id itself included), or nil if no tab holds it. kmux uses it
// to confine tab-scoped scans to the dashboard's own tab.
func tabWindows(id int) ([]lsWindow, error) {
	tabs, err := lsTabs()
	if err != nil {
		return nil, err
	}
	for _, t := range tabs {
		for _, w := range t {
			if w.ID == id {
				return t, nil
			}
		}
	}
	return nil, nil
}

// LiveWindowIDs returns the set of window ids currently known to kitty, so the
// manager can drop panes the user closed manually.
func LiveWindowIDs() (map[int]bool, error) {
	windows, err := lsWindows()
	if err != nil {
		return nil, err
	}
	ids := make(map[int]bool, len(windows))
	for _, w := range windows {
		ids[w.ID] = true
	}
	return ids, nil
}

// WindowsInTab returns how many windows live in the kitty tab that contains the
// window with the given id (the id itself included), or 0 if that window isn't
// found. kmux uses it to gate the idle slot's quit key on there being a spare pane
// beyond the dashboard and its fixed columns.
func WindowsInTab(id int) (int, error) {
	t, err := tabWindows(id)
	if err != nil {
		return 0, err
	}
	return len(t), nil
}

// WindowColumns returns each window's current text width in cells, keyed by id.
func WindowColumns() (map[int]int, error) {
	windows, err := lsWindows()
	if err != nil {
		return nil, err
	}
	cols := make(map[int]int, len(windows))
	for _, w := range windows {
		cols[w.ID] = w.Columns
	}
	return cols, nil
}

// BlankPane is a user-spawned bare-shell window the dashboard may adopt: its
// window id, plus whether it stands alone as a full-height column (a manual
// *vertical* split, with no vertical neighbors). The dashboard restacks a
// standalone column under an existing one and turns any other blank pane into an
// idle launcher in place — the StandaloneColumn flag is what decides which,
// computed from the same `ls` snapshot the scan already read.
type BlankPane struct {
	ID               int
	StandaloneColumn bool // no Top/Bottom neighbor: it's its own full-height column
}

// BlankShellWindows returns the windows whose foreground process is a bare
// interactive shell — a pane sitting at a prompt running nothing, which is what a
// pane the user spawned outside kmux (via kitty's own new-window keybinding) looks
// like. It is how the dashboard spots such a blank pane so it can turn it into a
// kmux idle launcher. kmux's own panes never match: the sidebar runs kmux, agent
// panes run a tmux client, and idle slots run `sh -c <loop>` (a script, excluded
// by the -c check) — so only genuinely external blank shells are reported. Each
// pane carries its StandaloneColumn classification (derived from kitty's reported
// neighbors) so the caller needs no second `ls` to decide how to adopt it.
//
// The scan is confined to the kitty tab that holds tabWindowID (the dashboard's
// sidebar window): kmux also opens unrelated tabs (lazygit, agent attach, project
// sessions), and a blank shell sitting in one of those is not the dashboard's to
// adopt. If no tab holds tabWindowID, nothing is reported.
func BlankShellWindows(tabWindowID int) ([]BlankPane, error) {
	windows, err := tabWindows(tabWindowID)
	if err != nil {
		return nil, err
	}
	var panes []BlankPane
	for _, w := range windows {
		if windowIsBareShell(w) {
			panes = append(panes, BlankPane{
				ID:               w.ID,
				StandaloneColumn: len(w.Neighbors.Top) == 0 && len(w.Neighbors.Bottom) == 0,
			})
		}
	}
	return panes, nil
}

// windowIsBareShell reports whether every one of a window's foreground processes
// is a bare interactive shell (and there is at least one). A window running a
// command (its foreground process is that command, not a shell) does not match, so
// kmux never disturbs a pane the user is actively working in.
func windowIsBareShell(w lsWindow) bool {
	if len(w.ForegroundProcesses) == 0 {
		return false
	}
	for _, p := range w.ForegroundProcesses {
		if !isBareShell(p.Cmdline) {
			return false
		}
	}
	return true
}

// shellNames are the program basenames treated as interactive shells (a login
// shell appears as "-bash", so the leading "-" is stripped before the lookup).
var shellNames = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true,
}

// isBareShell reports whether cmd is an interactive shell sitting at a prompt: its
// program is a known shell and it carries no `-c` argument (which would mean it is
// running a script — e.g. kmux's own `sh -c <idle loop>` placeholders).
func isBareShell(cmd []string) bool {
	if len(cmd) == 0 {
		return false
	}
	if !shellNames[strings.TrimPrefix(filepath.Base(cmd[0]), "-")] {
		return false
	}
	for _, a := range cmd[1:] {
		if a == "-c" {
			return false
		}
	}
	return true
}

