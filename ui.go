package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/olli-io/kmux/internal/config"
	"github.com/olli-io/kmux/internal/kitty"
	"github.com/olli-io/kmux/internal/project"
	"github.com/olli-io/kmux/internal/tmux"
)

const pollInterval = 2 * time.Second

// spinnerInterval is how often the busy-session animation advances a frame.
// Faster than pollInterval so the spinner reads as smooth motion without
// re-listing sessions each tick.
const spinnerInterval = 150 * time.Millisecond

// spinnerFrames is the rotating braille glyph cycle shown for a busy session: an
// arc of 4 filled dots (with a 2-dot gap) sweeping clockwise around the perimeter
// of one braille cell.
var spinnerFrames = []string{"⠹", "⠼", "⠶", "⠧", "⠏", "⠛"}

var (
	clStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))  // claude
	ocStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("213")) // opencode (pink)
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
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

// messages
type tickMsg time.Time
type sessionsMsg struct {
	names []string
	err   error
}
type projectsMsg struct {
	projects []project.Project
	err      error
}
type spinnerMsg struct{}
type reconciledMsg struct{ errs []error }
type attentionMsg struct {
	states map[string]attentionState
	hashes map[string]uint64 // session name -> pane fingerprint, for idle tracking
}
type focusedMsg struct{ err error }
type savedMsg struct{ err error }

type model struct {
	mgr           *Manager
	sessions      []string
	projects      []project.Project
	collapsed     map[string]bool           // collapse key -> collapsed
	detached      map[string]bool           // session name -> pane detached (tmux still running)
	attention     map[string]attentionState // session name -> latest detected attention state
	idle          idleTracker               // per-session pane-stability clock for idle-kill
	cursor        int                       // index into rows()
	spinnerFrame  int                       // animation frame for busy-session glyphs
	prompt        *agentPrompt              // non-nil while the agent-picker overlay is open
	lastErr       string
	width, height int

	// scopeDir is the main-worktree path kmux is scoped to (from the CLI
	// directory argument); scopeName is its project name. Both empty in the
	// default, unscoped mode. When set, the Projects panel shows only that
	// project and the Sessions panel only its sessions.
	scopeDir  string
	scopeName string
}

// agentPrompt is the state of the agent-picker shown when launching a project:
// it offers a choice of agent (claude / opencode) for the selected project row.
type agentPrompt struct {
	title   string // human label for the project/worktree being launched
	session string // the row's claude session name (base for sessionForKind)
	dir     string // working directory the agent launches in
	cursor  int    // index into promptOptions
	tab     bool   // launch the chosen agent in a standalone kitty tab, not a pane
}

// promptOptions are the agent kinds offered by the picker, in display order.
var promptOptions = []struct {
	kind, label string
	style       lipgloss.Style
}{
	{"claude", "Claude", clStyle},
	{"opencode", "OpenCode", ocStyle},
}

// newModel builds the dashboard model. scopeDir, when non-empty, is the main
// worktree of the single project kmux is scoped to (see model.scopeDir).
func newModel(mgr *Manager, scopeDir string) model {
	// Restore detached sessions and idle clocks from a previous run; best-effort
	// (a read error just starts with empty sets).
	detached, idle, err := LoadState()
	if err != nil || detached == nil {
		detached, idle = map[string]bool{}, map[string]idleRecord{}
	}
	scopeName := ""
	if scopeDir != "" {
		scopeName = filepath.Base(scopeDir)
	}
	// Resolve the idle-kill timeout from config; a read error falls back to the
	// default, matching the optional, best-effort handling of the rest of config.
	// Seeding the tracker with the persisted clocks lets idle time accumulated
	// before this launch keep counting instead of resetting to zero.
	cfg, _ := config.LoadConfig()
	return model{
		mgr:       mgr,
		collapsed: map[string]bool{},
		detached:  detached,
		attention: map[string]attentionState{},
		idle:      newIdleTrackerFrom(cfg.IdleDuration(), idle),
		scopeDir:  scopeDir,
		scopeName: scopeName,
	}
}

