package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/olli-io/kmux/internal/config"
)

// keyHint is one row in the bottom keybind panel: the key(s) and what they do.
type keyHint struct{ key, desc string }

// maxConflictLines caps how many keybinding-conflict lines the Keys panel shows,
// so a badly broken config can't grow the footer without bound.
const maxConflictLines = 6

// keyLabel renders a resolved key for the footer, swapping the names that don't
// read well literally (enter, arrows, space) for glyphs or words. An empty key
// (an action with no binding) renders empty.
func keyLabel(key string) string {
	switch key {
	case "":
		return ""
	case "enter":
		return "↵"
	case "up":
		return "↑"
	case "down":
		return "↓"
	case "left":
		return "←"
	case "right":
		return "→"
	case " ":
		return "space"
	}
	return key
}

// helpHints returns the keybind hints for the focused section, labeled from the
// resolved keybindings (m.keys): the section's action keys, then the
// user-configured commands that apply to the panel (e.g. editor, lazygit), then
// quit. The panel-focus digits 1/2 are documented by the panel titles instead, and
// the arrow-alias actions are omitted as conventional.
func (m model) helpHints(focused section) []keyHint {
	kb := func(action string) string { return keyLabel(m.keys[action]) }
	pair := func(a, b string) string { return kb(a) + "/" + kb(b) }

	move := keyHint{pair(config.ActionNextItem, config.ActionPrevItem), "Move"}
	switchPanel := keyHint{pair(config.ActionPrevPanel, config.ActionNextPanel), "Switch panel"}

	var hints []keyHint
	switch focused {
	case sectionSessions:
		hints = []keyHint{
			move,
			switchPanel,
			{kb(config.ActionCreateOrAttachAgent), "Focus pane"},
			{kb(config.ActionFullscreenAgent), "Fullscreen agent"},
			{kb(config.ActionDetachAgent), "Detach"},
			{kb(config.ActionKillAgent), "Kill session"},
		}
	default:
		hints = []keyHint{
			move,
			switchPanel,
			{kb(config.ActionCreateOrAttachAgent), "Launch agent"},
			{pair(config.ActionCreateOrFocusClaude, config.ActionCreateOrFocusOpencode), "Launch claude/opencode"},
			{kb(config.ActionFullscreenAgent), "Fullscreen agent"},
			{kb(config.ActionLaunchKmuxInProject), "Kmux project in tab"},
			{kb(config.ActionKillAgent), "Kill session"},
		}
	}
	panel := panelName(focused)
	for _, c := range m.commands {
		if !c.Matches(panel) {
			continue
		}
		label := c.Title
		if label == "" {
			label = c.Cmd
		}
		hints = append(hints, keyHint{keyLabel(c.Key), label})
	}
	return append(hints, keyHint{kb(config.ActionQuit), "Quit"})
}

// conflictLines renders the keybinding-conflict report (m.conflicts) as
// error-styled body lines, capped at maxConflictLines with a "+N more" summary
// when there are more. It returns nil when there are no conflicts.
func (m model) conflictLines() []string {
	if len(m.conflicts) == 0 {
		return nil
	}
	if len(m.conflicts) <= maxConflictLines {
		lines := make([]string, len(m.conflicts))
		for i, c := range m.conflicts {
			lines[i] = errStyle.Render("! " + c)
		}
		return lines
	}
	lines := make([]string, maxConflictLines)
	for i := 0; i < maxConflictLines-1; i++ {
		lines[i] = errStyle.Render("! " + m.conflicts[i])
	}
	more := len(m.conflicts) - (maxConflictLines - 1)
	lines[maxConflictLines-1] = errStyle.Render(fmt.Sprintf("! … %d more conflict(s)", more))
	return lines
}

// renderPrompt formats the agent picker's body lines: one row per agent kind,
// the selected one highlighted full-width, plus a hint row. width is the panel's
// inner width (for the selection highlight).
func renderPrompt(p *agentPrompt, width int) []string {
	lines := make([]string, 0, len(promptOptions)+2)
	for i, o := range promptOptions {
		marker := "  "
		if i == p.cursor {
			marker = chevronStyle.Render(chevronCollapsed + " ")
		}
		line := marker + o.style.Render(o.label)
		if i == p.cursor {
			line = selectLine(line, width)
		}
		lines = append(lines, line)
	}
	lines = append(lines, "", promptHint())
	return lines
}

