package tui

import (
	"path/filepath"
	"slices"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/olli-io/kmux/internal/agent"
	"github.com/olli-io/kmux/internal/config"
	"github.com/olli-io/kmux/internal/kitty"
	"github.com/olli-io/kmux/internal/layout"
	"github.com/olli-io/kmux/internal/status"
)

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tickMsg:
		return m, tea.Batch(pollCmd(), tickCmd())

	case projectTickMsg:
		// Skip firing a scan while one is still in flight so a slow scan can't stack
		// concurrent copies; always re-arm the ticker. Recurring ticks rescan only the
		// active projects; scoped mode rescans its single project.
		cmd := projectTickCmd()
		if !m.scanning {
			switch {
			case m.scopeDir != "":
				m.scanning = true
				cmd = tea.Batch(cmd, projectsCmd(m.scopeDir))
			default:
				if paths := m.activeProjectPaths(); len(paths) > 0 {
					m.scanning = true
					cmd = tea.Batch(cmd, activeProjectsCmd(paths))
				}
				// Nothing running: skip this tick, leaving m.projects as-is.
			}
		}
		return m, cmd

	case tea.FocusMsg:
		// kmux's tab was refocused. Projects with no running session are otherwise
		// only scanned at startup, so their git status goes stale while the tab sits
		// in the background. Trigger a full rescan so the dashboard is fresh the
		// moment the user looks at it.
		cmd := m.refreshProjectsCmd()
		return m, cmd

	case spinnerMsg:
		m.spinnerFrame++
		// Stop the ticker when nothing is busy to avoid a re-render every interval for
		// nothing; the attentionMsg handler restarts it on the next busy session.
		if !anyBusy(m.attention) {
			m.spinning = false
			return m, nil
		}
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
		// Kill sessions whose repo/worktree has been deleted, once confirmed missing
		// across enough polls. killSessionCmd re-lists, so the panel and panes update.
		for _, name := range m.trackOrphans(msg.orphaned) {
			cmd = tea.Batch(cmd, killSessionCmd(name))
		}
		return m, cmd

	case projectsMsg:
		m.scanning = false // scan finished; the next project tick may fire another
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, nil
		}
		// A partial refresh patches the rescanned projects into m.projects by path so
		// projects with no running session keep their status; a full sweep replaces.
		if msg.partial {
			m.projects = mergeProjects(m.projects, msg.projects)
			return m, nil
		}
		m.projects = msg.projects
		// A full, unscoped sweep is the only time m.projects holds the complete
		// ~/git set; persist it so a freshly-spawned idler can paint from the cache
		// instead of re-scanning every repo. Scoped mode holds a single project and
		// must not clobber the full cache.
		if m.scopeDir != "" {
			return m, nil
		}
		return m, saveProjectsCmd(m.projects)

	case reconciledMsg:
		if len(msg.errs) > 0 {
			m.lastErr = msg.errs[0].Error()
		}
		// The first reconcile means the panes are built; mark the layout ready and drop
		// the splash once the minimum hold has also elapsed. Later reconciles no-op.
		m.layoutReady = true
		cmd := m.dismissLauncherWhenReady()
		// Adopt user-spawned blank panes off this reconcile's snapshot. Only a scanned
		// reconcile (the poll) feeds the handler; the others never seed or convert.
		if msg.scanned {
			if blankCmd := m.handleBlankPanes(msg.blanks); blankCmd != nil {
				cmd = tea.Batch(cmd, blankCmd)
			}
		}
		return m, cmd

	case launcherMinMsg:
		// The minimum hold elapsed; dismiss the splash once the layout is also ready.
		m.minHeld = true
		cmd := m.dismissLauncherWhenReady()
		return m, cmd

	case launcherCapMsg:
		// Fallback: dismiss the splash even if no reconcile ever completed, ignoring
		// the min-hold gate so a stalled launch can't strand it.
		cmd := m.dismissLauncher()
		return m, cmd

	case launcherDismissedMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
		}
		return m, nil

	case attentionMsg:
		m.attention = msg.states // display-only: no reconcile, no pane churn
		// Reap agent sessions whose pane has sat unchanged past idleTimeout, freeing
		// the memory their idle agent processes hold.
		busy := make(map[string]bool, len(msg.states))
		for s, st := range msg.states {
			busy[s] = st == status.AttnBusy
		}
		kill := m.idle.Reap(time.Now(), msg.hashes, busy)
		// Persist the freshly advanced idle clocks so a restart resumes them and
		// the launch sweep can reap sessions that stayed idle while kmux was off.
		cmds := []tea.Cmd{m.saveStateCmd()}
		for _, name := range kill {
			cmds = append(cmds, killSessionCmd(name))
		}
		// Start the busy spinner on the idle->busy transition; this is the sole place
		// it restarts. The spinning guard prevents arming a second ticker.
		if !m.spinning && anyBusy(m.attention) {
			m.spinning = true
			cmds = append(cmds, spinnerCmd())
		}
		// Re-title the kitty tab only when the [!!] attention flag flips, so the
		// dashboard doesn't spawn a `kitten @` (a ~30ms cold start) every poll.
		if attn := anyNeedsAttention(m.attention); attn != m.tabAttn {
			m.tabAttn = attn
			cmds = append(cmds, setTabTitleCmd(m.mgr.SidebarID(), m.scopeDir, attn))
		}
		return m, tea.Batch(cmds...)

	case idleConvertedMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
		}
		return m, nil

	case focusedMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
		}
		return m, nil

	case commandErrMsg:
		// Float a dismissible error rather than using the bottom-of-panel line.
		if msg.err != nil {
			m.cmdErr = &commandError{title: msg.title, msg: msg.err.Error()}
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

// handleBlankPanes converts newly appeared blank panes into idle launchers. The
// first call only seeds idledPanes, so panes already open at startup are left
// untouched; each pane is converted at most once.
func (m *model) handleBlankPanes(panes []kitty.BlankPane) tea.Cmd {
	idlerPath := layout.IdlerPath()
	if idlerPath == "" {
		return nil // no helper to launch; leave the user's panes alone
	}
	var cmds []tea.Cmd
	for _, p := range panes {
		if m.idledPanes[p.ID] {
			continue
		}
		m.idledPanes[p.ID] = true
		if m.blankSeeded {
			cmds = append(cmds, convertBlankPaneCmd(m.mgr, p, idlerPath))
		}
	}
	m.blankSeeded = true
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// dismissLauncher tears down the splash tab, or returns nil when there's no splash
// or it was already dismissed. One-shot: it marks launched so the reveal happens
// exactly once, whichever trigger fires first.
func (m *model) dismissLauncher() tea.Cmd {
	if m.launcherID == 0 || m.launched {
		return nil
	}
	m.launched = true
	return dismissLauncherCmd(m.mgr.SidebarID(), m.launcherID)
}

// dismissLauncherWhenReady tears down the splash only once both gates are met: the
// layout has settled and the minimum hold has elapsed. The second trigger to arrive
// completes the dismissal, so the splash never blinks away over the early churn.
func (m *model) dismissLauncherWhenReady() tea.Cmd {
	if !m.layoutReady || !m.minHeld {
		return nil
	}
	return m.dismissLauncher()
}

// handleKey processes navigation and fold keys (arrows + vim).
func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// A command-error float captures all input until dismissed.
	if m.cmdErr != nil {
		return m.handleErrKey(msg)
	}
	// The agent picker captures all input while open.
	if m.prompt != nil {
		return m.handlePromptKey(msg)
	}

	rows := m.rows()
	m.clampCursor(rows)

	// ctrl+c and the panel-focus digits are fixed keys that always win, handled
	// before the rebindable actions so a user binding can never shadow them.
	switch msg.String() {
	case "ctrl+c":
		m.mgr.CloseAll()
		return m, tea.Quit
	case "1":
		m.cursor = sectionStart(rows, sectionProjects)
		return m, nil
	case "2":
		m.cursor = sectionStart(rows, sectionSessions)
		return m, nil
	}

	// Dispatch on the action the pressed key resolves to (keyAction is built from
	// the resolved keybindings). An unbound key falls through to user commands.
	switch m.keyAction[msg.String()] {
	case config.ActionQuit:
		m.mgr.CloseAll()
		return m, tea.Quit

	case config.ActionNextItem, config.ActionNextItemAlt:
		if m.cursor < len(rows)-1 {
			m.cursor++
		}
	case config.ActionPrevItem, config.ActionPrevItemAlt:
		if m.cursor > 0 {
			m.cursor--
		}

	case config.ActionNextPanel, config.ActionNextPanelAlt:
		m.focusPanel(rows, true)
	case config.ActionPrevPanel, config.ActionPrevPanelAlt:
		m.focusPanel(rows, false)

	case config.ActionDetachAgent:
		// Detach a session leaf: close its kitty pane but leave the tmux session
		// running. Marking it detached keeps reconcile from re-attaching; the
		// reconcile below closes the current pane.
		if r := rowAt(rows, m.cursor); isSessionLeaf(r) && !m.detached[r.session] {
			m.detached[r.session] = true
			return m, tea.Batch(reconcileCmd(m.mgr, m.attachable()), m.saveStateCmd())
		}
	case config.ActionKillAgent:
		// Kill the agent: ends the tmux session and its pane.
		if name := m.killTarget(rowAt(rows, m.cursor)); name != "" {
			return m, killSessionCmd(name)
		}
	case config.ActionCreateOrAttachAgent:
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

	case config.ActionLaunchKmuxInProject:
		// Open the selected project's root/main worktree in a new kitty tab
		// running its own kmux. Projects panel only (no-op for session rows).
		if dir := m.projectRoot(rowAt(rows, m.cursor)); dir != "" {
			return m, openTabCmd(dir)
		}

	case config.ActionRefreshProjects:
		// Force a full git rescan of every project on demand, the same sweep a tab
		// refocus triggers. Useful when status changed from outside kmux (a commit,
		// push, or checkout in another terminal) and the user wants it reflected now.
		cmd := m.refreshProjectsCmd()
		return m, cmd

	case config.ActionFullscreenAgent:
		// Open the selected agent in its own kitty tab instead of a managed pane.
		// Works in both panels: a session leaf attaches; a project row launches.
		r := rowAt(rows, m.cursor)
		if cmd := m.openSessionTab(r); cmd != nil {
			return m, cmd
		}
		if cmd, ok := m.launchProjectTab(r); ok {
			return m, cmd
		}

	case config.ActionCreateOrFocusClaude:
		// Launch (or focus) Claude for the selected project, skipping the picker.
		if cmd := m.launchKindOn(rowAt(rows, m.cursor), "claude"); cmd != nil {
			return m, cmd
		}
	case config.ActionCreateOrFocusOpencode:
		// Launch (or focus) OpenCode for the selected project, skipping the picker.
		if cmd := m.launchKindOn(rowAt(rows, m.cursor), "opencode"); cmd != nil {
			return m, cmd
		}

	default:
		// Any other key may be a user-configured command (e.g. editor, lazygit)
		// bound for the focused panel. Fixed keys above take precedence.
		if cmd := m.runUserCommand(msg.String(), rows); cmd != nil {
			return m, cmd
		}
	}
	return m, nil
}

// refreshProjectsCmd kicks off a full git rescan of every project (all of ~/git
// plus config folders, or the single scoped project), replacing m.projects
// wholesale so idle projects get fresh status too. Guarded by m.scanning so a
// refresh can't stack a second sweep on top of an in-flight one; returns nil in
// that case, coalescing into the running scan.
func (m *model) refreshProjectsCmd() tea.Cmd {
	if m.scanning {
		return nil
	}
	m.scanning = true
	return projectsCmd(m.scopeDir)
}

// focusPanel moves the cursor to the start of the previous or next panel. It's
// written generically over the panel list so a third panel would just work.
func (m *model) focusPanel(rows []row, next bool) {
	panels := []section{sectionProjects, sectionSessions}
	cur := m.focusedSection(rows)
	idx := slices.Index(panels, cur)
	if idx < 0 {
		idx = 0
	}
	delta := 1
	if !next {
		delta = -1
	}
	target := panels[(idx+delta+len(panels))%len(panels)]
	m.cursor = sectionStart(rows, target)
}

// handleErrKey dismisses the command-error float on any keypress; ctrl+c quits.
func (m model) handleErrKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		m.mgr.CloseAll()
		return m, tea.Quit
	}
	m.cmdErr = nil
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

