package focus

import (
	"errors"
	"strings"
	"testing"
)

// fakeExec swaps the runCapture/runFocus seams for recorders and restores them at
// test end. capture maps a command name to what it should print (a missing entry
// is an error, standing in for "binary absent" / "server down"); every invocation
// lands in *calls as "name arg arg ...".
func fakeExec(t *testing.T, capture map[string]string) *[]string {
	t.Helper()
	calls := []string{}
	origCapture, origFocus := runCapture, runFocus
	t.Cleanup(func() { runCapture, runFocus = origCapture, origFocus })

	runCapture = func(name string, args ...string) (string, error) {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		out, ok := capture[name]
		if !ok {
			return "", errors.New("not available")
		}
		return out, nil
	}
	runFocus = func(name string, args ...string) error {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
	return &calls
}

// fakeAerospace points the once-resolved aerospace path at bin ("" = not installed)
// for the duration of the test.
func fakeAerospace(t *testing.T, bin string) {
	t.Helper()
	orig := aerospacePath
	t.Cleanup(func() { aerospacePath = orig })
	aerospacePath = func() string { return bin }
}

// findCall returns the first recorded call containing substr, or "".
func findCall(calls []string, substr string) string {
	for _, c := range calls {
		if strings.Contains(c, substr) {
			return c
		}
	}
	return ""
}

// TestDarwinFocusAerospace is the happy path on the machine that has the bug:
// aerospace names the exact window, so the token is window-precise and osascript
// — with its Automation permission and its inability to tell two kitty instances
// apart — is never reached.
func TestDarwinFocusAerospace(t *testing.T) {
	fakeAerospace(t, "/opt/homebrew/bin/aerospace")
	calls := fakeExec(t, map[string]string{"/opt/homebrew/bin/aerospace": "12345\n"})

	token := darwinFrontmostApp()
	if token != "win:12345" {
		t.Fatalf("token = %q, want %q", token, "win:12345")
	}

	darwinRestoreFrontmostApp(token)
	if got := findCall(*calls, "focus --window-id"); got != "/opt/homebrew/bin/aerospace focus --window-id 12345" {
		t.Fatalf("restore call = %q, want aerospace focus --window-id 12345", got)
	}
	if got := findCall(*calls, "osascript"); got != "" {
		t.Fatalf("osascript spawned on the aerospace path: %q", got)
	}
}

// TestDarwinFocusPIDFallback covers a Mac without aerospace: the token falls back
// to the frontmost process's pid, which still separates nvim's kitty instance from
// the dashboard's (what the old bundle-id token could not do).
func TestDarwinFocusPIDFallback(t *testing.T) {
	t.Setenv("KITTY_PID", "999")
	fakeAerospace(t, "")
	calls := fakeExec(t, map[string]string{"osascript": "6789\n"})

	token := darwinFrontmostApp()
	if token != "pid:6789" {
		t.Fatalf("token = %q, want %q", token, "pid:6789")
	}

	darwinRestoreFrontmostApp(token)
	if got := findCall(*calls, "set frontmost"); !strings.Contains(got, "unix id is 6789") {
		t.Fatalf("restore call = %q, want it to target unix id 6789", got)
	}
}

// TestDarwinFocusSkipsOwnKitty: when the dashboard's own kitty was already
// frontmost nothing was stolen, so the capture returns "" and no restore is even
// attempted. The guard sits at capture time so "" keeps meaning "nothing to do".
func TestDarwinFocusSkipsOwnKitty(t *testing.T) {
	t.Setenv("KITTY_PID", "6789")
	fakeAerospace(t, "")
	calls := fakeExec(t, map[string]string{"osascript": "6789\n"})

	if token := darwinFrontmostApp(); token != "" {
		t.Fatalf("token = %q, want \"\" (our own kitty was frontmost)", token)
	}
	Restore("")
	if got := findCall(*calls, "set frontmost"); got != "" {
		t.Fatalf("restore spawned %q, want nothing", got)
	}
}

// TestDarwinFocusRejectsJunk: anything that isn't a lone positive integer degrades
// to the pid path instead of producing a token that would be interpolated into an
// argv or an AppleScript literal.
func TestDarwinFocusRejectsJunk(t *testing.T) {
	t.Setenv("KITTY_PID", "999")
	for _, out := range []string{"", "   ", "abc", "0", "-1", "12345\n678", "1 2"} {
		fakeAerospace(t, "/bin/aerospace")
		fakeExec(t, map[string]string{"/bin/aerospace": out, "osascript": "6789\n"})

		if token := darwinFrontmostApp(); token != "pid:6789" {
			t.Fatalf("aerospace printed %q: token = %q, want the pid fallback", out, token)
		}
	}
}

// TestDarwinRestoreUnknownToken: a token from neither mechanism (a stale bundle
// id, say) is ignored rather than shelled out as a malformed command.
func TestDarwinRestoreUnknownToken(t *testing.T) {
	fakeAerospace(t, "/bin/aerospace")
	calls := fakeExec(t, nil)

	darwinRestoreFrontmostApp("net.kovidgoyal.kitty")
	if len(*calls) != 0 {
		t.Fatalf("unknown token spawned %v, want nothing", *calls)
	}
}

// TestHyprlandFocus covers the Linux path end to end, including the gate that
// makes it a silent no-op under any other compositor.
func TestHyprlandFocus(t *testing.T) {
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	calls := fakeExec(t, map[string]string{"hyprctl": `{"address":"0xdeadbeef"}`})
	if got := hyprlandActiveWindow(); got != "" {
		t.Fatalf("outside Hyprland: got %q, want \"\"", got)
	}
	if len(*calls) != 0 {
		t.Fatalf("outside Hyprland: spawned %v, want nothing", *calls)
	}

	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "sig")
	addr := hyprlandActiveWindow()
	if addr != "0xdeadbeef" {
		t.Fatalf("address = %q, want 0xdeadbeef", addr)
	}

	hyprlandFocusWindow(addr)
	if got := findCall(*calls, "dispatch"); got != "hyprctl dispatch focuswindow address:0xdeadbeef" {
		t.Fatalf("restore call = %q", got)
	}
}
