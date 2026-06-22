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
)

const pollInterval = 2 * time.Second

var (
	clStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // claude
	ocStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // opencode
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	detachedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3")) // detached session (tmux alive, no pane)
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	okStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	activeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // project with a live session

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

	keyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // keybind hint (yellow)

	borderIdle = lipgloss.Color("240") // unfocused panel border (grey)
)

// messages
type tickMsg time.Time
type sessionsMsg struct {
	names []string
	err   error
}
type projectsMsg struct {
	projects []Project
	err      error
}
type reconciledMsg struct{ errs []error }
type focusedMsg struct{ err error }
type savedMsg struct{ err error }

type model struct {
	mgr           *Manager
	sessions      []string
	projects      []Project
	collapsed     map[string]bool // collapse key -> collapsed
	detached      map[string]bool // session name -> pane detached (tmux still running)
	cursor        int             // index into rows()
	prompt        *agentPrompt    // non-nil while the agent-picker overlay is open
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
	// Restore detached sessions from a previous run; best-effort (a read error
	// just starts with an empty set).
	detached, err := LoadDetached()
	if err != nil || detached == nil {
		detached = map[string]bool{}
	}
	scopeName := ""
	if scopeDir != "" {
		scopeName = filepath.Base(scopeDir)
	}
	return model{
		mgr:       mgr,
		collapsed: map[string]bool{},
		detached:  detached,
		scopeDir:  scopeDir,
		scopeName: scopeName,
	}
}

