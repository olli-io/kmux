package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/olli-io/kmux/internal/agent"
	"github.com/olli-io/kmux/internal/project"
	"github.com/olli-io/kmux/internal/status"
)

var (
	clStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))  // claude
	ocStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("213")) // opencode (pink)
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errColor    = lipgloss.Color("9") // error red (border, glyphs, text)
	errStyle    = lipgloss.NewStyle().Foreground(errColor)
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	activeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // project with a live session
	folderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // folder glyph (blue)

	chevronStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	selectedStyle = lipgloss.NewStyle().Background(lipgloss.Color("238"))

	// selectedOpenSeq is selectedStyle's background as a bare SGR open sequence,
	// derived (not hardcoded) so it tracks the color above across terminal color
	// profiles. selectLine re-emits it after inner ANSI resets.
	selectedOpenSeq = func() string {
		const sentinel = "\x00"
		if open, _, ok := strings.Cut(selectedStyle.Render(sentinel), sentinel); ok {
			return open
		}
		return ""
	}()

	keyStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // keybind hint (yellow)
	syncStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // ahead/behind-origin arrow (yellow)

	borderIdle = lipgloss.Color("240") // unfocused panel border (grey)
)

// rowDeco renders styled row labels/badges. It keeps lipgloss styling out of the
// pure tree-building code in tree.go. spinner is the current busy-animation frame,
// advanced on each spinner tick.
type rowDeco struct{ spinner int }

func (d rowDeco) session(name string, depth int, st status.AttentionState, attached, detached bool) row {
	// The leading mark is the attention glyph (what the agent is doing); the
	// attach state (A/D) rides on the agent badge instead.
	return row{
		section: sectionSessions,
		depth:   depth,
		label:   sessionLabel(name),
		badge:   agentBadge(name, attached, detached),
		mark:    attentionGlyph(st, d.spinner),
		session: name,
	}
}

// sessionLabel is the text a session row displays, mirroring the Projects panel:
// a main-worktree session shows the project name, a linked-worktree session shows
// the worktree name alone (the agent kind shows as a trailing badge).
func sessionLabel(name string) string {
	if wt := agent.WorktreeName(name); wt != "" {
		return wt
	}
	return agent.ProjectName(name)
}

// agentBadge renders the styled agent-kind badge for a session name ("CC" for
// Claude, "OC" for OpenCode), prefixed with its attach state: a green "A" when
// attached (live pane) or a red "D" when detached (tmux alive, pane closed), so
// the badge reads "A‧CC"/"D‧CC"/"CC" or "A‧OC"/"D‧OC"/"OC" (‧ is U+2027, matching
// the session-name separator). The prefix keeps its own color (green/red)
// distinct from the agent color. Returns "" for a non-agent name.
func agentBadge(name string, attached, detached bool) string {
	prefix := ""
	switch {
	case attached:
		prefix = okStyle.Render("A") + dimStyle.Render("‧")
	case detached:
		prefix = errStyle.Render("D") + dimStyle.Render("‧")
	}
	switch agent.AgentKind(name) {
	case "claude":
		return prefix + clStyle.Render("CC")
	case "opencode":
		return prefix + ocStyle.Render("OC")
	}
	return ""
}

// attentionGlyph returns the styled status glyph for an attention state, shown at
// the head of a session row. For the busy state it returns the spinner frame
// selected by frame, producing a rotating braille animation across ticks.
func attentionGlyph(st status.AttentionState, frame int) string {
	switch st {
	case status.AttnPermission:
		return errStyle.Render("!")
	case status.AttnWaiting:
		return okStyle.Render("✓")
	case status.AttnBusy:
		return dimStyle.Render(spinnerFrames[frame%len(spinnerFrames)])
	default: // status.AttnUnknown
		return dimStyle.Render("·")
	}
}

// branchGlyph is the git-branch symbol () shown before a branch name.
const branchGlyph = ""

// chevronCollapsed / chevronExpanded are the nerdfont chevrons shown before a
// collapsible header: right-pointing when collapsed, down-pointing when expanded.
const (
	chevronCollapsed = "" // nf-fa-chevron_right
	chevronExpanded  = "" // nf-fa-chevron_down
)

// folderGlyph / folderOpenGlyph are the folder symbols shown before a
// multi-worktree project header: a filled folder when collapsed, the outlined
// open folder when expanded.
const (
	folderGlyph     = "" // nf-fa-folder
	folderOpenGlyph = "" // nf-fa-folder_open_o
)

// branchSuffix renders the dim " <glyph> <branch>" tail, or "" when no branch.
func branchSuffix(branch string) string {
	if branch == "" {
		return ""
	}
	return dimStyle.Render(" " + branchGlyph + " " + branch)
}

