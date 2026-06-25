package main

import (
	"os/exec"
	"runtime"
	"strings"
)

// kittyBundleID is the macOS bundle identifier of the kitty app. When kitty is
// already frontmost there is nothing to hand focus back to, so restores to it
// are skipped.
const kittyBundleID = "net.kovidgoyal.kitty"

// frontmostApp returns the bundle identifier of the macOS app currently holding
// system focus, or "" on any other platform or on error. It records which app
// the user was in before kmux spawns a kitty pane, so focus can be handed back
// afterwards (see restoreFrontmostApp).
func frontmostApp() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("osascript", "-e",
		`tell application "System Events" to bundle identifier of first application process whose frontmost is true`).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// restoreFrontmostApp returns system focus to the app with the given bundle
// identifier (as captured by frontmostApp). Spawning a kitty window pulls the
// kitty app to the macOS foreground even with --keep-focus (that flag only
// governs the kitty window, not app activation), so this keeps background pane
// spawns from stealing focus from whatever the user was doing. It is a
// best-effort no-op on non-darwin platforms, on empty input, or when kitty was
// already frontmost.
func restoreFrontmostApp(bundleID string) {
	if runtime.GOOS != "darwin" || bundleID == "" || bundleID == kittyBundleID {
		return
	}
	_ = exec.Command("osascript", "-e",
		`tell application "System Events" to set frontmost of (first application process whose bundle identifier is "`+bundleID+`") to true`).Run()
}
