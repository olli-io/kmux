package tui

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// kittyBundleID is the macOS bundle identifier of the kitty app. When kitty is
// already frontmost there is nothing to hand focus back to, so restores to it
// are skipped.
const kittyBundleID = "net.kovidgoyal.kitty"

// frontmostApp returns an opaque token identifying the window/app that currently
// holds system focus, captured before kmux spawns a background kitty pane so
// focus can be handed back afterwards (see restoreFrontmostApp). Spawning a kitty
// window pulls it to the foreground even with --keep-focus (that flag only
// governs which kitty window is active, not OS-level activation): on macOS it
// activates the kitty app, on Hyprland it raises kitty's compositor window — both
// steal system focus from whatever the user was doing. Returns "" when there is
// nothing to restore (unsupported platform/compositor or error). The token format
// is platform-specific and only meaningful to restoreFrontmostApp.
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

// restoreFrontmostApp returns system focus to the window/app identified by token
// (as captured by frontmostApp), keeping background pane spawns from stealing
// focus. It is a best-effort no-op on empty input or unsupported platforms.
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

// darwinFrontmostApp returns the bundle identifier of the macOS app currently
// holding system focus, or "" on error.
func darwinFrontmostApp() string {
	out, err := exec.Command("osascript", "-e",
		`tell application "System Events" to bundle identifier of first application process whose frontmost is true`).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// darwinRestoreFrontmostApp activates the macOS app with the given bundle
// identifier. It is a no-op when kitty was already frontmost (nothing was
// stolen), so a background spawn while the user is in kitty does not thrash.
func darwinRestoreFrontmostApp(bundleID string) {
	if bundleID == kittyBundleID {
		return
	}
	_ = exec.Command("osascript", "-e",
		`tell application "System Events" to set frontmost of (first application process whose bundle identifier is "`+bundleID+`") to true`).Run()
}

// hyprlandActiveWindow returns the address of the Hyprland window that currently
// holds focus (e.g. "0x55f0..."), or "" when not running under Hyprland, on
// error, or when no window is focused. It is the Linux focus token: unlike macOS
// there is no app to activate, so kmux records the specific compositor window and
// dispatches focus back to it afterwards.
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

// hyprlandFocusWindow gives keyboard focus back to the Hyprland window with the
// given address. Restoring focus to the same window kitty spawned from is a
// harmless no-op, so unlike the macOS path no kitty-frontmost guard is needed.
func hyprlandFocusWindow(address string) {
	_ = exec.Command("hyprctl", "dispatch", "focuswindow", "address:"+address).Run()
}
