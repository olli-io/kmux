package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// bannerGlyph is a distinctive slice of the wordmark banner (the "m" crossbar in
// row 3) — a contiguous run within one line, so it survives lipgloss's per-line
// styling and centering and can stand in for "the banner rendered".
const bannerGlyph = "███▄███▄"

// TestSplashViewBeforeSize checks the splash renders its wordmark and spinner
// even before a WindowSizeMsg arrives (width/height still 0), so the very first
// frame is never blank.
func TestSplashViewBeforeSize(t *testing.T) {
	out := splashModel{}.View()
	if !strings.Contains(out, bannerGlyph) {
		t.Errorf("View() = %q, want it to contain the wordmark banner", out)
	}
	if !strings.Contains(out, "launching") {
		t.Errorf("View() = %q, want it to contain %q", out, "launching")
	}
	if !strings.Contains(out, splashFrames[0]) {
		t.Errorf("View() = %q, want it to contain the first spinner frame %q", out, splashFrames[0])
	}
}

// TestSplashViewCentered checks that once sized, the splash places its content in
// a block matching the terminal dimensions (lipgloss.Place pads to width/height).
func TestSplashViewCentered(t *testing.T) {
	m, _ := splashModel{}.Update(tea.WindowSizeMsg{Width: 40, Height: 9})
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 9 {
		t.Errorf("centered View() has %d lines, want 9 (the terminal height)", len(lines))
	}
	if !strings.Contains(m.View(), bannerGlyph) {
		t.Errorf("centered View() = %q, want it to contain the wordmark", m.View())
	}
}

// TestSplashTickAdvancesFrame checks a tick advances the spinner frame, driving
// the animation.
func TestSplashTickAdvancesFrame(t *testing.T) {
	m, cmd := splashModel{}.Update(splashTickMsg{})
	if m.(splashModel).frame != 1 {
		t.Errorf("frame after one tick = %d, want 1", m.(splashModel).frame)
	}
	if cmd == nil {
		t.Error("tick returned a nil cmd, want the next tick scheduled")
	}
}

// TestSplashQuitKeys checks q and ctrl+c quit the splash when it is run
// standalone; any other key is ignored (the dashboard, not the user, dismisses
// the real overlay by closing its tab).
func TestSplashQuitKeys(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
		{Type: tea.KeyCtrlC},
	} {
		if _, cmd := (splashModel{}).Update(key); cmd == nil {
			t.Errorf("key %v returned nil cmd, want tea.Quit", key)
		}
	}
	if _, cmd := (splashModel{}).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}); cmd != nil {
		t.Error("key 'x' returned a non-nil cmd, want it ignored")
	}
}