func (m model) Init() tea.Cmd {
	// Kick off an immediate poll, then tick on the interval.
	return tea.Batch(pollCmd(), projectsCmd(m.scopeDir), tickCmd(), spinnerCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(pollInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// spinnerCmd schedules the next busy-animation frame.
func spinnerCmd() tea.Cmd {
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return spinnerMsg{} })
}

// pollCmd lists agent sessions off the UI goroutine.
func pollCmd() tea.Cmd {
	return func() tea.Msg {
		names, err := tmux.ListAgentSessions()
		return sessionsMsg{names: names, err: err}
	}
}

// attentionCmd captures each session's tmux pane off the UI goroutine and
// classifies its attention state. It runs sequentially (capture-pane is a cheap
// local call and there are few sessions) and is best-effort: a capture failure
// for one session yields attnUnknown for it rather than failing the whole batch.
// It is fed the full session list (including detached ones — tmux keeps their
// buffers), so a detached-but-waiting agent still shows its status glyph.
func attentionCmd(sessions []string) tea.Cmd {
	snap := append([]string(nil), sessions...)
	return func() tea.Msg {
		states := make(map[string]attentionState, len(snap))
		hashes := make(map[string]uint64, len(snap))
		for _, s := range snap {
			text, err := tmux.CapturePane(s)
			if err != nil {
				// No hash recorded: the idle tracker treats this session as gone
				// for this poll and resets its clock when capture recovers, so a
				// flaky capture never causes a kill.
				states[s] = attnUnknown
				continue
			}
			states[s] = classifyAttention(tmux.AgentKind(s), text)
			hashes[s] = hashPane(text)
		}
		return attentionMsg{states: states, hashes: hashes}
	}
}

// projectsCmd scans projects off the UI goroutine. When scopeDir is set it
// resolves just that one project (scoped mode); otherwise it scans ~/git plus
// any folders listed in the config file.
func projectsCmd(scopeDir string) tea.Cmd {
	return func() tea.Msg {
		if scopeDir != "" {
			p, err := project.ScanProject(scopeDir)
			if err != nil {
				return projectsMsg{err: err}
			}
			return projectsMsg{projects: []project.Project{*p}}
		}
		projects, err := project.ScanProjects()
		return projectsMsg{projects: projects, err: err}
	}
}

// reconcileCmd performs kitty RC work off the UI goroutine. It attaches/detaches
// agent panes, then pads the layout with placeholder panes so real agent panes
// keep a fixed width. When the pane layout changes it follows up with a
// Rebalance to pin the sidebar width and even out the agent columns.
func reconcileCmd(mgr *Manager, active []string) tea.Cmd {
	return func() tea.Msg {
		live, err := kitty.LiveWindowIDs()
		if err != nil {
			live = nil // best-effort: skip the manual-close prune this round
		}
		// A reconcile that adds a pane pulls the kitty app to the macOS foreground
		// even with --keep-focus, stealing system focus from whatever the user was
		// doing. These spawns are automatic (a session appeared on its own, not via
		// a manual open), so capture the frontmost app first and hand focus back
		// afterwards to keep the spawn in the background. Only query when an add is
		// actually pending, so the idle tick stays cheap.
		var prevApp string
		for _, s := range active {
			if !mgr.Attached(s) {
				prevApp = frontmostApp()
				break
			}
		}
		changed, errs := mgr.Reconcile(active, live)
		// Lift stacked panes into any slot a removed column just freed, before
		// padding, so a collapsed split fills the gap instead of an idle slot.
		cchanged, cerrs := mgr.Compact()
		errs = append(errs, cerrs...)
		pchanged, perrs := mgr.SyncPlaceholders(live)
		errs = append(errs, perrs...)
		if changed || cchanged || pchanged {
			errs = append(errs, mgr.Rebalance()...)
		}
		if changed && prevApp != "" {
			restoreFrontmostApp(prevApp)
		}
		return reconciledMsg{errs: errs}
	}
}

// focusCmd gives keyboard focus to a session's kitty pane off the UI goroutine.
func focusCmd(id int) tea.Cmd {
	return func() tea.Msg {
		return focusedMsg{err: kitty.FocusWindow(id)}
	}
}

// openSessionCmd creates (if needed) and attaches an agent session pane off the
// UI goroutine, then pads/rebalances the layout the same way reconcileCmd does
// so the new pane lands at the fixed agent width. agentCmd is the executable the
// new tmux session runs (e.g. "claude" or "opencode").
func openSessionCmd(mgr *Manager, name, dir, agentCmd string) tea.Cmd {
	return func() tea.Msg {
		if err := mgr.Open(name, dir, agentCmd); err != nil {
			return reconciledMsg{errs: []error{err}}
		}
		live, err := kitty.LiveWindowIDs()
		if err != nil {
			live = nil // best-effort: skip the manual-close prune this round
		}
		_, errs := mgr.SyncPlaceholders(live)
		errs = append(errs, mgr.Rebalance()...)
		return reconciledMsg{errs: errs}
	}
}

// killSessionCmd kills a session's tmux session off the UI goroutine, then
// re-lists so the panel updates immediately (the resulting sessionsMsg drives
// reconcile, which closes the now-orphaned pane).
func killSessionCmd(name string) tea.Cmd {
	return func() tea.Msg {
		if err := tmux.KillSession(name); err != nil {
			return sessionsMsg{err: err}
		}
		names, err := tmux.ListAgentSessions()
		return sessionsMsg{names: names, err: err}
	}
}

// reattachSessionCmd re-opens a pane for an already-running session off the UI
// goroutine (for a session whose pane was lost), then pads/rebalances the layout
// the same way openSessionCmd does.
func reattachSessionCmd(mgr *Manager, name string) tea.Cmd {
	return func() tea.Msg {
		if err := mgr.Reattach(name); err != nil {
			return reconciledMsg{errs: []error{err}}
		}
		live, err := kitty.LiveWindowIDs()
		if err != nil {
			live = nil // best-effort: skip the manual-close prune this round
		}
		_, errs := mgr.SyncPlaceholders(live)
		errs = append(errs, mgr.Rebalance()...)
		return reconciledMsg{errs: errs}
	}
}

// saveStateCmd persists the detached-session set and the idle clocks off the UI
// goroutine. It snapshots both maps first so a later mutation can't race the
// write. (idle.snapshot already returns a fresh copy.)
func (m model) saveStateCmd() tea.Cmd {
	detached := make(map[string]bool, len(m.detached))
	for k, on := range m.detached {
		if on {
			detached[k] = true
		}
	}
	idle := m.idle.snapshot()
	return func() tea.Msg {
		return savedMsg{err: SaveState(detached, idle)}
	}
}

// lazygitCmd opens lazygit for dir in a new kitty tab.
func lazygitCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		return focusedMsg{err: kitty.OpenLazygit(dir)}
	}
}

