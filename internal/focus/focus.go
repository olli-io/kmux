package focus

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// Indirection seams over the platform shell-outs, mirroring layout.go's kitty
// seams. Only the two dispatchers below are GOOS-aware; every worker is a plain
// function, so both platforms' logic is testable off their own platform.
var (
	runCapture = func(name string, args ...string) (string, error) {
		out, err := exec.Command(name, args...).Output()
		return string(out), err
	}
	runFocus = func(name string, args ...string) error {
		return exec.Command(name, args...).Run()
	}
)

// Frontmost returns an opaque token for the window holding system focus, captured
// before kmux spawns a background kitty pane so focus can be handed back (see
// Restore). Spawning a kitty window steals OS focus even with --keep-focus.
//
// This is the backstop, not the main defence: the dashboard holds automatic pane
// launches back entirely while its own kitty tab is unfocused (see
// layout.ReconcileAll). What it covers is the gap that gate can't — a pass is a
// kitten round trip per launch, so the user can leave partway through one that
// started while focused.
//
// Returns "" when there is nothing to restore; the token format is platform-specific.
func Frontmost() string {
	switch runtime.GOOS {
	case "darwin":
		return darwinFrontmostApp()
	case "linux":
		return hyprlandActiveWindow()
	default:
		return ""
	}
}

// Restore hands system focus back to the token captured by Frontmost.
// Best-effort no-op on empty input or unsupported platforms.
func Restore(token string) {
	if token == "" {
		return
	}
	switch runtime.GOOS {
	case "darwin":
		darwinRestoreFrontmostApp(token)
	case "linux":
		hyprlandFocusWindow(token)
	}
}

// Prefixes tagging which mechanism produced a darwin token, so the restore knows
// how to read it back.
const (
	darwinWinToken = "win:" // aerospace window id (a CGWindowID)
	darwinPIDToken = "pid:" // unix pid of an application process
)

// darwinFrontmostApp prefers aerospace, which names the exact macOS window and so
// matches the Hyprland path's precision. That precision is the point: nvim and the
// dashboard each run in their own kitty instance, and those share a bundle
// identifier, so an app-level token cannot tell them apart. Without aerospace the
// frontmost process's pid at least separates the two instances.
func darwinFrontmostApp() string {
	if id := aerospaceFocusedWindow(); id != "" {
		return darwinWinToken + id
	}
	pid := darwinFrontmostPID()
	// Our own kitty was already frontmost: nothing was stolen, and the pid path
	// can't resolve below process granularity anyway.
	if pid == "" || pid == os.Getenv("KITTY_PID") {
		return ""
	}
	return darwinPIDToken + pid
}

// darwinRestoreFrontmostApp focuses whatever the token names. An unrecognized
// token is a no-op rather than a malformed command. The aerospace path needs no
// "was it already us" guard — refocusing the same window is harmless — and the pid
// path applies its own at capture time.
func darwinRestoreFrontmostApp(token string) {
	switch {
	case strings.HasPrefix(token, darwinWinToken):
		_ = runFocus(aerospacePath(), "focus", "--window-id",
			strings.TrimPrefix(token, darwinWinToken))
	case strings.HasPrefix(token, darwinPIDToken):
		darwinActivatePID(strings.TrimPrefix(token, darwinPIDToken))
	}
}

// aerospacePath resolves the aerospace CLI once per process, "" when absent. A
// GUI-launched kitty inherits launchd's PATH, which often lacks Homebrew, so the
// well-known location is checked too.
var aerospacePath = sync.OnceValue(func() string {
	if p, err := exec.LookPath("aerospace"); err == nil {
		return p
	}
	const brew = "/opt/homebrew/bin/aerospace"
	if info, err := os.Stat(brew); err == nil && info.Mode().IsRegular() {
		return brew
	}
	return ""
})

// aerospaceFocusedWindow returns the focused window's id, or "" when aerospace is
// missing, its server is down, or it printed something unexpected. Every one of
// those falls through to the pid path rather than failing the capture.
func aerospaceFocusedWindow() string {
	bin := aerospacePath()
	if bin == "" {
		return ""
	}
	out, err := runCapture(bin, "list-windows", "--focused", "--format", "%{window-id}")
	if err != nil {
		return ""
	}
	return positiveInt(out)
}

// positiveInt returns out trimmed when it is a lone positive integer, else "".
// It guards twice: against a CLI whose output format changes under us, and — since
// the value is interpolated back into an argv and an AppleScript literal — against
// anything that isn't a bare number getting that far.
func positiveInt(out string) string {
	s := strings.TrimSpace(out)
	if n, err := strconv.Atoi(s); err != nil || n <= 0 {
		return ""
	}
	return s
}

// frontmostPIDScript asks System Events for the pid of the frontmost app. Needs
// the Automation permission; a denial surfaces as an error and yields "".
const frontmostPIDScript = `tell application "System Events" to unix id of first application process whose frontmost is true`

func darwinFrontmostPID() string {
	out, err := runCapture("osascript", "-e", frontmostPIDScript)
	if err != nil {
		return ""
	}
	return positiveInt(out)
}

// darwinActivatePID brings the process with this pid to the front. pid is known to
// be a bare integer (positiveInt), so it is safe inside the script literal.
func darwinActivatePID(pid string) {
	_ = runFocus("osascript", "-e",
		`tell application "System Events" to set frontmost of (first application process whose unix id is `+pid+`) to true`)
}

// hyprlandActiveWindow returns the address of the focused Hyprland window, or ""
// when not under Hyprland, on error, or when none is focused. Unlike macOS there
// is no app to activate, so kmux records the specific compositor window.
func hyprlandActiveWindow() string {
	if os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") == "" {
		return ""
	}
	out, err := runCapture("hyprctl", "activewindow", "-j")
	if err != nil {
		return ""
	}
	var w struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal([]byte(out), &w); err != nil {
		return ""
	}
	return w.Address
}

// hyprlandFocusWindow focuses the Hyprland window with the given address.
// Restoring to the same window is a harmless no-op, so no kitty-frontmost guard.
func hyprlandFocusWindow(address string) {
	_ = runFocus("hyprctl", "dispatch", "focuswindow", "address:"+address)
}
