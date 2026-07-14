package agent

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olli-io/kmux/internal/status"
)

func TestClassifyAttnEvent(t *testing.T) {
	cases := []struct {
		name  string
		event string
		ntype string
		want  attnAction
	}{
		// Claude Code (Notification hook + notification_type).
		{"claude permission", "Notification", "permission_prompt", attnSetPerm},
		{"claude idle", "Notification", "idle_prompt", attnSetWaiting},
		{"claude stop clears", "Stop", "", attnClear},
		{"claude submit clears", "UserPromptSubmit", "", attnClear},
		{"claude auth ignored", "Notification", "auth_success", attnNone},
		// OpenCode (plugin event.type, passed via --event).
		{"opencode permission updated", "permission.updated", "", attnSetPerm},
		{"opencode permission asked", "permission.asked", "", attnSetPerm},
		{"opencode permission replied clears", "permission.replied", "", attnClear},
		{"opencode idle", "session.idle", "", attnSetWaiting},
		{"opencode status ignored", "session.status", "", attnNone},
		{"unknown", "whatever.happened", "", attnNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyAttnEvent(c.event, c.ntype); got != c.want {
				t.Errorf("classifyAttnEvent(%q,%q) = %d, want %d", c.event, c.ntype, got, c.want)
			}
		})
	}
}

// TestRunAttnHookFlags drives the OpenCode-plugin path (event as flags) end to end
// against an orphan dir (t.TempDir is outside any git repo) with a temp config, and
// confirms the marker the dashboard would read is written and cleared.
func TestRunAttnHookFlags(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(dir)
	wantSession := OrphanSession(resolved) // opencode marker swap below
	wantSession = SessionForKind(wantSession, "opencode")

	// Permission request -> a permission marker for the opencode session.
	pa := ParsedArgs{Attn: true, AttnKind: "opencode", AttnEvent: "permission.updated", AttnCwd: dir}
	if err := RunAttnHook(pa, nil); err != nil {
		t.Fatalf("RunAttnHook(permission): %v", err)
	}
	markers, _ := status.LoadAttnMarkers(time.Now())
	if got := markers[wantSession]; got != status.AttnPermission {
		t.Fatalf("marker[%q] = %d, want AttnPermission; all=%v", wantSession, got, markers)
	}

	// Resolve -> marker cleared.
	pa.AttnEvent = "permission.replied"
	if err := RunAttnHook(pa, nil); err != nil {
		t.Fatalf("RunAttnHook(replied): %v", err)
	}
	markers, _ = status.LoadAttnMarkers(time.Now())
	if _, ok := markers[wantSession]; ok {
		t.Fatalf("marker not cleared: %v", markers)
	}
}

// TestRunAttnHookStdin drives the Claude-Code hook path (event JSON on stdin) and
// confirms an idle_prompt Notification writes a waiting marker for the claude session.
func TestRunAttnHookStdin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(dir)
	wantSession := OrphanSession(resolved) // default kind is claude

	payload := `{"hook_event_name":"Notification","notification_type":"idle_prompt","cwd":"` + dir + `"}`
	pa := ParsedArgs{Attn: true} // no flags -> read stdin
	if err := RunAttnHook(pa, strings.NewReader(payload)); err != nil {
		t.Fatalf("RunAttnHook(stdin): %v", err)
	}
	markers, _ := status.LoadAttnMarkers(time.Now())
	if got := markers[wantSession]; got != status.AttnWaiting {
		t.Fatalf("marker[%q] = %d, want AttnWaiting; all=%v", wantSession, got, markers)
	}
}
