// Package grid shows agent panes in one tmux window: the dashboard pane on the
// left, then three columns of agent panes. tmux draws the grid. All state is in
// tmux pane options, so each pass starts from a tmux snapshot.
package grid

import (
	"fmt"
	"strings"
)

const (
	maxColumns  = 3    // agent columns to the right of the dashboard
	sidebarFrac = 0.16 // part of the window width for the dashboard
)

// Layout returns the tmux layout string for a window of w x h cells: dash, then
// the columns (each top to bottom), then the filler in the space of the columns
// that are not used. filler is "" when there is none. The leaf order is the order
// that tmux uses to assign panes: dash, columns, filler.
func Layout(w, h int, dash string, cols [][]string, filler string) string {
	if len(cols) == 0 && filler == "" {
		return withChecksum(leaf(w, h, 0, 0, dash))
	}
	sb := int(float64(w)*sidebarFrac + 0.5)
	// One separator column is to the right of the dashboard and of each slot but the last.
	usable := w - sb - maxColumns
	slot := usable / maxColumns

	parts := []string{leaf(sb, h, 0, 0, dash)}
	x := sb + 1
	last := len(cols) - 1
	for i, col := range cols {
		cw := slot
		if i == maxColumns-1 || (i == last && filler == "") {
			cw = w - x // the last cell gets the remainder
		}
		parts = append(parts, column(cw, h, x, col))
		x += cw + 1
	}
	if filler != "" {
		parts = append(parts, leaf(w-x, h, x, 0, filler))
	}
	return withChecksum(fmt.Sprintf("%dx%d,0,0{%s}", w, h, strings.Join(parts, ",")))
}

// column returns one column cell: a leaf, or a top-to-bottom split.
func column(w, h, x int, panes []string) string {
	if len(panes) == 1 {
		return leaf(w, h, x, 0, panes[0])
	}
	usable := h - (len(panes) - 1) // one separator row between panes
	each := usable / len(panes)
	parts := make([]string, len(panes))
	y := 0
	for i, p := range panes {
		ph := each
		if i == len(panes)-1 {
			ph = h - y
		}
		parts[i] = leaf(w, ph, x, y, p)
		y += ph + 1
	}
	return fmt.Sprintf("%dx%d,%d,0[%s]", w, h, x, strings.Join(parts, ","))
}

func leaf(w, h, x, y int, pane string) string {
	return fmt.Sprintf("%dx%d,%d,%d,%s", w, h, x, y, strings.TrimPrefix(pane, "%"))
}

// withChecksum adds the 16-bit checksum that tmux requires before a layout.
func withChecksum(body string) string {
	var c uint16
	for i := 0; i < len(body); i++ {
		c = (c >> 1) + ((c & 1) << 15)
		c += uint16(body[i])
	}
	return fmt.Sprintf("%04x,%s", c, body)
}