// editorCmd opens (or focuses) an editor for dir off the UI goroutine.
func editorCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		return focusedMsg{err: OpenEditor(dir)}
	}
}

// openAgentTabCmd attaches an agent session in a new standalone kitty tab (not a
// managed pane), off the UI goroutine. When agentCmd is non-empty it first
// ensures a detached tmux session exists; for an already-running session pass an
// empty agentCmd to skip creation and just attach.
func openAgentTabCmd(name, dir, agentCmd string) tea.Cmd {
	return func() tea.Msg {
		if agentCmd != "" {
			if err := tmux.NewDetachedSession(name, dir, agentCmd); err != nil {
				return focusedMsg{err: err}
			}
		}
		return focusedMsg{err: kitty.OpenAgentTab(name, name)}
	}
}

// openTabCmd launches a new kitty tab running a fresh kmux scoped to dir, off
// the UI goroutine. The new tab is an independent kmux session in the same
// terminal window.
func openTabCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		exe, err := os.Executable()
		if err != nil {
			return focusedMsg{err: err}
		}
		return focusedMsg{err: kitty.OpenTab(exe, dir, "kmux::"+filepath.Base(dir))}
	}
}

// rowDeco renders styled row labels/badges. It keeps lipgloss styling out of the
// pure tree-building code in tree.go. spinner is the current busy-animation frame,
// advanced on each spinner tick.
type rowDeco struct{ spinner int }

