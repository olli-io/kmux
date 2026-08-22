package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// blankCell is the empty braille pattern (no dots lit) — an all-blank render is
// made entirely of these.
const blankCell = rune(0x2800)

// TestSplashBitmapBuilt checks the composited "[kmux]" bitmap has the expected
// pixel width (6 glyphs × splashGlyphW plus the inter-glyph gaps) and lights at
// least some pixels.
func TestSplashBitmapBuilt(t *testing.T) {
	wantWidth := 6*splashGlyphW + 0 + 2 + 2 + 2 + 0
	if splashWidth != wantWidth {
		t.Errorf("splashWidth = %d, want %d", splashWidth, wantWidth)
	}
	lit := false
	for _, row := range splashBitmap {
		for _, on := range row {
			lit = lit || on
		}
	}
	if !lit {
		t.Error("splashBitmap is entirely blank, want the wordmark drawn")
	}
}

// TestSplashRenderReveal checks the wipe: reveal 0 is all blank, and the fully
// revealed frame lights braille cells (differs from blank).
func TestSplashRenderReveal(t *testing.T) {
	empty := renderSplash(0)
	for _, r := range empty {
		if r != '\n' && r != blankCell {
			t.Fatalf("renderSplash(0) contains non-blank rune %q, want a blank frame", r)
		}
	}
	full := renderSplash(splashWidth)
	if full == empty {
		t.Error("renderSplash(splashWidth) equals the blank frame, want the wordmark drawn")
	}
	// A partial reveal should light fewer cells than the full one.
	if strings.Count(renderSplash(splashWidth/2), string(blankCell)) <=
		strings.Count(full, string(blankCell)) {
		t.Error("half reveal is not more blank than the full reveal, want a left-to-right wipe")
	}
}

// TestSplashViewBeforeSize checks the splash renders its matrix even before a
// WindowSizeMsg arrives (width/height still 0), so the first frame is never blank
// once anything is revealed.
func TestSplashViewBeforeSize(t *testing.T) {
	out := splashModel{reveal: splashWidth}.View()
	if !strings.Contains(out, renderSplash(splashWidth)) {
		t.Errorf("View() = %q, want it to contain the fully-revealed matrix", out)
	}
}

// TestSplashViewCentered checks that once sized, the splash places its content in
// a block matching the terminal dimensions (lipgloss.Place pads to width/height).
func TestSplashViewCentered(t *testing.T) {
	m, _ := splashModel{}.Update(tea.WindowSizeMsg{Width: 60, Height: 9})
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 9 {
		t.Errorf("centered View() has %d lines, want 9 (the terminal height)", len(lines))
	}
}

// TestSplashTickAdvancesReveal checks a tick advances the wipe one column,
// driving the animation.
func TestSplashTickAdvancesReveal(t *testing.T) {
	m, cmd := splashModel{}.Update(splashTickMsg{})
	if m.(splashModel).reveal != 1 {
		t.Errorf("reveal after one tick = %d, want 1", m.(splashModel).reveal)
	}
	if cmd == nil {
		t.Error("tick returned a nil cmd, want the next tick scheduled")
	}
}

// TestSplashTickHolds checks that once the reveal reaches the full width the
// animation stops: the frame is held and the ticker disarms.
func TestSplashTickHolds(t *testing.T) {
	held, cmd := splashModel{reveal: splashWidth}.Update(splashTickMsg{})
	if got := held.(splashModel).reveal; got != splashWidth {
		t.Fatalf("reveal at end of wipe = %d, want %d (held frame)", got, splashWidth)
	}
	if cmd != nil {
		t.Error("held frame scheduled another tick, want the ticker disarmed")
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
