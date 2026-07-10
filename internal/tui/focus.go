package tui

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// kittyBundleID is the macOS bundle identifier of the kitty app; restores to it
// are skipped since kitty being frontmost means nothing was stolen.
const kittyBundleID = "net.kovidgoyal.kitty"

// frontmostApp returns an opaque token for the app/window holding system focus,
// captured before kmux spawns a background kitty pane so focus can be handed back
// (see restoreFrontmostApp). Spawning a kitty window steals OS focus even with
// --keep-focus. Returns "" when there is nothing to restore; the token format is
// platform-specific.
func frontmostApp() string {
	switch runtime.GOOS {
	case "darwin":
		return darwinFrontmostApp()
	case "linux":
		return hyprlandActiveWindow()
	default:
		return ""
	}
}

// restoreFrontmostApp hands system focus back to the token captured by
// frontmostApp. Best-effort no-op on empty input or unsupported platforms.
func restoreFrontmostApp(token string) {
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

func darwinFrontmostApp() string {
	out, err := exec.Command("osascript", "-e",
		`tell application "System Events" to bundle identifier of first application process whose frontmost is true`).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// darwinRestoreFrontmostApp activates the app with the given bundle id, a no-op
// when kitty was already frontmost so a spawn while the user is in kitty doesn't
// thrash.
func darwinRestoreFrontmostApp(bundleID string) {
	if bundleID == kittyBundleID {
		return
	}
	_ = exec.Command("osascript", "-e",
		`tell application "System Events" to set frontmost of (first application process whose bundle identifier is "`+bundleID+`") to true`).Run()
}

// hyprlandActiveWindow returns the address of the focused Hyprland window, or ""
// when not under Hyprland, on error, or when none is focused. Unlike macOS there
// is no app to activate, so kmux records the specific compositor window.
func hyprlandActiveWindow() string {
	if os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") == "" {
		return ""
	}
	out, err := exec.Command("hyprctl", "activewindow", "-j").Output()
	if err != nil {
		return ""
	}
	var w struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(out, &w); err != nil {
		return ""
	}
	return w.Address
}

// hyprlandFocusWindow focuses the Hyprland window with the given address.
// Restoring to the same window is a harmless no-op, so no kitty-frontmost guard.
func hyprlandFocusWindow(address string) {
	_ = exec.Command("hyprctl", "dispatch", "focuswindow", "address:"+address).Run()
}