func (d rowDeco) session(name string, depth int, st attentionState, attached, detached bool) row {
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

// sessionLabel strips the agent suffix (~cl / ~oc) from a session name, leaving the
// project/worktree path the row displays (the agent kind shows as a trailing badge).
func sessionLabel(name string) string {
	return strings.TrimSuffix(strings.TrimSuffix(name, "~cl"), "~oc")
}

// agentBadge renders the styled agent-kind badge for a session name ("CC" for
// Claude, "OC" for OpenCode), prefixed with its attach state: a green "A" when
// attached (live pane) or a red "D" when detached (tmux alive, pane closed), so
// the badge reads "A~CC"/"D~CC"/"CC" or "A~OC"/"D~OC"/"OC". The prefix keeps its own
// color (green/red) distinct from the agent color. Returns "" for a non-agent name.
func agentBadge(name string, attached, detached bool) string {
	prefix := ""
	switch {
	case attached:
		prefix = okStyle.Render("A") + dimStyle.Render("~")
	case detached:
		prefix = errStyle.Render("D") + dimStyle.Render("~")
	}
	switch tmux.AgentKind(name) {
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
func attentionGlyph(st attentionState, frame int) string {
	switch st {
	case attnPermission:
		return errStyle.Render("!")
	case attnWaiting:
		return okStyle.Render("✓")
	case attnBusy:
		return dimStyle.Render(spinnerFrames[frame%len(spinnerFrames)])
	default: // attnUnknown
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
// of a plain git glyph), the project name (green when active), and the dim branch
// tail.
func branchLabel(name, branch string, active bool, gs gitStatus) string {
	return gitStatusGlyph(gs) + " " + projectName(name, active) + branchSuffix(branch)
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

// projectName renders a project/worktree name, colored green when it has a live
// agent session.
func projectName(name string, active bool) string {
	if active {
		return activeStyle.Render(name)
	}
	return name
}

// projectLeaf labels a single-worktree project (name + branch).
func (rowDeco) projectLeaf(p project.Project, active bool) string {
	return branchLabel(p.Name, p.Branch, active, projectGit(p))
}

// projectFolder labels a multi-worktree project header (folder glyph + name).
// The glyph is the open variant when expanded, the closed variant otherwise.
// The branch moves onto the main-worktree child row inside the expanded list.
func (rowDeco) projectFolder(p project.Project, open, active bool) string {
	glyph := folderGlyph
	if open {
		glyph = folderOpenGlyph
	}
	label := folderStyle.Render(glyph) + " " + projectName(p.Name, active)
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
func (rowDeco) mainWorktree(p project.Project, active bool) string {
	return branchLabel(p.Name, p.Branch, active, projectGit(p))
}

func (rowDeco) worktree(w project.Worktree, active bool) string {
	return branchLabel(w.Name, w.Branch, active, worktreeGit(w))
}

// rows builds the combined, navigable row list: session rows first, then
// project rows. The cursor indexes into this slice.
func (m model) rows() []row {
	deco := rowDeco{spinner: m.spinnerFrame}
	names := projectNames(m.projects)
	sess := buildSessionRows(m.sessions, names, m.collapsed, m.attention, m.mgr.Attached, m.isDetached, deco)
	proj := buildProjectRows(m.projects, m.collapsed, m.hasSessionAny, deco)
	out := make([]row, 0, len(sess)+len(proj))
	out = append(out, sess...)
	out = append(out, proj...)
	return out
}

// attachable is m.sessions minus detached sessions: the set reconcile keeps
// panes for. A detached session stays in the Sessions panel (and its tmux
// session keeps running) but has no pane, so it renders as unattached until
// re-opened with enter/l.
func (m model) attachable() []string {
	if len(m.detached) == 0 {
		return m.sessions
	}
	out := make([]string, 0, len(m.sessions))
	for _, s := range m.sessions {
		if !m.detached[s] {
			out = append(out, s)
		}
	}
	return out
}

// scopedSessions restricts a session list to the scoped project, dropping
// sessions that belong to other projects (and the ungrouped bucket). In the
// default, unscoped mode it returns the list unchanged. Matching mirrors
// groupSessions so a kept session lands under the same project node.
func (m model) scopedSessions(names []string) []string {
	if m.scopeName == "" {
		return names
	}
	scope := []string{m.scopeName}
	out := make([]string, 0, len(names))
	for _, s := range names {
		rem := strings.TrimSuffix(strings.TrimSuffix(s, "~cl"), "~oc")
		if _, _, ok := matchProject(rem, scope); ok {
			out = append(out, s)
		}
	}
	return out
}

// isDetached reports whether the user has detached session name's pane.
func (m model) isDetached(name string) bool {
	return m.detached[name]
}

// pruneDetached drops detached entries for sessions that no longer exist, so a
// future session that reuses the same name isn't silently kept detached.
func (m *model) pruneDetached() (changed bool) {
	for s := range m.detached {
		if !slices.Contains(m.sessions, s) {
			delete(m.detached, s)
			changed = true
		}
	}
	return changed
}

// hasSession reports whether an agent session with the given name is running.
func (m model) hasSession(name string) bool {
	return slices.Contains(m.sessions, name)
}

// hasSessionAny reports whether a project/worktree has a running session of
// either agent kind. claudeName is the ~cl session name (as produced by
// expectedSession); the opencode name is derived by suffix swap. It drives the
// "active" green coloring of project rows so an opencode-only project still
// reads as live.
func (m model) hasSessionAny(claudeName string) bool {
	return m.hasSession(claudeName) || m.hasSession(sessionForKind(claudeName, "opencode"))
}

// killTarget returns the agent session a `d` press should kill for row r: a
// session leaf kills itself; a project or worktree row kills its running
// session. It returns "" when there's nothing to kill (folder headers, or
// project rows whose session isn't running).
func (m model) killTarget(r *row) string {
	if r == nil {
		return ""
	}
	if isSessionLeaf(r) {
		return r.session
	}
	if r.section == sectionProjects && r.session != "" && m.hasSession(r.session) {
		return r.session
	}
	return ""
}

// lazygitDir returns the directory to open lazygit in for row r: a project or
// worktree row carries its dir directly; a session leaf is resolved back to its
// project's directory via the session name. It returns "" when there's no
// associated directory (e.g. folder headers or ungrouped sessions).
func (m model) lazygitDir(r *row) string {
	if r == nil {
		return ""
	}
	if r.dir != "" {
		return r.dir // project / worktree leaf carries its dir directly
	}
	if r.section != sectionSessions {
		return ""
	}
	if !r.collapsible {
		return m.sessionDir(r.session) // session leaf: resolve from its name
	}
	// Sessions-panel group header: derive the dir from its collapse key, which
	// encodes "sess:<project>" or "sess:<project>/<worktree>".
	proj, wt, _ := strings.Cut(strings.TrimPrefix(r.key, "sess:"), "/")
	return m.projectPath(proj, wt)
}

// editorDir resolves the directory to open the editor in for row r. It reuses
// lazygitDir (project/worktree leaves and session rows) and falls back to the
// project root so a multi-worktree folder header opens its editor too.
func (m model) editorDir(r *row) string {
	if dir := m.lazygitDir(r); dir != "" {
		return dir
	}
	return m.projectRoot(r)
}

// projectRoot returns the main-worktree path of the project that Projects-panel
// row r belongs to, for any of its rows: the folder header (project name encoded
// in its collapse key), the main-worktree leaf, or a linked-worktree leaf. It
// returns "" for non-project rows or when no project matches.
func (m model) projectRoot(r *row) string {
	if r == nil || r.section != sectionProjects {
		return ""
	}
	if r.collapsible {
		name := strings.TrimPrefix(r.key, "proj:")
		for _, p := range m.projects {
			if p.Name == name {
				return p.Path
			}
		}
		return ""
	}
	// Leaf: find the project owning this dir (its main path or a worktree path).
	for _, p := range m.projects {
		if p.Path == r.dir {
			return p.Path
		}
		for _, w := range p.Worktrees {
			if w.Path == r.dir {
				return p.Path
			}
		}
	}
	return ""
}

// sessionDir resolves the working directory for an agent session by matching its
// name (<project>[/<worktree>]~<cl|oc>) against the scanned projects, mirroring
// how sessions are grouped under projects in the tree. It returns "" when no
// project matches (e.g. ungrouped sessions).
func (m model) sessionDir(name string) string {
	rem := strings.TrimSuffix(strings.TrimSuffix(name, "~cl"), "~oc")
	proj, wt, ok := matchProject(rem, projectNames(m.projects))
	if !ok {
		return ""
	}
	return m.projectPath(proj, wt)
}

// projectPath returns the directory for a scanned project (wt == "") or one of
// its linked worktrees (wt == the worktree's basename), or "" when no such
// project/worktree exists (e.g. the "(ungrouped)" bucket).
func (m model) projectPath(proj, wt string) string {
	for _, p := range m.projects {
		if p.Name != proj {
			continue
		}
		if wt == "" {
			return p.Path
		}
		for _, w := range p.Worktrees {
			if w.Name == wt {
				return w.Path
			}
		}
	}
	return ""
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tickMsg:
		return m, tea.Batch(pollCmd(), projectsCmd(m.scopeDir), tickCmd())

	case spinnerMsg:
		m.spinnerFrame++
		return m, spinnerCmd()

	case sessionsMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, nil
		}
		m.lastErr = ""
		m.sessions = m.scopedSessions(msg.names)
		pruned := m.pruneDetached()
		// Refresh attention off the freshest session list (drives the session glyphs).
		cmd := tea.Batch(reconcileCmd(m.mgr, m.attachable()), attentionCmd(m.sessions))
		if pruned {
			cmd = tea.Batch(cmd, m.saveStateCmd())
		}
		return m, cmd

	case projectsMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, nil
		}
		m.projects = msg.projects
		return m, nil

	case reconciledMsg:
		if len(msg.errs) > 0 {
			m.lastErr = msg.errs[0].Error()
		}
		return m, nil

	case attentionMsg:
		m.attention = msg.states // display-only: no reconcile, no pane churn
		// Reap agent sessions whose pane has sat unchanged past idleTimeout,
		// freeing the memory their idle agent processes hold.
		busy := make(map[string]bool, len(msg.states))
		for s, st := range msg.states {
			busy[s] = st == attnBusy
		}
		kill := m.idle.reap(time.Now(), msg.hashes, busy)
		// Persist the freshly advanced idle clocks so a restart resumes them and
		// the launch sweep can reap sessions that stayed idle while kmux was off.
		cmds := []tea.Cmd{m.saveStateCmd()}
		for _, name := range kill {
			cmds = append(cmds, killSessionCmd(name))
		}
		return m, tea.Batch(cmds...)

	case focusedMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
		}
		return m, nil

	case savedMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
		}
		return m, nil
	}
	return m, nil
}

// handleKey processes navigation and fold keys (arrows + vim).
func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// The agent picker captures all input while open.
	if m.prompt != nil {
		return m.handlePromptKey(msg)
	}

	rows := m.rows()
	m.clampCursor(rows)

	switch msg.String() {
	case "q", "ctrl+c":
		m.mgr.CloseAll()
		return m, tea.Quit

	case "j", "down":
		if m.cursor < len(rows)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}

	case "l", "right":
		r := rowAt(rows, m.cursor)
		if r == nil {
			break
		}
		if cmd := m.openOrFocusSession(r); cmd != nil {
			return m, cmd
		}
		if cmd, ok := m.launchProject(r); ok {
			return m, cmd
		}
		if r.collapsible && m.collapsed[r.key] {
			delete(m.collapsed, r.key)
		}
	case "h", "left":
		r := rowAt(rows, m.cursor)
		if r == nil {
			break
		}
		if r.collapsible && !m.collapsed[r.key] {
			m.collapsed[r.key] = true
		} else {
			m.cursor = parentIndex(rows, m.cursor)
		}

	case "d":
		// Detach a session leaf: close the agent's kitty pane while leaving the
		// tmux session running, so the row stays in the panel (now unattached) and
		// can be re-opened with enter/l. Marking it detached keeps reconcile from
		// immediately re-attaching a pane; the reconcile below closes the current
		// pane right away.
		if r := rowAt(rows, m.cursor); isSessionLeaf(r) && !m.detached[r.session] {
			m.detached[r.session] = true
			return m, tea.Batch(reconcileCmd(m.mgr, m.attachable()), m.saveStateCmd())
		}
	case "D":
		// Kill the agent: ends the tmux session and its pane.
		if name := m.killTarget(rowAt(rows, m.cursor)); name != "" {
			return m, killSessionCmd(name)
		}
	case "enter", " ":
		r := rowAt(rows, m.cursor)
		if r == nil {
			break
		}
		if cmd := m.openOrFocusSession(r); cmd != nil {
			return m, cmd
		}
		if cmd, ok := m.launchProject(r); ok {
			return m, cmd
		}
		if r.collapsible {
			if m.collapsed[r.key] {
				delete(m.collapsed, r.key)
			} else {
				m.collapsed[r.key] = true
			}
		}

	case "g":
		if dir := m.lazygitDir(rowAt(rows, m.cursor)); dir != "" {
			return m, lazygitCmd(dir)
		}

	case "e":
		// Open (or focus) the editor for the selected row's directory. Works in
		// both panels, mirroring lazygit.
		if dir := m.editorDir(rowAt(rows, m.cursor)); dir != "" {
			return m, editorCmd(dir)
		}

	case "t":
		// Open the selected project's root/main worktree in a new kitty tab
		// running its own kmux. Projects panel only (no-op for session rows).
		if dir := m.projectRoot(rowAt(rows, m.cursor)); dir != "" {
			return m, openTabCmd(dir)
		}

	case "f":
		// Open the selected agent in its own kitty tab instead of a managed pane.
		// Works in both panels: a session leaf attaches its session; a project row
		// launches (or attaches) an agent, opening the picker when the kind is
		// ambiguous.
		r := rowAt(rows, m.cursor)
		if cmd := m.openSessionTab(r); cmd != nil {
			return m, cmd
		}
		if cmd, ok := m.launchProjectTab(r); ok {
			return m, cmd
		}

	case "c":
		// Launch (or focus) Claude for the selected project, skipping the picker.
		if cmd := m.launchKindOn(rowAt(rows, m.cursor), "claude"); cmd != nil {
			return m, cmd
		}
	case "o":
		// Launch (or focus) OpenCode for the selected project, skipping the picker.
		if cmd := m.launchKindOn(rowAt(rows, m.cursor), "opencode"); cmd != nil {
			return m, cmd
		}

	case "1":
		m.cursor = sectionStart(rows, sectionProjects)
	case "2":
		m.cursor = sectionStart(rows, sectionSessions)
	}
	return m, nil
}

