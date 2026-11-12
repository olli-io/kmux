package kitty

import "testing"

// siblingFocused is a `kitten @ ls` fixture for the case the launch gate must let
// through: the dashboard's own tab is active and its OS window has focus, with the
// cursor in the agent pane next to the sidebar rather than the sidebar itself. Field
// shapes mirror real kitty output — every pane carries is_active (its tab's current
// window, true in each tab regardless of focus) and is_focused (the single pane
// holding keyboard focus anywhere in the instance).
const siblingFocused = `[
  {"id": 1, "tabs": [
    {"windows": [
      {"id": 1,  "is_active": false, "is_focused": false, "title": "[kmux][dashboard]", "columns": 40, "foreground_processes": [{"cmdline": ["kmux"]}]},
      {"id": 11, "is_active": true,  "is_focused": true,  "title": "[kmux][CC]~/git/a", "columns": 80, "foreground_processes": [{"cmdline": ["tmux", "attach"]}]}
    ]},
    {"windows": [
      {"id": 12, "is_active": true, "is_focused": false, "title": "lazygit", "columns": 80, "foreground_processes": [{"cmdline": ["lazygit"]}]}
    ]}
  ]},
  {"id": 2, "tabs": [
    {"windows": [
      {"id": 19, "is_active": true, "is_focused": false, "title": "shell", "columns": 80, "foreground_processes": [{"cmdline": ["zsh"]}]}
    ]}
  ]}
]`

// sidebarFocused is the same instance with the cursor moved back to the sidebar.
const sidebarFocused = `[
  {"id": 1, "tabs": [
    {"windows": [
      {"id": 1,  "is_active": true,  "is_focused": true,  "title": "[kmux][dashboard]", "columns": 40, "foreground_processes": [{"cmdline": ["kmux"]}]},
      {"id": 11, "is_active": false, "is_focused": false, "title": "[kmux][CC]~/git/a", "columns": 80, "foreground_processes": [{"cmdline": ["tmux", "attach"]}]}
    ]}
  ]}
]`

// otherTabFocused is the state the gate exists to catch: the same OS window is
// focused, but the cursor is in an unrelated tab, so bringing the dashboard's tab
// its new pane would yank the user out of what they are doing.
const otherTabFocused = `[
  {"id": 1, "tabs": [
    {"windows": [
      {"id": 1,  "is_active": true, "is_focused": false, "title": "[kmux][dashboard]", "columns": 40, "foreground_processes": [{"cmdline": ["kmux"]}]},
      {"id": 11, "is_active": false, "is_focused": false, "title": "[kmux][CC]~/git/a", "columns": 80, "foreground_processes": [{"cmdline": ["tmux", "attach"]}]}
    ]},
    {"windows": [
      {"id": 12, "is_active": true, "is_focused": true, "title": "lazygit", "columns": 120, "foreground_processes": [{"cmdline": ["lazygit"]}]}
    ]}
  ]}
]`

func fakeLS(t *testing.T, out string) {
	t.Helper()
	orig := runKitten
	t.Cleanup(func() { runKitten = orig })
	runKitten = func(_ ...string) (string, error) { return out, nil }
}

// TestSnapshotFocusIsTabLevel pins the semantics the launch gate depends on: focus
// is the sidebar's whole tab, not the sidebar pane alone. Working in an agent pane
// beside it still counts as focused — the panes kmux opens land in that same tab,
// which the user is already looking at — while a tab away does not.
func TestSnapshotFocusIsTabLevel(t *testing.T) {
	fakeLS(t, siblingFocused)

	for _, id := range []int{1, 11} {
		if _, _, focused, err := Snapshot(id); err != nil || !focused {
			t.Errorf("Snapshot(%d) focused = %v (err %v), want true — the cursor is in this tab", id, focused, err)
		}
	}

	fakeLS(t, sidebarFocused)
	if _, _, focused, err := Snapshot(1); err != nil || !focused {
		t.Fatalf("Snapshot(1) focused = %v (err %v), want true with the cursor on the sidebar", focused, err)
	}

	// Same OS window, different tab: the dashboard's tab is out of sight.
	fakeLS(t, otherTabFocused)
	if _, _, focused, err := Snapshot(1); err != nil || focused {
		t.Fatalf("Snapshot(1) focused = %v (err %v), want false — the cursor is a tab away", focused, err)
	}
}

// TestSnapshotFocusIgnoresTabActive guards the field that is easy to confuse with
// focus: every tab has an active window whether or not anything about it is
// focused. Reading is_active instead of is_focused would report the sidebar as
// focused from a background tab, or from another app entirely.
func TestSnapshotFocusIgnoresTabActive(t *testing.T) {
	fakeLS(t, siblingFocused)

	// Pane 12 is its tab's active window, and pane 19 is its own tab's and OS
	// window's — neither tab holds the cursor.
	for _, id := range []int{12, 19} {
		if _, _, focused, err := Snapshot(id); err != nil || focused {
			t.Errorf("Snapshot(%d) focused = %v, want false (active in its tab, not focused)", id, focused)
		}
	}
}

// TestSnapshotUnknownWindow: a sidebar id kitty no longer knows about (the window
// closed mid-poll) must read as unfocused rather than defaulting to focused, so a
// confused pass holds off instead of launching blind.
func TestSnapshotUnknownWindow(t *testing.T) {
	fakeLS(t, siblingFocused)

	if _, _, focused, err := Snapshot(999); err != nil || focused {
		t.Fatalf("Snapshot(999) focused = %v (err %v), want false for an unknown window", focused, err)
	}
}

// TestSnapshotSpansInstanceButScopesBlanks: the live-id set must cover every OS
// window so the manual-close prune sees all managed panes, while blank-pane
// adoption stays confined to the dashboard's own tab — a spare shell in another
// tab or window isn't the dashboard's to take over.
func TestSnapshotSpansInstanceButScopesBlanks(t *testing.T) {
	fakeLS(t, siblingFocused)

	live, blanks, _, err := Snapshot(1)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, id := range []int{1, 11, 12, 19} {
		if !live[id] {
			t.Errorf("live is missing window %d; the prune would close it as vanished", id)
		}
	}
	// Pane 19 is a bare shell, but it lives in another OS window entirely.
	if len(blanks) != 0 {
		t.Errorf("blanks = %v, want none — the only bare shell is outside the dashboard's tab", blanks)
	}
}
