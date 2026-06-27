package tui

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// userCommandCmd surfaces a launcher failure as a commandErrMsg (carrying the
// title) and a success as a plain focusedMsg.
func TestUserCommandCmdRouting(t *testing.T) {
	boom := errors.New("kitten: launch failed")
	failOpen := func(dir, title, runline string) error { return boom }
	okOpen := func(dir, title, runline string) error { return nil }

	switch msg := userCommandCmd("/g/repo", "lazygit", "lazygit", failOpen)().(type) {
	case commandErrMsg:
		if msg.title != "lazygit" || msg.err != boom {
			t.Errorf("commandErrMsg = %+v, want title lazygit / boom", msg)
		}
	default:
		t.Errorf("failure yielded %T, want commandErrMsg", msg)
	}

	if _, ok := userCommandCmd("/g/repo", "lazygit", "lazygit", okOpen)().(focusedMsg); !ok {
		t.Errorf("success should yield focusedMsg")
	}
}

// A commandErrMsg opens the error float; any subsequent keypress dismisses it.
func TestCommandErrFloatLifecycle(t *testing.T) {
	m := model{}

	updated, _ := m.Update(commandErrMsg{title: "lazygit", err: errors.New("boom")})
	m = updated.(model)
	if m.cmdErr == nil || m.cmdErr.title != "lazygit" || !strings.Contains(m.cmdErr.msg, "boom") {
		t.Fatalf("cmdErr = %+v, want it set from the message", m.cmdErr)
	}

	// While the float is up, an arbitrary key just dismisses it (no other action).
	updated, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	m = updated.(model)
	if m.cmdErr != nil {
		t.Errorf("cmdErr should be cleared after a keypress, got %+v", m.cmdErr)
	}
}

// holdOnError keeps a failing surface open with a labeled notice but lets a
// successful command close silently.
func TestHoldOnError(t *testing.T) {
	out, err := exec.Command("sh", "-c", holdOnError("true", "lazygit")).CombinedOutput()
	if err != nil {
		t.Errorf("success wrapper errored: %v (%s)", err, out)
	}
	if strings.Contains(string(out), "failed") {
		t.Errorf("success printed a failure notice: %q", out)
	}

	c := exec.Command("sh", "-c", holdOnError("(exit 3)", "lazygit"))
	c.Stdin = strings.NewReader("X") // the keypress the notice waits for
	out, _ = c.CombinedOutput()
	if !strings.Contains(string(out), "lazygit failed (exit 3)") {
		t.Errorf("failure notice missing/wrong: %q", out)
	}
}

// detachProcessCmd floats an early non-zero exit but treats a still-running
// process as launched.
func TestDetachProcessCmd(t *testing.T) {
	cases := []struct {
		name    string
		runline string
		wantErr bool
	}{
		{"fast failure floats", "exit 9", true},
		{"fast success is launched", "true", false},
		{"still running is launched", "sleep 1", false},
	}
	for _, tc := range cases {
		switch m := detachProcessCmd(t.TempDir(), "Editor", tc.runline)().(type) {
		case commandErrMsg:
			if !tc.wantErr {
				t.Errorf("%s: got commandErrMsg, want launched", tc.name)
			}
			if m.title != "Editor" {
				t.Errorf("%s: title = %q, want Editor", tc.name, m.title)
			}
		case focusedMsg:
			if tc.wantErr {
				t.Errorf("%s: got focusedMsg, want commandErrMsg", tc.name)
			}
		default:
			t.Errorf("%s: unexpected msg %T", tc.name, m)
		}
	}
}