// promptHint renders the picker's key-hint footer line.
func promptHint() string {
	return keyStyle.Render("↵") + dimStyle.Render(" launch  ") + keyStyle.Render("esc") + dimStyle.Render(" cancel")
}

// promptBox renders the floating agent picker: a focused, rounded box titled with
// the project being launched, one row per agent kind, and the key hint. It is
// sized to its widest line but capped at maxInner inner columns so it never
// overflows the screen.
func promptBox(p *agentPrompt, maxInner int) string {
	title := "Launch agent · " + p.title
	inner := lipgloss.Width(title)
	for _, o := range promptOptions {
		if w := lipgloss.Width(chevronCollapsed + " " + o.label); w > inner {
			inner = w
		}
	}
	if w := lipgloss.Width(promptHint()); w > inner {
		inner = w
	}
	inner += 2 // breathing room around the content
	if inner > maxInner {
		inner = maxInner
	}
	if inner < 1 {
		inner = 1
	}
	body := renderPrompt(p, inner)
	return panel(title, body, inner+2, len(body)+2, true)
}

// helpHeight is the body-row count of the keybind panel: the tallest of the
// sections' hint lists or the (capped) conflict report, so the panel stays a
// constant height, the dashboard doesn't jump when switching panels, and a
// conflict report is never clipped (shorter content pads with blank rows).
func (m model) helpHeight() int {
	hints := max(
		len(m.helpHints(sectionSessions)),
		len(m.helpHints(sectionProjects)),
	)
	return max(hints, min(len(m.conflicts), maxConflictLines))
}

// renderHelp formats the keybind hints into panel body lines, keys left-aligned
// to a common width with dim descriptions. While the config has keybinding
// conflicts the hints are replaced by the error-styled conflict report, so a
// broken config is visible rather than silently mis-behaving.
func (m model) renderHelp(focused section) []string {
	if lines := m.conflictLines(); lines != nil {
		return lines
	}
	hints := m.helpHints(focused)
	kw := 0
	for _, h := range hints {
		if w := lipgloss.Width(h.key); w > kw {
			kw = w
		}
	}
	lines := make([]string, len(hints))
	for i, h := range hints {
		pad := strings.Repeat(" ", kw-lipgloss.Width(h.key)+2)
		lines[i] = keyStyle.Render(h.key) + pad + dimStyle.Render(h.desc)
	}
	return lines
}

func (m model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}

	rows := m.rows()
	cursor := m.cursor
	if cursor >= len(rows) {
		cursor = len(rows) - 1
	}
	if cursor < 0 {
		cursor = 0
	}
	focused := sectionSessions
	if r := rowAt(rows, cursor); r != nil {
		focused = r.section
	}

	contentWidth := m.width - 2 // inside the vertical borders
	if contentWidth < 1 {
		contentWidth = 1
	}

	// Track the selected row's position within its panel so the picker overlay can
	// anchor to it: selPanel is which panel holds the cursor, selPanelIdx its index
	// among that panel's body lines (-1 if the cursor isn't on a real row), selDepth
	// its tree indentation.
	var sLines, pLines []string
	selPanel, selPanelIdx, selDepth := sectionSessions, -1, 0
	for i, r := range rows {
		line := renderRow(r, i == cursor, m.collapsed, contentWidth)
		switch r.section {
		case sectionSessions:
			if i == cursor {
				selPanel, selPanelIdx, selDepth = sectionSessions, len(sLines), r.depth
			}
			sLines = append(sLines, line)
		default:
			if i == cursor {
				selPanel, selPanelIdx, selDepth = sectionProjects, len(pLines), r.depth
			}
			pLines = append(pLines, line)
		}
	}
	if len(sLines) == 0 {
		sLines = []string{dimStyle.Render("no agent sessions")}
	}
	if len(pLines) == 0 {
		pLines = []string{dimStyle.Render("no projects found")}
	}
	if m.lastErr != "" {
		pLines = append(pLines, "", errStyle.Render("! "+m.lastErr))
	}

	// Reserve the bottom rows for the keybind panel, but drop it on very short
	// terminals so the lists keep usable height.
	hh := m.helpHeight() + 2
	avail := m.height - hh
	if avail < 6 { // two list panels need 3 rows each; drop help before starving them
		hh, avail = 0, m.height
	}

	// Split the available height evenly between the two list panels (min 3 each);
	// Projects takes any odd leftover row.
	sh := avail / 2
	if sh < 3 {
		sh = 3
	}
	ph := avail - sh
	if ph < 3 {
		ph = 3
	}

	projTitle := "[1]─Projects"
	if m.scopeName != "" {
		projTitle = "[1]─Project · " + m.scopeName
	}
	sessions := panel("[2]─Sessions", sLines, m.width, sh, focused == sectionSessions)
	projects := panel(projTitle, pLines, m.width, ph, focused == sectionProjects)
	parts := []string{projects, sessions}
	if hh > 0 {
		parts = append(parts, panel("Keys", m.renderHelp(focused), m.width, hh, false))
	}
	frame := lipgloss.JoinVertical(lipgloss.Left, parts...)

	// The agent picker floats just under the selected row rather than docking in a
	// panel, so launching reads as an action on that row. The panels stack Projects
	// [0, ph), Sessions after; +1 skips each panel's top border.
	if m.prompt != nil && selPanelIdx >= 0 {
		base := 0
		if selPanel == sectionSessions {
			base = ph
		}
		frame = m.overlayPrompt(frame, base+1+selPanelIdx, selDepth)
	}
	// Command-error float sits on top, centered.
	if m.cmdErr != nil {
		frame = m.overlayError(frame)
	}
	return frame
}