// gitStatus is the per-checkout git state a row's leading mark renders: whether
// the working tree is dirty and how it sits relative to its upstream (origin).
type gitStatus struct {
	dirty    bool
	ahead    int
	behind   int
	upstream bool
}

func worktreeGit(w project.Worktree) gitStatus {
	return gitStatus{dirty: w.Dirty, ahead: w.Ahead, behind: w.Behind, upstream: w.Upstream}
}

func projectGit(p project.Project) gitStatus {
	return gitStatus{dirty: p.Dirty, ahead: p.Ahead, behind: p.Behind, upstream: p.Upstream}
}

// branchLabel labels a worktree/project leaf: a leading git-status mark (in place
// of a plain git glyph), the project name (colored by live session state), and the
// dim branch tail.
func branchLabel(name, branch string, live liveState, gs gitStatus) string {
	return gitStatusGlyph(gs) + " " + projectName(name, live) + branchSuffix(branch)
}

// gitStatusGlyph marks a checkout's git state at the head of its row. A dirty
// working tree (staged or unstaged changes) always shows a red "M", as before.
// Otherwise the mark reports how the branch sits against its upstream (origin):
// a yellow ↑ when ahead, ↓ when behind, a red ⇕ when diverged (both), and a
// green "=" when it matches. A clean checkout with no upstream configured is
// local-only and shows a dim "L" — distinct from "=" so a branch never pushed
// doesn't masquerade as one in sync with origin.
func gitStatusGlyph(gs gitStatus) string {
	switch {
	case gs.dirty:
		return errStyle.Render("M")
	case !gs.upstream:
		return dimStyle.Render("L")
	case gs.ahead > 0 && gs.behind > 0:
		return errStyle.Render("⇕")
	case gs.ahead > 0:
		return syncStyle.Render("↑")
	case gs.behind > 0:
		return syncStyle.Render("↓")
	default:
		return okStyle.Render("=")
	}
}

// projectName renders a project/worktree name, colored by its live session state:
// green when a session has a live pane, red when its only session is detached
// (mirroring the Sessions panel's "D"), uncolored when it has no session.
func projectName(name string, live liveState) string {
	switch live {
	case liveAttached:
		return activeStyle.Render(name)
	case liveDetached:
		return errStyle.Render(name)
	}
	return name
}

// projectLeaf labels a single-worktree project (name + branch).
func (rowDeco) projectLeaf(p project.Project, live liveState) string {
	return branchLabel(p.Name, p.Branch, live, projectGit(p))
}

// projectFolder labels a multi-worktree project header (folder glyph + name).
// The glyph is the open variant when expanded, the closed variant otherwise.
// The branch moves onto the main-worktree child row inside the expanded list.
func (rowDeco) projectFolder(p project.Project, open bool, live liveState) string {
	glyph := folderGlyph
	if open {
		glyph = folderOpenGlyph
	}
	label := folderStyle.Render(glyph) + " " + projectName(p.Name, live)
	// When collapsed the worktree rows (with their own marks) are hidden, so the
	// header carries an aggregate status rolled up across the main worktree and
	// every linked one. Expanded, the per-row marks below say it instead.
	if !open {
		label += " " + gitStatusGlyph(projectStatus(p))
	}
	return label
}

// projectStatus rolls a project's main worktree and every linked worktree into
// one git status for the collapsed-folder mark: dirty if any is dirty, ahead/
// behind if any is, and upstream-tracked if any has an upstream. The rollup
// favors action — a single behind worktree shows ↓ for the whole folder — so a
// collapsed project never hides changes that an expanded one would surface.
func projectStatus(p project.Project) gitStatus {
	gs := projectGit(p)
	for _, w := range p.Worktrees {
		gs.dirty = gs.dirty || w.Dirty
		gs.ahead += w.Ahead
		gs.behind += w.Behind
		gs.upstream = gs.upstream || w.Upstream
	}
	return gs
}

// sessionFolder labels a multi-session project header in the Sessions panel,
// mirroring projectFolder: a folder glyph (open when expanded) plus the project
// name. The per-session attention/agent state rides on the child rows, so the
// header itself stays an uncolored grouping node.
func (rowDeco) sessionFolder(name string, open bool) string {
	glyph := folderGlyph
	if open {
		glyph = folderOpenGlyph
	}
	return folderStyle.Render(glyph) + " " + name
}

// mainWorktree labels the main worktree row (repo name + branch), listed first
// inside an expanded project folder.
func (rowDeco) mainWorktree(p project.Project, live liveState) string {
	return branchLabel(p.Name, p.Branch, live, projectGit(p))
}

func (rowDeco) worktree(w project.Worktree, live liveState) string {
	return branchLabel(w.Name, w.Branch, live, worktreeGit(w))
}
