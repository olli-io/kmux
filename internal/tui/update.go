package tui

import (
	"path/filepath"
	"reflect"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/olli-io/kmux/internal/agent"
	"github.com/olli-io/kmux/internal/config"
	"github.com/olli-io/kmux/internal/status"
	"github.com/olli-io/kmux/internal/tmux"
)

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tickMsg:
		// Keep the tick alive but skip the tmux round-trips while kmux's tab is in
		// the background: with no one watching the session list, polling it just
		// burns list-sessions/capture-pane calls. FocusMsg resumes real polling.
		if !m.focused {
			return m, tickCmd()
		}
		return m, tea.Batch(pollCmd(m.mgr), tickCmd())

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
		// Resume tmux polling (paused on blur) and poll once immediately so the
		// session list and attention glyphs are current the instant the tab shows
		// again, rather than waiting for the next tick.
		if !m.focused {
			m.focused = true
			cmd = tea.Batch(cmd, pollCmd(m.mgr))
		}
		return m, cmd

	case tea.BlurMsg:
		// kmux's tab lost focus; pause tmux polling until it's refocused. The tick
		// keeps firing but stops shelling out to tmux (see tickMsg).
		m.focused = false
		return m, nil

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
		m.panes = tmux.Agents(msg.snap)
		pruned := m.pruneDetached()
		// Refresh attention off the freshest session list (drives the session glyphs).
		cmd := tea.Batch(reconcileCmd(m.mgr, msg.snap, msg.seq, m.attachable()), attentionCmd(m.sessions, m.panes))
		if pruned {
			cmd = tea.Batch(cmd, m.saveStateCmd())
		}
		// Kill sessions whose repo/worktree has been deleted, once confirmed missing
		// across enough polls. killSessionCmd re-lists, so the panel and panes update.
		for _, name := range m.trackOrphans(msg.orphaned) {
			cmd = tea.Batch(cmd, killSessionCmd(m.mgr, m.panes[name]))
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
		return m, nil

	case reconciledMsg:
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
		// Persist the idle clocks when they changed, so a restart resumes them and
		// the launch sweep can reap sessions that stayed idle while kmux was off.
		var cmds []tea.Cmd
		if idle := m.idle.Snapshot(); !reflect.DeepEqual(idle, m.lastIdle) {
			m.lastIdle = idle
			cmds = append(cmds, m.saveStateCmd())
		}
		for _, name := range kill {
			cmds = append(cmds, killSessionCmd(m.mgr, m.panes[name]))
		}
		// Start the busy spinner on the idle->busy transition; this is the sole place
		// it restarts. The spinning guard prevents arming a second ticker.
		if !m.spinning && anyBusy(m.attention) {
			m.spinning = true
			cmds = append(cmds, spinnerCmd())
		}
		// Rename the grid window only when the [!!] attention flag flips.
		if attn := anyNeedsAttention(m.attention); attn != m.tabAttn {
			m.tabAttn = attn
			cmds = append(cmds, setWindowNameCmd(m.mgr.Window(), m.scopeDir, attn))
		}
		// Mirror the [!!] marker onto each session's own tmux session name, so it shows
		// in tmux's status bar / choose-tree too — but only on a per-session flip, so a
		// `tmux rename-session` doesn't run every poll. A permission session gets the
		// marker; any other state (including a session that left the poll) clears it.
		cmds = append(cmds, m.sessionAttnCmds(msg.states)...)
		return m, tea.Batch(cmds...)

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

// handleKey processes navigation and fold keys (arrows + vim).
func (m model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
		_ = m.mgr.Close()
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
		_ = m.mgr.Close()
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
		// Detach a session leaf: move its pane out of the grid into a session of its
		// own. The agent continues to run. Marking it detached keeps reconcile from
		// moving it back in.
		if r := rowAt(rows, m.cursor); isSessionLeaf(r) && !m.detached[r.session] {
			m.detached[r.session] = true
			return m, tea.Batch(reconcileCmd(m.mgr, nil, 0, m.attachable()), m.saveStateCmd())
		}
	case config.ActionKillAgent:
		// Kill the agent: removes its pane and thus ends the agent.
		if name := m.killTarget(rowAt(rows, m.cursor)); name != "" {
			return m, killSessionCmd(m.mgr, m.panes[name])
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
		// Open the selected project's root/main worktree in a new tmux window
		// running its own kmux. Projects panel only (no-op for session rows).
		if dir := m.projectRoot(rowAt(rows, m.cursor)); dir != "" {
			return m, openTabCmd(dir)
		}

	case config.ActionRefreshProjects:
		// Force a full git rescan of every project on demand, the same sweep a
		// refocus triggers. Useful when status changed from outside kmux (a commit,
		// push, or checkout in another terminal) and the user wants it reflected now.
		cmd := m.refreshProjectsCmd()
		return m, cmd

	case config.ActionFullscreenAgent:
		// Show the selected agent at full window size (tmux zoom). Works in both
		// panels: a session leaf zooms; a project row zooms or launches.
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
	case config.ActionCreateOrFocusPi:
		// Launch (or focus) Pi for the selected project, skipping the picker.
		if cmd := m.launchKindOn(rowAt(rows, m.cursor), "pi"); cmd != nil {
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
func (m model) handleErrKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		_ = m.mgr.Close()
		return m, tea.Quit
	}
	m.cmdErr = nil
	return m, nil
}

// handlePromptKey drives the agent picker: j/k move between agents, enter/space
// launches the highlighted one, and esc/h cancels. ctrl+c still quits outright.
func (m model) handlePromptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		_ = m.mgr.Close()
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
	case "enter", "space", "l", "right":
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
// marker, matching the per-session red "!" glyph (see attentionGlyph). A merely
// finished/waiting session (AttnWaiting) shows a green "✓" in the panel but does not
// flag the tab: the marker means "a session is asking you something", not "idle".
func anyNeedsAttention(states map[string]status.AttentionState) bool {
	for _, st := range states {
		if st == status.AttnPermission {
			return true
		}
	}
	return false
}

// sessionAttnCmds returns the tmux session renames needed to bring each session's [!!]
// marker in sync with states, updating m.sessAttn in place. It emits a rename only when
// a session's marked/unmarked status changed since the last poll, so a steady state
// shells out nothing. A session that was marked but is absent from this poll
// (killed/detached out of the list) is cleared from the tracking map without a rename —
// it's gone or will re-sync when it reappears.
func (m *model) sessionAttnCmds(states map[string]status.AttentionState) []tea.Cmd {
	var cmds []tea.Cmd
	for s, st := range states {
		want := st == status.AttnPermission
		if want == m.sessAttn[s] {
			continue // no change for this session
		}
		if want {
			m.sessAttn[s] = true
		} else {
			delete(m.sessAttn, s)
		}
		cmds = append(cmds, setSessionAttnCmd(m.panes[s], s, want))
	}
	// Drop tracking for sessions that vanished from the poll so the map can't grow
	// unbounded; they're gone, so no rename is needed.
	for s := range m.sessAttn {
		if _, ok := states[s]; !ok {
			delete(m.sessAttn, s)
		}
	}
	return cmds
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

// focusOrReattach selects a session's pane, or moves the pane into the grid when
// it is not there. It clears any detached flag so reconcile keeps the pane in
// the grid, persisting the change.
func (m *model) focusOrReattach(name string) tea.Cmd {
	save := m.clearDetached(name)
	if m.attached(name) {
		return tea.Batch(focusCmd(m.panes[name]), save)
	}
	return tea.Batch(reconcileCmd(m.mgr, nil, 0, m.attachable()), save)
}

// zoomOrReattach shows a session's pane at full window size. A pane that is not
// in the grid goes into the grid first; a second press then zooms it.
func (m *model) zoomOrReattach(name string) tea.Cmd {
	if m.attached(name) {
		return zoomCmd(m.panes[name])
	}
	return m.focusOrReattach(name)
}

// openSessionTab zooms a session leaf row's pane. Returns nil when r is not a
// session leaf.
func (m *model) openSessionTab(r *row) tea.Cmd {
	if !isSessionLeaf(r) {
		return nil
	}
	return m.zoomOrReattach(r.session)
}

// launchProjectTab is the zoom counterpart of launchProject: exactly one running
// kind is zoomed directly; none or several opens the picker.
func (m *model) launchProjectTab(r *row) (tea.Cmd, bool) {
	if r == nil || r.section != sectionProjects || r.session == "" {
		return nil, false
	}
	if running := m.runningKinds(r.session); len(running) == 1 {
		return m.zoomOrReattach(running[0]), true
	}
	return m.launchProject(r)
}

// launchKindActions maps an agent kind to the keybinding action that launches it
// without going through the picker. The switch above dispatches the actions; this map
// exists so the footer hint can list the keys in registry order (see launchKindHint).
// A kind absent here simply has no dedicated key — the picker still reaches it.
var launchKindActions = map[string]string{
	"claude":   config.ActionCreateOrFocusClaude,
	"opencode": config.ActionCreateOrFocusOpencode,
	"pi":       config.ActionCreateOrFocusPi,
}

// runningKinds returns the live sessions among every agent kind of the project or
// worktree that claudeName names, in registry order. The launch paths branch on its
// length: exactly one running kind is focused directly, none or several opens the
// picker.
func (m *model) runningKinds(claudeName string) []string {
	var running []string
	for _, name := range kindSessions(claudeName) {
		if m.hasSession(name) {
			running = append(running, name)
		}
	}
	return running
}

// launchProject activates a project/worktree leaf row: exactly one running kind
// focuses it directly, none or several opens the picker. Returns (nil, false) for rows
// it doesn't act on (folder headers, non-project rows) so callers fall through to fold.
func (m *model) launchProject(r *row) (tea.Cmd, bool) {
	if r.section != sectionProjects || r.session == "" {
		return nil, false
	}
	running := m.runningKinds(r.session)
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
	return m.launchKind(p.session, p.dir, promptOptions[p.cursor].kind)
}

// launchKind focuses the given kind's session if running, otherwise creates and
// attaches one. session is the base name for agent.SessionForKind.
func (m *model) launchKind(session, dir, kind string) tea.Cmd {
	name := agent.SessionForKind(session, kind)
	if m.hasSession(name) {
		return m.focusOrReattach(name)
	}
	return openSessionCmd(m.mgr, name, dir, agent.AgentCommand(kind, dir))
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