// overlayPrompt composites the floating agent picker onto the rendered frame,
// anchored just below the selected row (selY) at its indentation. It flips above
// the row when there isn't room below, and clamps horizontally so the box stays
// on screen.
func (m model) overlayPrompt(frame string, selY, depth int) string {
	box := promptBox(m.prompt, m.width-2)
	boxLines := strings.Split(box, "\n")
	boxW, boxH := lipgloss.Width(boxLines[0]), len(boxLines)

	y := selY + 1
	if y+boxH > m.height {
		if up := selY - boxH; up >= 0 {
			y = up // not enough room below: sit above the row instead
		}
	}
	x := 2 + depth*2 // align under the row's label (left border + indent)
	if x+boxW > m.width {
		x = m.width - boxW
	}
	if x < 0 {
		x = 0
	}

	lines := strings.Split(frame, "\n")
	for i, bl := range boxLines {
		if row := y + i; row >= 0 && row < len(lines) {
			lines[row] = overlayLine(lines[row], bl, x)
		}
	}
	return strings.Join(lines, "\n")
}

// errorBox renders the command-error float: a red-bordered box titled with the
// failed command, the wrapped message, and a dismiss hint. maxInner/maxHeight cap
// its size; overflow message lines are dropped with an ellipsis.
func errorBox(e *commandError, maxInner, maxHeight int) string {
	bs := lipgloss.NewStyle().Foreground(errColor)
	ts := bs.Bold(true)

	title := "Command failed"
	if e.title != "" {
		title += " · " + e.title
	}
	hint := keyStyle.Render("esc") + dimStyle.Render(" dismiss")

	inner := max(lipgloss.Width(title), lipgloss.Width(hint), 40)
	if inner > maxInner {
		inner = maxInner
	}
	if inner < 1 {
		inner = 1
	}

	msg := strings.Split(ansi.Wrap(strings.TrimSpace(e.msg), inner, ""), "\n")
	// Cap height; ellipsize the last line when the message overflows.
	if maxBody := maxHeight - 4; maxBody >= 1 && len(msg) > maxBody {
		msg = msg[:maxBody]
		msg[maxBody-1] = ansi.Truncate(msg[maxBody-1], inner-1, "…")
	}
	body := append(msg, "", hint)
	return box(title, body, inner+2, len(body)+2, bs, ts)
}

// overlayError composites the command-error float centered on the frame.
func (m model) overlayError(frame string) string {
	box := errorBox(m.cmdErr, m.width-2, m.height-2)
	boxLines := strings.Split(box, "\n")
	boxW, boxH := lipgloss.Width(boxLines[0]), len(boxLines)

	x := max((m.width-boxW)/2, 0)
	y := max((m.height-boxH)/2, 0)

	lines := strings.Split(frame, "\n")
	for i, bl := range boxLines {
		if r := y + i; r >= 0 && r < len(lines) {
			lines[r] = overlayLine(lines[r], bl, x)
		}
	}
	return strings.Join(lines, "\n")
}

