package tui

import (
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/olli-io/kmux/internal/agent"
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
			busy[s] = st == status.AttnBusy
		}
		kill := m.idle.Reap(time.Now(), msg.hashes, busy)
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
// given agent kind's session in a new kitty tab, creating the tmux session first
// when it isn't already running.
func (m *model) launchKindTab(session, dir, kind string) tea.Cmd {
	name := agent.SessionForKind(session, kind)
	if m.hasSession(name) {
		return openAgentTabCmd(name, "", "")
	}
	return openAgentTabCmd(name, dir, agent.AgentCommand(kind))
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
// (the base for agent.SessionForKind), dir its working directory.
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
