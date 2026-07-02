package tui

import (
	"path/filepath"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/olli-io/kmux/internal/agent"
	"github.com/olli-io/kmux/internal/config"
	"github.com/olli-io/kmux/internal/layout"
	"github.com/olli-io/kmux/internal/project"
	"github.com/olli-io/kmux/internal/status"
)

type model struct {
	mgr           *layout.Manager
	sessions      []string
	projects      []project.Project
	collapsed     map[string]bool                  // collapse key -> collapsed
	detached      map[string]bool                  // session name -> pane detached (tmux still running)
	attention     map[string]status.AttentionState // session name -> latest detected attention state
	idle          status.IdleTracker               // per-session pane-stability clock for idle-kill
	cursor        int                              // index into rows()
	spinnerFrame  int                              // animation frame for busy-session glyphs
	prompt        *agentPrompt                     // non-nil while the agent-picker overlay is open
	commands      []config.CustomCommand           // user-configurable command keybindings (editor, lazygit, …)
	cmdErr        *commandError                    // non-nil while a failed-command error float is shown
	lastErr       string
	width, height int

	// idledPanes tracks kitty window ids of user-spawned blank panes kmux has
	// already handled, so each is converted into an idle launcher at most once.
	// blankSeeded gates that conversion on having taken a first snapshot: panes
	// present before kmux started are recorded but left alone, so only panes that
	// appear afterwards ("new" panes) become launchers.
	idledPanes  map[int]bool
	blankSeeded bool

	// scanning is set while a project rescan (projectsCmd) is in flight and cleared
	// when its projectsMsg lands. The project ticker skips a tick while it's set, so
	// a scan that outruns projectInterval can't stack concurrent copies — the pileup
	// that made the old poll-cadence scan spawn git processes faster than they
	// finished.
	scanning bool

	// keys is the resolved action→key map (config.KeyActions → key) used to label
	// the Keys footer. keyAction is its reverse (key→action), built in KeyActions
	// order so the first action listed wins a shared key; the dispatch in handleKey
	// switches on it. conflicts holds any duplicate-key report lines from
	// config.KeybindingConflicts, shown in place of the hints while non-empty.
	keys      map[string]string
	keyAction map[string]string
	conflicts []string

	// scopeDir is the main-worktree path kmux is scoped to (from the CLI
	// directory argument); scopeName is its project name. Both empty in the
	// default, unscoped mode. When set, the Projects panel shows only that
	// project and the Sessions panel only its sessions.
	scopeDir  string
	scopeName string
}

// commandError backs the error float for a failed command: title names it, msg
// is the failure. Dismissed by any keypress.
type commandError struct {
	title string
	msg   string
}

