package main

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// runSplash animates the launch overlay until the process is killed. It has no
// exit logic of its own: the dashboard closes this tab once its layout settles.
// Run standalone, q or ctrl+c quits it.
func runSplash() error {
	_, err := tea.NewProgram(splashModel{}).Run()
	return err
}

// splashWordmark is bold blue to match the dashboard's folder/claude accent.
var splashWordmark = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))

// "[kmux]" is drawn on a pixel grid, composited left-to-right with gaps, then
// rendered into Unicode braille cells (each packs a 2×4 pixel block). The reveal
// wipes the bitmap one pixel column at a time, then holds the finished wordmark.
const (
	splashRows   = 16 // pixel rows of the glyph grid
	splashGlyphW = 8  // pixel columns per glyph
	splashThick  = 2  // stroke thickness
)

// pixGrid is a single glyph's splashRows×splashGlyphW pixel bitmap.
type pixGrid [][]bool

func newGrid() pixGrid {
	g := make(pixGrid, splashRows)
	for y := range g {
		g[y] = make([]bool, splashGlyphW)
	}
	return g
}

// px lights one pixel, ignoring out-of-bounds coordinates.
func (g pixGrid) px(x, y int) {
	if y >= 0 && y < splashRows && x >= 0 && x < splashGlyphW {
		g[y][x] = true
	}
}

// block lights a splashThick×splashThick square with its top-left at (x,y).
func (g pixGrid) block(x, y int) {
	for i := 0; i < splashThick; i++ {
		for j := 0; j < splashThick; j++ {
			g.px(x+i, y+j)
		}
	}
}

// vline draws a thick vertical stroke down column x from row y0 to y1.
func (g pixGrid) vline(x, y0, y1 int) {
	for y := y0; y <= y1; y++ {
		for i := 0; i < splashThick; i++ {
			g.px(x+i, y)
		}
	}
}

// hline draws a thick horizontal stroke along row y from column x0 to x1.
func (g pixGrid) hline(y, x0, x1 int) {
	for x := x0; x <= x1; x++ {
		for i := 0; i < splashThick; i++ {
			g.px(x, y+i)
		}
	}
}

// diag draws a thick line from (x0,y0) to (x1,y1) via Bresenham.
func (g pixGrid) diag(x0, y0, x1, y1 int) {
	dx, dy := splashAbs(x1-x0), splashAbs(y1-y0)
	sx, sy := 1, 1
	if x0 >= x1 {
		sx = -1
	}
	if y0 >= y1 {
		sy = -1
	}
	err := dx - dy
	x, y := x0, y0
	for {
		g.block(x, y)
		if x == x1 && y == y1 {
			break
		}
		e2 := 2 * err
		if e2 > -dy {
			err -= dy
			x += sx
		}
		if e2 < dx {
			err += dx
			y += sy
		}
	}
}

func splashAbs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Glyph builders — each returns the pixel bitmap for one character of "[kmux]".
func glyphBracketL() pixGrid { g := newGrid(); g.vline(0, 0, 15); g.hline(0, 0, 3); g.hline(14, 0, 3); return g }
func glyphBracketR() pixGrid { g := newGrid(); g.vline(6, 0, 15); g.hline(0, 3, 6); g.hline(14, 3, 6); return g }
func glyphK() pixGrid        { g := newGrid(); g.vline(0, 0, 15); g.diag(2, 9, 6, 4); g.diag(2, 10, 6, 15); return g }
func glyphM() pixGrid        { g := newGrid(); g.hline(4, 0, 7); g.vline(0, 4, 15); g.vline(3, 4, 15); g.vline(6, 4, 15); return g }
func glyphU() pixGrid        { g := newGrid(); g.vline(0, 4, 15); g.vline(6, 4, 15); g.hline(14, 0, 7); return g }
func glyphX() pixGrid        { g := newGrid(); g.diag(0, 4, 6, 15); g.diag(6, 4, 0, 15); return g }

// splashGaps is the extra pixel spacing after each glyph; brackets sit flush
// against k/x while the inner letters breathe. len == len(seq)-1.
var splashGaps = []int{0, 2, 2, 2, 0}

// splashBitmap and its pixel width splashWidth are built once at startup.
var splashBitmap, splashWidth = buildSplashBitmap()

func buildSplashBitmap() ([][]bool, int) {
	seq := []pixGrid{glyphBracketL(), glyphK(), glyphM(), glyphU(), glyphX(), glyphBracketR()}
	w := len(seq) * splashGlyphW
	for _, gap := range splashGaps {
		w += gap
	}
	full := make([][]bool, splashRows)
	for y := range full {
		full[y] = make([]bool, w)
	}
	xoff := 0
	for i, g := range seq {
		for y := 0; y < splashRows; y++ {
			for x := 0; x < splashGlyphW; x++ {
				if g[y][x] {
					full[y][xoff+x] = true
				}
			}
		}
		xoff += splashGlyphW
		if i < len(splashGaps) {
			xoff += splashGaps[i]
		}
	}
	return full, w
}

// splashDotBit maps a pixel's (dy,dx) position within a braille cell to its bit in
// the Unicode braille pattern (U+2800 + bits).
var splashDotBit = [4][2]int{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}

// renderSplash draws the bitmap as braille, lighting only pixels in columns left
// of reveal — the left-to-right wipe.
func renderSplash(reveal int) string {
	cols := (splashWidth + 1) / 2
	rows := splashRows / 4
	var b strings.Builder
	for cr := 0; cr < rows; cr++ {
		for cc := 0; cc < cols; cc++ {
			bits := 0
			for dy := 0; dy < 4; dy++ {
				for dx := 0; dx < 2; dx++ {
					x, y := cc*2+dx, cr*4+dy
					if x < splashWidth && x < reveal && splashBitmap[y][x] {
						bits |= splashDotBit[dy][dx]
					}
				}
			}
			b.WriteRune(rune(0x2800 + bits))
		}
		if cr < rows-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// splashSweepDuration is the full wipe time; splashRevealInterval advances one
// pixel column per tick to hit that total.
const splashSweepDuration = 400 * time.Millisecond

var splashRevealInterval = splashSweepDuration / time.Duration(splashWidth)

type splashTickMsg struct{}

type splashModel struct {
	reveal        int // pixel columns exposed so far (splashWidth == held full frame)
	width, height int
}

func (splashModel) Init() tea.Cmd { return splashTick(splashRevealInterval) }

func splashTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return splashTickMsg{} })
}

func (m splashModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyPressMsg:
		if s := msg.String(); s == "ctrl+c" || s == "q" {
			return m, tea.Quit
		}
		return m, nil
	case splashTickMsg:
		if m.reveal >= splashWidth {
			// Fully drawn: stop ticking and hold the final frame.
			return m, nil
		}
		m.reveal++
		return m, splashTick(splashRevealInterval)
	}
	return m, nil
}

func (m splashModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m splashModel) render() string {
	block := splashWordmark.Render(renderSplash(m.reveal))
	if m.width == 0 || m.height == 0 {
		return block // no size yet: draw unplaced rather than collapse to empty
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, block)
}
