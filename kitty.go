package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

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
// Note: launch's --match selects a *tab*, and --location splits relative to the
// active window. To split a specific window we must use --next-to; otherwise
// every split would target the focused sidebar (placing panes under it).
func Launch(loc SplitLocation, nextToID, bias int, title string, cmd ...string) (int, error) {
	args := []string{
		"launch",
		"--type=window",
		"--location=" + string(loc),
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

// OpenLazygit opens lazygit for dir in a new kitty tab in the current OS window
// and focuses it. The tab runs lazygit with its cwd set to dir. This is
// fire-and-forget: it is NOT a managed pane, so Manager/Reconcile/Rebalance never
// see it; closing lazygit closes the tab.
func OpenLazygit(dir string) error {
	_, err := kittenAt(
		"launch",
		"--type=tab",
		"--cwd", dir,
		"--tab-title", "lazygit · "+filepath.Base(dir),
		"lazygit")
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
	ID      int    `json:"id"`
	Title   string `json:"title"`
	Columns int    `json:"columns"` // text width in cells
}

// lsWindows returns every window kitty knows about, flattened across all OS
// windows and tabs.
func lsWindows() ([]lsWindow, error) {
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
	var windows []lsWindow
	for _, ow := range osWindows {
		for _, t := range ow.Tabs {
			windows = append(windows, t.Windows...)
		}
	}
	return windows, nil
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
