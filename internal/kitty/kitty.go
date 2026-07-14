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

// InKitty reports whether the host terminal is kitty. kitty exports KITTY_PID and
// KITTY_WINDOW_ID and sets TERM=xterm-kitty; any one is a reliable signal.
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

func GotoLayoutSplits() error {
	_, err := kittenAt("goto-layout", "splits")
	return err
}

// Launch splits nextToID along loc, runs `cmd...`, and returns the new window id.
// bias (0 disables) is the percentage of the split given to the new window.
//
// kitty ignores --next-to unless the matched window is in the target tab, which
// defaults to the active tab. kmux often has an unrelated tab focused, so we pin
// the target tab via --match window_id: to keep --next-to reliable.
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

// OpenTab launches a kitty tab running a fresh kmux scoped to dir. exe is the
// running kmux executable's path; the new tab is an independent kmux session.
func OpenTab(exe, dir, title string) error {
	_, err := kittenAt(
		"launch",
		"--type=tab",
		"--cwd", dir,
		"--tab-title", title,
		exe, dir)
	return err
}

// OpenLauncherTab opens a focused kitty tab running `exe --splash` and returns its
// window id so the dashboard can close it once the first reconcile settles. Opened
// focused so it covers the pane churn while the dashboard builds in the background.
func OpenLauncherTab(exe string) (int, error) {
	out, err := kittenAt(
		"launch",
		"--type=tab",
		"--tab-title", "[kmux][launcher]",
		exe, "--splash")
	if err != nil {
		return 0, err
	}
	id, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("parse launcher window id from %q: %w", out, err)
	}
	return id, nil
}

// OpenAgentTab attaches the tmux session `name` in a new kitty tab. Unlike a
// managed pane it is fire-and-forget: the layout never sees it, and closing the
// tab only detaches tmux.
func OpenAgentTab(name, title string) error {
	_, err := kittenAt(
		"launch",
		"--type=tab",
		"--tab-title", title,
		"tmux", "attach", "-t", name)
	return err
}

// OpenCommandTab runs runline (via `sh -c`) in a new kitty tab with cwd dir. It
// backs the user-configurable command keybindings and is fire-and-forget, not a
// managed pane.
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

// RunInWindow types `command` plus Enter into window id via send-text, so the
// shell already there runs it — the only way to start a process in an existing
// kitty window, since `launch` always makes a new one.
func RunInWindow(id int, command string) error {
	_, err := kittenAt("send-text",
		"--match", "id:"+strconv.Itoa(id),
		command+"\r")
	return err
}

// SetWindowTitle sets window id's title and pins it so the running program can't
// overwrite it (--temporary would let a later shell escape win).
func SetWindowTitle(id int, title string) error {
	_, err := kittenAt("set-window-title",
		"--match", "id:"+strconv.Itoa(id),
		title)
	return err
}

// SetTabTitle sets the title of the kitty tab that contains the window with the
// given id. kmux matches on its sidebar window id to title its own dashboard tab
// ("[kmux][dash]…"), independent of which pane inside the tab is focused.
func SetTabTitle(windowID int, title string) error {
	_, err := kittenAt("set-tab-title",
		"--match", "id:"+strconv.Itoa(windowID),
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

// CloseWindow closes window id. Closing one running `tmux attach` only detaches;
// the tmux session keeps running.
func CloseWindow(id int) error {
	_, err := kittenAt("close-window",
		"--match", "id:"+strconv.Itoa(id),
		"--ignore-no-match")
	return err
}

// ResizeWindowHoriz widens (positive) or narrows (negative) window id by increment
// cells. A zero increment is a no-op.
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

// Neighbors are the window ids adjacent to a window on each edge, per `kitten @ ls`.
// kmux reads Top/Bottom to tell which panes share a column. Any edge may be nil.
type Neighbors struct {
	Left   []int `json:"left"`
	Top    []int `json:"top"`
	Right  []int `json:"right"`
	Bottom []int `json:"bottom"`
}

// lsProcess is the subset of a foreground-process record we read: its argv, used
// to recognize a bare shell prompt.
type lsProcess struct {
	Cmdline []string `json:"cmdline"`
}

// lsTabs runs `kitten @ ls` and returns every tab's window list, grouped by tab,
// across all OS windows.
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

// tabWindows returns the windows in the tab containing id (id included), or nil if
// no tab holds it. Confines tab-scoped scans to the dashboard's own tab.
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

// Snapshot derives from a single `kitten @ ls` both the live window ids (all tabs,
// for the manual-close prune) and the blank panes in the tab holding tabWindowID.
// Folding them saves a second `kitten` spawn per poll — the dominant macOS cost.
//
// Blanks are confined to the dashboard's tab: a blank shell in one of kmux's other
// tabs (lazygit, agent attach) isn't the dashboard's to adopt. The live-id set
// spans every tab so the prune sees all managed windows.
func Snapshot(tabWindowID int) (live map[int]bool, blanks []BlankPane, err error) {
	tabs, err := lsTabs()
	if err != nil {
		return nil, nil, err
	}
	live = map[int]bool{}
	for _, t := range tabs {
		holdsTarget := false
		for _, w := range t {
			live[w.ID] = true
			if w.ID == tabWindowID {
				holdsTarget = true
			}
		}
		if holdsTarget {
			blanks = blankPanesIn(t)
		}
	}
	return live, blanks, nil
}

// blankPanesIn returns the bare-shell blank panes among a tab's windows, tagged
// with StandaloneColumn. A bare shell is a pane the user spawned outside kmux
// sitting at a prompt; kmux's own panes run kmux, a tmux client, or `sh -c`.
func blankPanesIn(windows []lsWindow) []BlankPane {
	var panes []BlankPane
	for _, w := range windows {
		if windowIsBareShell(w) {
			panes = append(panes, BlankPane{
				ID:               w.ID,
				StandaloneColumn: len(w.Neighbors.Top) == 0 && len(w.Neighbors.Bottom) == 0,
			})
		}
	}
	return panes
}

// WindowsInTab returns the window count in the tab containing id (id included), or
// 0 if not found. Gates the idle slot's quit key on there being a spare pane.
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

// BlankPane is a user-spawned bare-shell window the dashboard may adopt: its window
// id plus whether it stands alone as a full-height column. The dashboard restacks a
// standalone column under an existing one; any other blank pane becomes an idle
// launcher in place.
type BlankPane struct {
	ID               int
	StandaloneColumn bool // no Top/Bottom neighbor: it's its own full-height column
}

// windowIsBareShell reports whether every foreground process is a bare shell (and
// there is at least one), so kmux never disturbs a pane the user is working in.
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

// isBareShell reports whether cmd is a known shell with no `-c` argument (which
// would mean it's running a script, like kmux's own `sh -c <idle loop>`).
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