// handlePromptKey drives the agent picker: j/k move between agents, enter/space
// launches the highlighted one, and esc/h cancels. ctrl+c still quits outright.
func (m model) handlePromptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.mgr.CloseAll()
		return m, tea.Quit
	case "esc", "q", "h", "left":
		m.prompt = nil
	case "j", "down":
		if m.prompt.cursor < len(promptOptions)-1 {
			m.prompt.cursor++
		}
	case "k", "up":
		if m.prompt.cursor > 0 {
			m.prompt.cursor--
		}
	case "tab":
		m.prompt.cursor = (m.prompt.cursor + 1) % len(promptOptions)
	case "enter", " ", "l", "right":
		return m, m.confirmPrompt()
	}
	return m, nil
}

// isSessionLeaf reports whether r is an actionable session row (a leaf in the
// Sessions panel, i.e. a session name rather than a project/worktree node).
func isSessionLeaf(r *row) bool {
	return r != nil && r.section == sectionSessions && !r.collapsible
}

// actionSession returns the agent session name a focus/open action targets for row
// r: a session leaf carries it in its session field. It returns "" for any other
// row.
func actionSession(r *row) string {
	if isSessionLeaf(r) {
		return r.session
	}
	return ""
}

// openOrFocusSession returns a command to focus the agent pane for a session leaf
// row, re-opening a pane first when the session is running but currently has none
// (e.g. its pane was closed by hand). It returns nil when r targets no session.
func (m *model) openOrFocusSession(r *row) tea.Cmd {
	name := actionSession(r)
	if name == "" {
		return nil
	}
	return m.focusOrReattach(name)
}