// overlayLine splices over onto base starting at display column x, ANSI-aware,
// leaving the base on either side intact. Resets bracket the inserted span so the
// box's colors don't bleed into the surrounding frame and vice versa.
func overlayLine(base, over string, x int) string {
	left := ansi.Truncate(base, x, "")
	if w := lipgloss.Width(left); w < x {
		left += strings.Repeat(" ", x-w)
	}
	right := ansi.TruncateLeft(base, x+lipgloss.Width(over), "")
	return left + "\x1b[0m" + over + "\x1b[0m" + right
}

// renderRow renders a tree row: indent, chevron, optional mark/badge, label.
// Selected rows get a full-width background highlight.
func renderRow(r row, selected bool, collapsed map[string]bool, width int) string {
	var b strings.Builder
	b.WriteString(strings.Repeat("  ", r.depth))
	if r.collapsible {
		if collapsed[r.key] {
			b.WriteString(chevronStyle.Render(chevronCollapsed + " "))
		} else {
			b.WriteString(chevronStyle.Render(chevronExpanded + " "))
		}
	} else if r.section != sectionSessions {
		// Session leaves sit flush under their project header's chevron; other
		// panels keep the chevron-width placeholder so labels align.
		b.WriteString("  ")
	}
	if r.mark != "" {
		b.WriteString(r.mark)
		b.WriteString(" ")
	}
	b.WriteString(r.label)
	if r.badge != "" {
		b.WriteString(" ")
		b.WriteString(r.badge)
	}

	line := b.String()
	if selected {
		return selectLine(line, width)
	}
	return line
}

// selectLine paints a composed row line with the uniform selection background.
// Inner styled segments (chevrons, marks, badges, dim branch text) each end with
// an ANSI reset that also clears the background, which would punch
// default-colored gaps into the highlight; re-emitting the background after every
// reset keeps the bar one solid color while preserving the inner foregrounds.
//
// The line is truncated/padded to exactly width before styling: lipgloss's
// .Width() would wrap an over-long line onto extra rows (unlike the truncating
// padCell used for unselected rows), breaking the layout, so we size the line
// ourselves and render the background without it.
func selectLine(line string, width int) string {
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+selectedOpenSeq)
	return selectedStyle.Render(padCell(line, width))
}

// panel draws a rounded, titled box (lazygit-style: title in the top border,
// border color reflecting focus) sized to width x height, filling/clipping body
// lines to fit. width and height include the border.
func panel(title string, body []string, width, height int, focused bool) string {
	// Focused panels use the default text color; idle panels are dimmed grey.
	bs := lipgloss.NewStyle()
	ts := lipgloss.NewStyle().Bold(true)
	if !focused {
		bs = bs.Foreground(borderIdle)
		ts = ts.Foreground(borderIdle)
	}
	return box(title, body, width, height, bs, ts)
}

// box draws a rounded, titled frame with border style bs and title style ts
// (panel and the error float differ only in color). width/height include the
// border; body lines are clipped/padded to the inner width.
func box(title string, body []string, width, height int, bs, ts lipgloss.Style) string {
	inner := width - 2 // columns between the vertical borders
	if inner < 1 {
		inner = 1
	}

	// Top border with the title embedded: ╭─title───╮
	if maxTitle := inner - 2; maxTitle >= 1 && lipgloss.Width(title) > maxTitle {
		title = ansi.Truncate(title, maxTitle, "…")
	}
	fill := inner - lipgloss.Width(title) - 1 // leading "─" before the title
	if fill < 0 {
		fill = 0
	}
	top := bs.Render("╭─") + ts.Render(title) + bs.Render(strings.Repeat("─", fill)+"╮")

	out := make([]string, 0, height)
	out = append(out, top)
	for i := 0; i < height-2; i++ {
		var raw string
		if i < len(body) {
			raw = body[i]
		}
		out = append(out, bs.Render("│")+padCell(raw, inner)+bs.Render("│"))
	}
	out = append(out, bs.Render("╰"+strings.Repeat("─", inner)+"╯"))
	return strings.Join(out, "\n")
}

// padCell pads (or clips) s to exactly w display columns, ANSI-aware.
func padCell(s string, w int) string {
	if sw := lipgloss.Width(s); sw > w {
		return ansi.Truncate(s, w, "")
	} else {
		return s + strings.Repeat(" ", w-sw)
	}
}