// agentPrompt is the state of the agent-picker shown when launching a project:
// it offers a choice of agent (claude / opencode) for the selected project row.
type agentPrompt struct {
	title   string // human label for the project/worktree being launched
	session string // the row's claude session name (base for agent.SessionForKind)
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

// NewModel builds the dashboard model. scopeDir, when non-empty, is the main
// worktree of the single project kmux is scoped to (see model.scopeDir).
func NewModel(mgr *layout.Manager, scopeDir string) tea.Model {
	// Restore detached sessions and idle clocks from a previous run; best-effort
	// (a read error just starts with empty sets).
	detached, idle, err := status.LoadState()
	if err != nil || detached == nil {
		detached, idle = map[string]bool{}, map[string]status.IdleRecord{}
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
	keys := cfg.Keybindings
	reserved := reservedKeys(keys)
	return model{
		mgr:        mgr,
		collapsed:  map[string]bool{},
		idledPanes: map[int]bool{},
		detached:   detached,
		attention:  map[string]status.AttentionState{},
		idle:       status.NewIdleTrackerFrom(cfg.IdleDuration(), idle),
		commands:   effectiveCommands(cfg.CustomCommands, reserved),
		keys:       keys,
		keyAction:  keyActionMap(keys),
		conflicts:  cfg.KeybindingConflicts(),
		scopeDir:   scopeDir,
		scopeName:  scopeName,
	}
}

// reservedKeys is the set of keys user commands cannot override: every resolved
// keybinding plus the always-fixed 1, 2, and ctrl+c. A configured command landing
// on one is dropped by effectiveCommands, so the fixed/rebindable action wins and
// the command never shows in the Keys footer.
func reservedKeys(keys map[string]string) map[string]bool {
	reserved := map[string]bool{"1": true, "2": true, "ctrl+c": true}
	for _, k := range keys {
		reserved[k] = true
	}
	return reserved
}

// keyActionMap inverts the action→key map into key→action for dispatch. It
// iterates config.KeyActions in canonical order so that when two actions share a
// key the first-listed action wins (matching KeybindingConflicts' resolution).
func keyActionMap(keys map[string]string) map[string]string {
	out := make(map[string]string, len(keys))
	for _, action := range config.KeyActions() {
		if k := keys[action]; k != "" {
			if _, taken := out[k]; !taken {
				out[k] = action
			}
		}
	}
	return out
}

// effectiveCommands drops command bindings that can't take effect: those with an
// empty cmd line and those whose key collides with a reserved binding.
func effectiveCommands(cmds []config.CustomCommand, reserved map[string]bool) []config.CustomCommand {
	out := make([]config.CustomCommand, 0, len(cmds))
	for _, c := range cmds {
		if c.Cmd == "" || reserved[c.Key] {
			continue
		}
		out = append(out, c)
	}
	return out
}

// focusedSection reports which panel holds the cursor, matching View's logic.
func (m model) focusedSection(rows []row) section {
	if r := rowAt(rows, m.cursor); r != nil {
		return r.section
	}
	return sectionSessions
}

// panelName maps a section to the config panel-scope name used by CustomCommand.Matches.
func panelName(s section) string {
	if s == sectionProjects {
		return "projects"
	}
	return "sessions"
}

func (m model) Init() tea.Cmd {
	// Kick off an immediate poll and project scan, then tick each on its own
	// interval — sessions on the fast pollInterval, projects on the slow
	// projectInterval (see projectTickCmd). The first blank-pane scan seeds the set
	// of pre-existing shells so only later ones become launchers; blankTickCmd then
	// re-scans on its own faster ticker.
	return tea.Batch(pollCmd(), projectsCmd(m.scopeDir), blankPanesCmd(m.mgr.SidebarID()), tickCmd(), blankTickCmd(), projectTickCmd(), spinnerCmd())
}

// rows builds the combined, navigable row list: session rows first, then
// project rows. The cursor indexes into this slice.
func (m model) rows() []row {
	deco := rowDeco{spinner: m.spinnerFrame}
	names := agent.ProjectPaths(m.projects)
	sess := buildSessionRows(m.sessions, names, m.collapsed, m.attention, m.mgr.Attached, m.isDetached, deco)
	proj := buildProjectRows(m.projects, m.collapsed, m.projectLive, deco)
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
	if m.scopeDir == "" {
		return names
	}
	scope := []string{m.scopeDir}
	out := make([]string, 0, len(names))
	for _, s := range names {
		if _, _, ok := agent.MatchProject(s, scope); ok {
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

// projectLive reports a project/worktree's aggregate live state for row coloring,
// considering both agent kinds. claudeName is the [kmux][CC] session name (as produced by
// agent.ExpectedSession); the opencode name is derived by marker swap. It returns
// liveAttached if any running session has a live pane, liveDetached if it has a
// running session but every one is detached, and liveNone if none is running — so
// an opencode-only project still reads as live and a detached one reads red.
func (m model) projectLive(claudeName string) liveState {
	live := liveNone
	for _, name := range [2]string{claudeName, agent.SessionForKind(claudeName, "opencode")} {
		if !m.hasSession(name) {
			continue
		}
		if m.isDetached(name) {
			live = maxLive(live, liveDetached)
		} else {
			live = maxLive(live, liveAttached)
		}
	}
	return live
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
	// Sessions-panel group header: its collapse key is "sess:<projectPath>", so
	// the project's main path is the directory to open in.
	return m.projectPath(strings.TrimPrefix(r.key, "sess:"), "")
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

// commandVars builds the placeholder values exposed to user-configured commands
// for the selected row r (see config.CustomCommand.Cmd). Every key is always present
// (empty when it doesn't apply), so a command never sees a literal {name}:
//
//	{dir}          working directory of the row (the command's cwd)
//	{project}      project name (e.g. kmux)
//	{worktree}     linked-worktree name, empty on the main worktree
//	{project_root} main-worktree path of the project
//	{tmux_session} agent session name ([kmux][CC|OC]<projectPath>[@<worktree>])
//
// dir is resolved via editorDir, so it's set for any actionable row; project,
// worktree, and project_root come from matching that dir to a scanned project.
func (m model) commandVars(r *row) map[string]string {
	dir := m.editorDir(r)
	vars := map[string]string{
		"dir":          dir,
		"project":      "",
		"worktree":     "",
		"project_root": "",
		"tmux_session": "",
	}
	if r != nil {
		vars["tmux_session"] = r.session
	}
	for _, p := range m.projects {
		if p.Path == dir {
			vars["project"], vars["project_root"] = p.Name, p.Path
			break
		}
		if w, ok := worktreeAt(p, dir); ok {
			vars["project"], vars["worktree"], vars["project_root"] = p.Name, w.Name, p.Path
			break
		}
	}
	return vars
}

// worktreeAt returns p's linked worktree whose path is dir, if any.
func worktreeAt(p project.Project, dir string) (project.Worktree, bool) {
	for _, w := range p.Worktrees {
		if w.Path == dir {
			return w, true
		}
	}
	return project.Worktree{}, false
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
// name ([kmux][CC|OC]<projectPath>[@<worktree>]) against the scanned projects,
// mirroring how sessions are grouped under projects in the tree. It returns ""
// when no project matches (e.g. ungrouped sessions).
func (m model) sessionDir(name string) string {
	proj, wt, ok := agent.MatchProject(name, agent.ProjectPaths(m.projects))
	if !ok {
		return ""
	}
	return m.projectPath(proj, wt)
}

// projectPath returns the directory for a scanned project by its main-worktree
// path (wt == "") or one of its linked worktrees (wt == the worktree's
// session-form segment), or "" when no such project/worktree exists (e.g. the
// "(ungrouped)" bucket).
func (m model) projectPath(projPath, wt string) string {
	for _, p := range m.projects {
		if p.Path != projPath {
			continue
		}
		if wt == "" {
			return p.Path
		}
		for _, w := range p.Worktrees {
			if agent.WorktreeMatches(wt, w.Name) {
				return w.Path
			}
		}
	}
	return ""
}