// focusOrReattach focuses a running session's pane, re-opening one first when the
// session is alive but has no pane (e.g. closed by hand or detached). Either way
// it clears any detached flag so reconcile keeps the pane, persisting that change
// when there was one.
func (m *model) focusOrReattach(name string) tea.Cmd {
	save := m.clearDetached(name)
	if id, ok := m.mgr.WindowID(name); ok {
		return tea.Batch(focusCmd(id), save)
	}
	return tea.Batch(reattachSessionCmd(m.mgr, name), save)
}

// openSessionTab returns a command to attach a session leaf row's session in its
// own standalone kitty tab (not a managed pane). The tmux session already
// exists, so it just attaches. It returns nil when r is not a session leaf.
func (m *model) openSessionTab(r *row) tea.Cmd {
	if !isSessionLeaf(r) {
		return nil
	}
	return openAgentTabCmd(r.session, "", "")
}

// launchProjectTab is the standalone-tab counterpart of launchProject: it opens
// the project/worktree row's agent in its own kitty tab rather than a pane. When
// exactly one agent kind is running it attaches that one directly; when neither
// (or both) is running it opens the agent picker in tab mode so the chosen kind
// launches into a tab. It returns (cmd, true) for rows it handled and
// (nil, false) for rows it doesn't act on, mirroring launchProject.
func (m *model) launchProjectTab(r *row) (tea.Cmd, bool) {
	if r == nil || r.section != sectionProjects || r.session == "" {
		return nil, false
	}
	claude := r.session
	opencode := sessionForKind(r.session, "opencode")
	var running []string
	if m.hasSession(claude) {
		running = append(running, claude)
	}
	if m.hasSession(opencode) {
		running = append(running, opencode)
	}
	if len(running) == 1 {
		return openAgentTabCmd(running[0], "", ""), true
	}
	m.prompt = &agentPrompt{
		title:   filepath.Base(r.dir),
		session: r.session,
		dir:     r.dir,
		tab:     true,
	}
	return nil, true
}