// isSessionLeaf reports whether r is a session leaf: a session name rather than a
// project/worktree node.
func isSessionLeaf(r *row) bool {
	return r != nil && r.section == sectionSessions && !r.collapsible
}

func anyBusy(states map[string]status.AttentionState) bool {
	for _, st := range states {
		if st == status.AttnBusy {
			return true
		}
	}
	return false
}

// anyNeedsAttention reports whether any session is blocked on a prompt awaiting a
// yes/no answer (AttnPermission) — the condition that lights the tab-title [!!]
// marker. A merely idle/finished session (AttnWaiting) does not qualify: the
// marker means "a session is asking you something", not "a session is idle".
func anyNeedsAttention(states map[string]status.AttentionState) bool {
	for _, st := range states {
		if st == status.AttnPermission {
			return true
		}
	}
	return false
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

// openOrFocusSession focuses a session leaf row's pane, re-opening one first when
// the session has none. Returns nil when r targets no session.
func (m *model) openOrFocusSession(r *row) tea.Cmd {
	name := actionSession(r)
	if name == "" {
		return nil
	}
	return m.focusOrReattach(name)
}

// focusOrReattach focuses a session's pane, re-opening one first when it has none.
// It clears any detached flag so reconcile keeps the pane, persisting the change.
func (m *model) focusOrReattach(name string) tea.Cmd {
	save := m.clearDetached(name)
	if id, ok := m.mgr.WindowID(name); ok {
		return tea.Batch(focusCmd(id), save)
	}
	return tea.Batch(reattachSessionCmd(m.mgr, name), save)
}

// openSessionTab attaches a session leaf row's session in its own kitty tab, not a
// managed pane. Returns nil when r is not a session leaf.
func (m *model) openSessionTab(r *row) tea.Cmd {
	if !isSessionLeaf(r) {
		return nil
	}
	return openAgentTabCmd(r.session, "", "")
}

// launchProjectTab is the standalone-tab counterpart of launchProject: it opens the
// row's agent in a kitty tab rather than a pane. One running kind attaches directly;
// neither or both opens the picker in tab mode.
func (m *model) launchProjectTab(r *row) (tea.Cmd, bool) {
	if r == nil || r.section != sectionProjects || r.session == "" {
		return nil, false
	}
	claude := r.session
	opencode := agent.SessionForKind(r.session, "opencode")
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
// given kind's session in a kitty tab, creating the tmux session first if needed.
func (m *model) launchKindTab(session, dir, kind string) tea.Cmd {
	name := agent.SessionForKind(session, kind)
	if m.hasSession(name) {
		return openAgentTabCmd(name, "", "")
	}
	return openAgentTabCmd(name, dir, agent.AgentCommand(kind))
}

// launchProject activates a project/worktree leaf row: one running kind focuses it
// directly, neither or both opens the picker. Returns (nil, false) for rows it
// doesn't act on (folder headers, non-project rows) so callers fall through to fold.
func (m *model) launchProject(r *row) (tea.Cmd, bool) {
	if r.section != sectionProjects || r.session == "" {
		return nil, false
	}
	claude := r.session
	opencode := agent.SessionForKind(r.session, "opencode")
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

// confirmPrompt launches the agent picker's selected kind and clears the picker.
func (m *model) confirmPrompt() tea.Cmd {
	p := m.prompt
	m.prompt = nil
	if p.tab {
		return m.launchKindTab(p.session, p.dir, promptOptions[p.cursor].kind)
	}
	return m.launchKind(p.session, p.dir, promptOptions[p.cursor].kind)
}

// launchKind focuses the given kind's session if running, otherwise creates and
// attaches one. session is the base name for agent.SessionForKind.
func (m *model) launchKind(session, dir, kind string) tea.Cmd {
	name := agent.SessionForKind(session, kind)
	// Opening a session clears any detached flag so reconcile keeps its pane;
	// persist the change when there was one.
	save := m.clearDetached(name)
	if id, ok := m.mgr.WindowID(name); ok {
		return tea.Batch(focusCmd(id), save)
	}
	return openSessionCmd(m.mgr, name, dir, agent.AgentCommand(kind))
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

// sectionStart returns the index of the first row in sec, or 0 if absent.
func sectionStart(rows []row, sec section) int {
	for i, r := range rows {
		if r.section == sec {
			return i
		}
	}
	return 0
}