func (m model) Init() tea.Cmd {
	// Kick off an immediate poll, then tick on the interval.
	return tea.Batch(pollCmd(), projectsCmd(m.scopeDir), tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(pollInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// pollCmd lists agent sessions off the UI goroutine.
func pollCmd() tea.Cmd {
	return func() tea.Msg {
		names, err := ListAgentSessions()
		return sessionsMsg{names: names, err: err}
	}
}

// projectsCmd scans projects off the UI goroutine. When scopeDir is set it
// resolves just that one project (scoped mode); otherwise it scans ~/git plus
// any folders listed in the config file.
func projectsCmd(scopeDir string) tea.Cmd {
	return func() tea.Msg {
		if scopeDir != "" {
			p, err := ScanProject(scopeDir)
			if err != nil {
				return projectsMsg{err: err}
			}
			return projectsMsg{projects: []Project{*p}}
		}
		projects, err := ScanProjects()
		return projectsMsg{projects: projects, err: err}
	}
}

// reconcileCmd performs kitty RC work off the UI goroutine. It attaches/detaches
// agent panes, then pads the layout with placeholder panes so real agent panes
// keep a fixed width. When the pane layout changes it follows up with a
// Rebalance to pin the sidebar width and even out the agent columns.
func reconcileCmd(mgr *Manager, active []string) tea.Cmd {
	return func() tea.Msg {
		live, err := LiveWindowIDs()
		if err != nil {
			live = nil // best-effort: skip the manual-close prune this round
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
		return reconciledMsg{errs: errs}
	}
}

// focusCmd gives keyboard focus to a session's kitty pane off the UI goroutine.
func focusCmd(id int) tea.Cmd {
	return func() tea.Msg {
		return focusedMsg{err: FocusWindow(id)}
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
		live, err := LiveWindowIDs()
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
		if err := KillSession(name); err != nil {
			return sessionsMsg{err: err}
		}
		names, err := ListAgentSessions()
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
		live, err := LiveWindowIDs()
		if err != nil {
			live = nil // best-effort: skip the manual-close prune this round
		}
		_, errs := mgr.SyncPlaceholders(live)
		errs = append(errs, mgr.Rebalance()...)
		return reconciledMsg{errs: errs}
	}
}

// saveDetachedCmd persists the detached-session set off the UI goroutine. It
// snapshots the map first so a later mutation can't race the write.
func saveDetachedCmd(detached map[string]bool) tea.Cmd {
	snap := make(map[string]bool, len(detached))
	for k, on := range detached {
		if on {
			snap[k] = true
		}
	}
	return func() tea.Msg {
		return savedMsg{err: SaveDetached(snap)}
	}
}

// lazygitCmd opens lazygit for dir in kitty's quick-access dropdown.
func lazygitCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		return focusedMsg{err: OpenLazygit(dir)}
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
		return focusedMsg{err: OpenTab(exe, dir, "kmux::"+filepath.Base(dir))}
	}
}

// rowDeco renders styled row labels/badges. It keeps lipgloss styling out of the
// pure tree-building code in tree.go.
type rowDeco struct{}

func (rowDeco) session(name string, depth int, attached, detached bool) row {
	var badge string
	switch AgentKind(name) {
	case "claude":
		badge = clStyle.Render("Claude")
	case "opencode":
		badge = ocStyle.Render("OpenCode")
	}
	// ✓ attached (live pane); ○ detached (tmux alive, pane closed); · neither.
	mark := dimStyle.Render("·")
	switch {
	case attached:
		mark = okStyle.Render("✓")
	case detached:
		mark = detachedStyle.Render("○")
	}
	return row{section: sectionSessions, depth: depth, label: name, badge: badge, mark: mark}
}

// branchGlyph is the git-branch symbol () shown before a branch name.
const branchGlyph = ""

// folderGlyph / folderOpenGlyph are the outlined folder symbols shown before a
// multi-worktree project header: closed when collapsed, open when expanded.
const (
	folderGlyph     = "" // nf-fa-folder_o
	folderOpenGlyph = "" // nf-fa-folder_open_o
)

// branchSuffix renders the dim " <glyph> <branch>" tail, or "" when no branch.
func branchSuffix(branch string) string {
	if branch == "" {
		return ""
	}
	return dimStyle.Render(" " + branchGlyph + " " + branch)
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
func (rowDeco) projectLeaf(p Project, active bool) string {
	return projectName(p.Name, active) + branchSuffix(p.Branch)
}

// projectFolder labels a multi-worktree project header (folder glyph + name).
// The glyph is the open variant when expanded, the closed variant otherwise.
// The branch moves onto the main-worktree child row inside the expanded list.
func (rowDeco) projectFolder(p Project, open, active bool) string {
	glyph := folderGlyph
	if open {
		glyph = folderOpenGlyph
	}
	return glyph + " " + projectName(p.Name, active)
}

// mainWorktree labels the main worktree row (repo name + branch), listed first
// inside an expanded project folder.
func (rowDeco) mainWorktree(p Project, active bool) string {
	return projectName(p.Name, active) + branchSuffix(p.Branch)
}

func (rowDeco) worktree(w Worktree, active bool) string {
	label := projectName(w.Name, active)
	if w.Branch != "" {
		label += dimStyle.Render(" " + branchGlyph + " " + w.Branch)
	}
	return label
}

// rows builds the combined, navigable row list: session rows first, then
// project rows. The cursor indexes into this slice.
func (m model) rows() []row {
	deco := rowDeco{}
	sess := buildSessionRows(m.sessions, projectNames(m.projects), m.collapsed, m.mgr.Attached, m.isDetached, deco)
	proj := buildProjectRows(m.projects, m.collapsed, m.hasSessionAny, deco)
	return append(sess, proj...)
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
		rem := strings.TrimSuffix(strings.TrimSuffix(s, "_cl"), "_oc")
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
// either agent kind. claudeName is the _cl session name (as produced by
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
		return r.label
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
		return m.sessionDir(r.label) // session leaf: resolve from its name
	}
	// Sessions-panel group header: derive the dir from its collapse key, which
	// encodes "sess:<project>" or "sess:<project>/<worktree>".
	proj, wt, _ := strings.Cut(strings.TrimPrefix(r.key, "sess:"), "/")
	return m.projectPath(proj, wt)
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
// name (<project>[_<worktree>]_<cl|oc>) against the scanned projects, mirroring
// how sessions are grouped under projects in the tree. It returns "" when no
// project matches (e.g. ungrouped sessions).
func (m model) sessionDir(name string) string {
	rem := strings.TrimSuffix(strings.TrimSuffix(name, "_cl"), "_oc")
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

	case sessionsMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, nil
		}
		m.lastErr = ""
		m.sessions = m.scopedSessions(msg.names)
		pruned := m.pruneDetached()
		cmd := reconcileCmd(m.mgr, m.attachable())
		if pruned {
			cmd = tea.Batch(cmd, saveDetachedCmd(m.detached))
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
		if m.openAgentPrompt(r) {
			return m, nil
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
		if r := rowAt(rows, m.cursor); isSessionLeaf(r) && !m.detached[r.label] {
			m.detached[r.label] = true
			return m, tea.Batch(reconcileCmd(m.mgr, m.attachable()), saveDetachedCmd(m.detached))
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
		if m.openAgentPrompt(r) {
			return m, nil
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

	case "t":
		// Open the selected project's root/main worktree in a new kitty tab
		// running its own kmux. Projects panel only (no-op for session rows).
		if dir := m.projectRoot(rowAt(rows, m.cursor)); dir != "" {
			return m, openTabCmd(dir)
		}

	case "1":
		m.cursor = sectionStart(rows, sectionSessions)
	case "2":
		m.cursor = sectionStart(rows, sectionProjects)
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

// openOrFocusSession returns a command to focus the agent pane for a session
// leaf row, re-opening a pane first when the session is running but currently
// has none (e.g. its pane was closed by hand). It returns nil when r is not a
// session leaf.
func (m *model) openOrFocusSession(r *row) tea.Cmd {
	if !isSessionLeaf(r) {
		return nil
	}
	// Opening a session clears any detached flag so reconcile keeps its pane;
	// persist the change when there was one.
	save := m.clearDetached(r.label)
	if id, ok := m.mgr.WindowID(r.label); ok {
		return tea.Batch(focusCmd(id), save)
	}
	return tea.Batch(reattachSessionCmd(m.mgr, r.label), save)
}

// openAgentPrompt opens the agent picker for an actionable project leaf so the
// user can choose which agent (claude / opencode) to launch. It returns false
// for folder headers (empty session) and non-project rows so callers fall
// through to the fold/collapse handling.
func (m *model) openAgentPrompt(r *row) bool {
	if r.section != sectionProjects || r.session == "" {
		return false
	}
	m.prompt = &agentPrompt{
		title:   filepath.Base(r.dir),
		session: r.session,
		dir:     r.dir,
	}
	return true
}

// confirmPrompt acts on the agent picker's current selection: it focuses the
// chosen agent's session if it is already running, otherwise creates and
// attaches one. It clears the picker either way.
func (m *model) confirmPrompt() tea.Cmd {
	p := m.prompt
	m.prompt = nil
	kind := promptOptions[p.cursor].kind
	name := sessionForKind(p.session, kind)
	// Opening a session clears any detached flag so reconcile keeps its pane;
	// persist the change when there was one.
	save := m.clearDetached(name)
	if id, ok := m.mgr.WindowID(name); ok {
		return tea.Batch(focusCmd(id), save)
	}
	return openSessionCmd(m.mgr, name, p.dir, agentCommand(kind))
}

// clearDetached removes name's detached flag and returns a command to persist
// the change, or nil when name was not detached (nothing to save).
func (m *model) clearDetached(name string) tea.Cmd {
	if !m.detached[name] {
		return nil
	}
	delete(m.detached, name)
	return saveDetachedCmd(m.detached)
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
	if focused == sectionSessions {
		return []keyHint{
			{"↵/space", "focus pane"},
			{"d", "detach"},
			{"D", "kill session"},
			{"g", "lazygit"},
			{"j/k", "move"},
			{"1/2", "switch panel"},
			{"q", "quit"},
		}
	}
	return []keyHint{
		{"↵/space", "launch agent"},
		{"t", "open in tab"},
		{"D", "kill session"},
		{"g", "lazygit"},
		{"j/k", "move"},
		{"1/2", "switch panel"},
		{"q", "quit"},
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
			marker = chevronStyle.Render("▸ ")
		}
		line := marker + o.style.Render(o.label)
		if i == p.cursor {
			line = selectLine(line, width)
		}
		lines = append(lines, line)
	}
	lines = append(lines, "", keyStyle.Render("↵")+dimStyle.Render(" launch  ")+keyStyle.Render("esc")+dimStyle.Render(" cancel"))
	return lines
}

// helpHeight is the body-row count of the keybind panel: the taller of the two
// sections' hint lists, so the panel stays a constant height and the dashboard
// doesn't jump when switching panels (the shorter list pads with blank rows).
func helpHeight() int {
	s, p := len(helpHints(sectionSessions)), len(helpHints(sectionProjects))
	if s > p {
		return s
	}
	return p
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

	var sLines, pLines []string
	for i, r := range rows {
		line := renderRow(r, i == cursor, m.collapsed, contentWidth)
		if r.section == sectionSessions {
			sLines = append(sLines, line)
		} else {
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

	// The bottom panel is normally the context-sensitive keybind hints, but it
	// becomes the agent picker while one is open.
	bottomTitle, bottomBody := "Keys", renderHelp(focused)
	if m.prompt != nil {
		bottomTitle = "Launch agent · " + m.prompt.title
		bottomBody = renderPrompt(m.prompt, contentWidth)
	}

	// Reserve the bottom rows for that panel, but drop it on very short terminals
	// so the lists keep usable height — unless the picker is open, which always
	// needs to be visible.
	hh := helpHeight() + 2
	avail := m.height - hh
	if avail < 6 && m.prompt == nil {
		hh, avail = 0, m.height
	}

	// Size the sessions panel to its content (up to half the remaining height);
	// the projects panel absorbs the rest.
	sh := len(sLines) + 2
	if max := avail / 2; sh > max {
		sh = max
	}
	if sh < 3 {
		sh = 3
	}
	ph := avail - sh
	if ph < 3 {
		ph = 3
	}

	projTitle := "[2]─Projects"
	if m.scopeName != "" {
		projTitle = "[2]─Project · " + m.scopeName
	}
	sessions := panel("[1]─Sessions", sLines, m.width, sh, focused == sectionSessions)
	projects := panel(projTitle, pLines, m.width, ph, focused == sectionProjects)
	parts := []string{sessions, projects}
	if hh > 0 {
		parts = append(parts, panel(bottomTitle, bottomBody, m.width, hh, m.prompt != nil))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// renderRow renders a tree row: indent, chevron, optional mark/badge, label.
// Selected rows get a full-width background highlight.
func renderRow(r row, selected bool, collapsed map[string]bool, width int) string {
	var b strings.Builder
	b.WriteString(strings.Repeat("  ", r.depth))
	if r.collapsible {
		if collapsed[r.key] {
			b.WriteString(chevronStyle.Render("▸ "))
		} else {
			b.WriteString(chevronStyle.Render("▾ "))
		}
	} else {
		b.WriteString("  ")
	}
	if r.mark != "" {
		b.WriteString(r.mark + " ")
	}
	if r.badge != "" {
		b.WriteString(r.badge + " ")
	}
	b.WriteString(r.label)

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
func selectLine(line string, width int) string {
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+selectedOpenSeq)
	return selectedStyle.Width(width).Render(line)
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