// launchKindTab is the standalone-tab counterpart of launchKind: it attaches the
// given agent kind's session in a new kitty tab, creating the tmux session first
// when it isn't already running.
func (m *model) launchKindTab(session, dir, kind string) tea.Cmd {
	name := sessionForKind(session, kind)
	if m.hasSession(name) {
		return openAgentTabCmd(name, "", "")
	}
	return openAgentTabCmd(name, dir, agentCommand(kind))
}

// launchProject activates a project/worktree leaf row. When exactly one agent
// kind already has a running session it focuses that session directly, skipping
// the picker — there is nothing to choose. When neither (or both) kinds are
// running it opens the agent picker. It returns (cmd, true) for rows it handled
// (cmd is non-nil only for the direct-focus case) and (nil, false) for rows it
// doesn't act on — folder headers (empty session) and non-project rows — so
// callers fall through to fold/collapse handling.
func (m *model) launchProject(r *row) (tea.Cmd, bool) {
	if r.section != sectionProjects || r.session == "" {
		return nil, false
	}
	claude := r.session
	opencode := sessionForKind(r.session, "opencode")
	var running []string
	if m.hasSession(claude) {
		running = append(running, claude)
	}
	if m.hasSession(opencode) {
		running = append(running, opencode)
	}
	if len(running) == 1 {
		return m.focusOrReattach(running[0]), true
	}
	m.prompt = &agentPrompt{
		title:   filepath.Base(r.dir),
		session: r.session,
		dir:     r.dir,
	}
	return nil, true
}

