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
	// derived so it tracks the color above. selectLine re-emits it after inner resets.
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

// rowDeco renders styled row labels/badges, keeping lipgloss out of the tree
// building in tree.go.
type rowDeco struct{ spinner int }

// orphanGlyph marks an orphaned (no-repo) session, mirroring the mark
// agent.OrphanSession bakes into the session name.
const orphanGlyph = "∅" // U+2205 EMPTY SET

func (d rowDeco) session(name string, depth int, st status.AttentionState, attached, detached bool) row {
	// The leading mark is the attention glyph; the attach state (A/D) rides on the
	// agent badge. An orphaned session shows a dim ∅ between label and badge.
	label := sessionLabel(name)
	if agent.IsOrphan(name) {
		label += " " + dimStyle.Render(orphanGlyph)
	}
	return row{
		section: sectionSessions,
		depth:   depth,
		label:   label,
		badge:   agentBadge(name, attached, detached),
		mark:    attentionGlyph(st, d.spinner),
		session: name,
	}
}

// sessionLabel mirrors the Projects panel: a main-worktree session shows the
// project name, a linked-worktree session shows the worktree name alone.
func sessionLabel(name string) string {
	if wt := agent.WorktreeName(name); wt != "" {
		return wt
	}
	return agent.ProjectName(name)
}

// agentBadge renders the styled agent-kind badge ("CC"/"OC") prefixed with its
// attach state: green "A" attached, red "D" detached. ‧ (U+2027) is a cosmetic
// separator. Returns "" for a non-agent name.
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

// attentionGlyph returns the styled status glyph for an attention state. Busy
// returns the spinner frame at frame, animating across ticks. AttnPermission (blocked
// on a prompt awaiting an answer) is the urgent red "!"; AttnWaiting (turn finished,
// nothing pending) is a calm green "✓".
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

// chevronCollapsed / chevronExpanded precede a collapsible header.
const (
	chevronCollapsed = "" // nf-fa-chevron_right
	chevronExpanded  = "" // nf-fa-chevron_down
)

// folderGlyph / folderOpenGlyph precede a multi-worktree project header (open
// variant when expanded).
const (
	folderGlyph     = "" // nf-fa-folder
	folderOpenGlyph = "" // nf-fa-folder_open_o
)

func branchSuffix(branch string) string {
	if branch == "" {
		return ""
	}
	return dimStyle.Render(" " + branchGlyph + " " + branch)
}

// gitStatus is the per-checkout git state a row's leading mark renders.
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

func branchLabel(name, branch string, live liveState, gs gitStatus) string {
	return gitStatusGlyph(gs) + " " + projectName(name, live) + branchSuffix(branch)
}

// gitStatusGlyph marks a checkout's git state: red "M" dirty, else ↑/↓/⇕ against
// upstream or green "=" in sync. A clean checkout with no upstream shows a dim
// "L", distinct from "=" so a never-pushed branch isn't read as in sync.
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

// projectName colors a project/worktree name by live session state: green with a
// live pane, red when its only session is detached, uncolored with no session.
func projectName(name string, live liveState) string {
	switch live {
	case liveAttached:
		return activeStyle.Render(name)
	case liveDetached:
		return errStyle.Render(name)
	}
	return name
}

func (rowDeco) projectLeaf(p project.Project, live liveState) string {
	return branchLabel(p.Name, p.Branch, live, projectGit(p))
}

// projectFolder labels a multi-worktree project header, open glyph when expanded.
// The branch moves onto the main-worktree child row inside the expanded list.
func (rowDeco) projectFolder(p project.Project, open bool, live liveState) string {
	glyph := folderGlyph
	if open {
		glyph = folderOpenGlyph
	}
	label := folderStyle.Render(glyph) + " " + projectName(p.Name, live)
	// Collapsed, the header carries an aggregate status rolled up across all
	// worktrees; expanded, the per-row marks say it instead.
	if !open {
		label += " " + gitStatusGlyph(projectStatus(p))
	}
	return label
}

// projectStatus rolls all of a project's worktrees into one status for the
// collapsed-folder mark. The rollup favors action so a collapsed project never
// hides changes an expanded one would surface.
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

// sessionFolder labels a multi-session project header, mirroring projectFolder;
// per-session state rides on the child rows.
func (rowDeco) sessionFolder(name string, open bool) string {
	glyph := folderGlyph
	if open {
		glyph = folderOpenGlyph
	}
	return folderStyle.Render(glyph) + " " + name
}

func (rowDeco) mainWorktree(p project.Project, live liveState) string {
	return branchLabel(p.Name, p.Branch, live, projectGit(p))
}

func (rowDeco) worktree(w project.Worktree, live liveState) string {
	return branchLabel(w.Name, w.Branch, live, worktreeGit(w))
}