// confirmPrompt acts on the agent picker's current selection, launching the
// chosen agent kind and clearing the picker.
func (m *model) confirmPrompt() tea.Cmd {
	p := m.prompt
	m.prompt = nil
	if p.tab {
		return m.launchKindTab(p.session, p.dir, promptOptions[p.cursor].kind)
	}
	return m.launchKind(p.session, p.dir, promptOptions[p.cursor].kind)
}

// launchKind focuses the given agent kind's session if it is already running,
// otherwise creates and attaches one. session is the row's claude session name
// (the base for sessionForKind), dir its working directory.
func (m *model) launchKind(session, dir, kind string) tea.Cmd {
	name := sessionForKind(session, kind)
	// Opening a session clears any detached flag so reconcile keeps its pane;
	// persist the change when there was one.
	save := m.clearDetached(name)
	if id, ok := m.mgr.WindowID(name); ok {
		return tea.Batch(focusCmd(id), save)
	}
	return openSessionCmd(m.mgr, name, dir, agentCommand(kind))
}

// launchKindOn launches a specific agent kind for project leaf row r, returning
// nil for rows that can't launch one (folder headers, non-project rows).
func (m *model) launchKindOn(r *row, kind string) tea.Cmd {
	if r == nil || r.section != sectionProjects || r.session == "" {
		return nil
	}
	return m.launchKind(r.session, r.dir, kind)
}

// clearDetached removes name's detached flag and returns a command to persist
// the change, or nil when name was not detached (nothing to save).
func (m *model) clearDetached(name string) tea.Cmd {
	if !m.detached[name] {
		return nil
	}
	delete(m.detached, name)
	return m.saveStateCmd()
}

func (m *model) clampCursor(rows []row) {
	if m.cursor >= len(rows) {
		m.cursor = len(rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func rowAt(rows []row, i int) *row {
	if i < 0 || i >= len(rows) {
		return nil
	}
	return &rows[i]
}

// parentIndex returns the index of the nearest preceding row at a shallower
// depth, or the current index if none exists.
func parentIndex(rows []row, i int) int {
	if i < 0 || i >= len(rows) {
		return i
	}
	for j := i - 1; j >= 0; j-- {
		if rows[j].depth < rows[i].depth {
			return j
		}
	}
	return i
}

// sectionStart returns the index of the first row in sec, or 0 if absent.
func sectionStart(rows []row, sec section) int {
	for i, r := range rows {
		if r.section == sec {
			return i
		}
	}
	return 0
}

// keyHint is one row in the bottom keybind panel: the key(s) and what they do.
type keyHint struct{ key, desc string }

// helpHints returns the keybind hints for the focused section: the section's
// action keys first, then the shared navigation keys (move / switch panel /
// quit). The h/l fold keys are omitted as redundant with open/focus.
func helpHints(focused section) []keyHint {
	switch focused {
	case sectionSessions:
		return []keyHint{
			{"j/k", "Move"},
			{"1/2", "Switch panel"},
			{"↵/space", "Focus pane"},
			{"f", "Fullscreen agent"},
			{"d", "Detach"},
			{"D", "Kill session"},
			{"g", "Lazygit"},
			{"e", "Editor"},
			{"q", "Quit"},
		}
	}
	return []keyHint{
		{"j/k", "Move"},
		{"1/2", "Switch panel"},
		{"↵/space", "Launch agent"},
		{"c/o", "Launch claude/opencode"},
		{"f", "Fullscreen agent"},
		{"t", "Kmux project in tab"},
		{"D", "Kill session"},
		{"g", "Lazygit"},
		{"e", "Editor"},
		{"q", "Quit"},
	}
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

// helpHeight is the body-row count of the keybind panel: the tallest of the three
// sections' hint lists, so the panel stays a constant height and the dashboard
// doesn't jump when switching panels (shorter lists pad with blank rows).
func helpHeight() int {
	return max(
		len(helpHints(sectionSessions)),
		len(helpHints(sectionProjects)),
	)
}

// renderHelp formats the keybind hints into panel body lines, keys left-aligned
// to a common width with dim descriptions.
func renderHelp(focused section) []string {
	hints := helpHints(focused)
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
	hh := helpHeight() + 2
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
		parts = append(parts, panel("Keys", renderHelp(focused), m.width, hh, false))
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
		b.WriteString(r.mark + " ")
	}
	b.WriteString(r.label)
	if r.badge != "" {
		b.WriteString(" " + r.badge)
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
