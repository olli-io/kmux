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
	spinning      bool                             // whether the spinner ticker is currently armed (only while a session is busy)
	prompt        *agentPrompt                     // non-nil while the agent-picker overlay is open
	commands      []config.CustomCommand           // user-configurable command keybindings (editor, lazygit, …)
	cmdErr        *commandError                    // non-nil while a failed-command error float is shown
	lastErr       string
	width, height int

	// idledPanes records blank panes already handled so each becomes an idle
	// launcher at most once. blankSeeded holds conversion until the first snapshot,
	// so only panes appearing after kmux started become launchers.
	idledPanes  map[int]bool
	blankSeeded bool

	// orphanStrikes counts consecutive polls a session's anchor directory has been
	// missing; it's killed at orphanConfirmPolls, and reset if the directory
	// reappears or the session leaves the list.
	orphanStrikes map[string]int

	// scanning is set while a project rescan is in flight; the project ticker skips
	// a tick while it's set, so a slow scan can't stack concurrent copies.
	scanning bool

	// keys is the action→key map for the Keys footer; keyAction is its reverse
	// (key→action), built so the first action listed wins a shared key. conflicts
	// holds duplicate-key report lines, shown in place of the hints when non-empty.
	keys      map[string]string
	keyAction map[string]string
	conflicts []string

	// scopeDir is the main-worktree path kmux is scoped to; scopeName is its project
	// name. Both empty in the default, unscoped mode, which shows all projects.
	scopeDir  string
	scopeName string

	// tabAttn is the last-applied [!!] attention state of the kitty tab title.
	// The attentionMsg handler re-titles the tab only when this flips, so the
	// dashboard doesn't spawn a `kitten @` on every poll. It starts false,
	// matching the no-attention title Init sets at launch.
	tabAttn bool

	// launcherID is the kitty window id of the [kmux][launcher] splash tab the
	// dashboard was spawned behind (0 when none — e.g. the splash failed to open).
	// launched guards the one-shot dismissal: once the layout is ready and the
	// minimum hold has elapsed (or the cap-timer fallback fires) the finished
	// dashboard is focused and this tab closed, and launched is set so later
	// reconciles don't try again. layoutReady/minHeld are the two gates the normal
	// dismissal waits on (see dismissLauncherWhenReady).
	launcherID  int
	launched    bool
	layoutReady bool // first reconcile settled: panes built and sized
	minHeld     bool // launcherMin elapsed since launch
}

// commandError backs the error float for a failed command, dismissed by any keypress.
type commandError struct {
	title string
	msg   string
}

// agentPrompt is the agent-picker state shown when launching a project.
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

// NewModel builds the dashboard model. scopeDir scopes it to one project's main
// worktree; launcherID is the splash tab it builds behind, closed once the first
// reconcile settles.
func NewModel(mgr *layout.Manager, scopeDir string, launcherID int) tea.Model {
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
	// Config is best-effort; a read error falls back to defaults. Seeding the tracker
	// with the persisted clocks lets pre-launch idle time keep counting.
	cfg, _ := config.LoadConfig()
	keys := cfg.Keybindings
	reserved := reservedKeys(keys)
	return model{
		mgr:           mgr,
		collapsed:     map[string]bool{},
		idledPanes:    map[int]bool{},
		orphanStrikes: map[string]int{},
		detached:      detached,
		attention:     map[string]status.AttentionState{},
		idle:          status.NewIdleTrackerFrom(cfg.IdleDuration(), idle),
		commands:      effectiveCommands(cfg.CustomCommands, reserved),
		keys:          keys,
		keyAction:     keyActionMap(keys),
		conflicts:     cfg.KeybindingConflicts(),
		scopeDir:      scopeDir,
		scopeName:     scopeName,
		launcherID:    launcherID,
	}
}

// reservedKeys are the keys user commands cannot override: every resolved
// keybinding plus the fixed 1, 2, and ctrl+c.
func reservedKeys(keys map[string]string) map[string]bool {
	reserved := map[string]bool{"1": true, "2": true, "ctrl+c": true}
	for _, k := range keys {
		reserved[k] = true
	}
	return reserved
}

// keyActionMap inverts action→key into key→action, iterating in canonical order
// so the first-listed action wins a shared key.
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
	// Kick off an immediate poll and full project scan, then tick each on its own
	// interval. This projectsCmd is the one full ~/git sweep; later project ticks
	// rescan only active projects. The spinner ticker is armed later, only while a
	// session is busy.
	cmds := []tea.Cmd{pollCmd(), projectsCmd(m.scopeDir), tickCmd(), projectTickCmd()}
	// Label the dashboard's kitty tab immediately (no attention yet), so it's
	// identifiable in the tab bar before any session exists — mirroring how
	// main.go titles the sidebar window at launch. The attentionMsg handler takes
	// over the [!!] marker from here as sessions change state.
	cmds = append(cmds, setTabTitleCmd(m.mgr.SidebarID(), m.scopeDir, false))
	// When the dashboard is building behind a launch-overlay splash tab, arm a
	// fallback cap: if the first reconcile never completes (e.g. the tmux poll
	// errored and short-circuited before reconcile), the cap timer still dismisses
	// the splash so it can't linger over a stalled launch.
	if m.launcherID != 0 {
		cmds = append(cmds, launcherCapCmd(), launcherMinCmd())
	}
	return tea.Batch(cmds...)
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

// activeProjectPaths returns the distinct main-worktree paths of projects with a
// running session (attached or detached) — the set the steady-state ticker
// rescans instead of sweeping all of ~/git. Ungrouped sessions are skipped.
func (m model) activeProjectPaths() []string {
	paths := agent.ProjectPaths(m.projects)
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, s := range m.sessions {
		if p, _, ok := agent.MatchProject(s, paths); ok && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// mergeProjects patches existing with fresh copies from updates (matched by Path),
// leaving projects absent from updates untouched. Order is preserved, so the panel
// doesn't reshuffle on a partial refresh.
func mergeProjects(existing, updates []project.Project) []project.Project {
	if len(updates) == 0 {
		return existing
	}
	byPath := make(map[string]project.Project, len(updates))
	for _, p := range updates {
		byPath[p.Path] = p
	}
	out := make([]project.Project, len(existing))
	for i, p := range existing {
		if fresh, ok := byPath[p.Path]; ok {
			out[i] = fresh
		} else {
			out[i] = p
		}
	}
	return out
}

// attachable is m.sessions minus detached sessions: the set reconcile keeps panes
// for. A detached session stays listed and running but has no pane.
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

// scopedSessions restricts a session list to the scoped project, returning it
// unchanged in unscoped mode. Matching mirrors groupSessions.
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

func (m model) hasSession(name string) bool {
	return slices.Contains(m.sessions, name)
}

// trackOrphans advances the confirmation counters from a poll's missing set,
// returning sessions missing for orphanConfirmPolls consecutive polls. One whose
// directory reappears or leaves the list is reset, so a transient miss can't reap.
func (m *model) trackOrphans(orphaned []string) []string {
	missing := make(map[string]bool, len(orphaned))
	for _, s := range orphaned {
		missing[s] = true
	}
	var kill []string
	for _, s := range m.sessions {
		if !missing[s] {
			delete(m.orphanStrikes, s)
			continue
		}
		m.orphanStrikes[s]++
		if m.orphanStrikes[s] >= orphanConfirmPolls {
			delete(m.orphanStrikes, s)
			kill = append(kill, s)
		}
	}
	// Drop counters for sessions no longer listed so the map can't leak stale entries.
	for s := range m.orphanStrikes {
		if !slices.Contains(m.sessions, s) {
			delete(m.orphanStrikes, s)
		}
	}
	return kill
}

// projectLive reports a project's aggregate live state for row coloring, across
// both agent kinds: liveAttached if any session has a live pane, liveDetached if
// all running sessions are detached, liveNone if none is running.
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

// killTarget returns the session a `d` press should kill for row r: a leaf kills
// itself, a project/worktree row kills its running session. "" when nothing to kill.
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

// lazygitDir returns the directory to open lazygit in for row r: a project/worktree
// row carries its dir; a session leaf resolves it from the name. "" when none.
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
	// Sessions-panel group header: collapse key is "sess:<projectPath>".
	return m.projectPath(strings.TrimPrefix(r.key, "sess:"), "")
}

// editorDir reuses lazygitDir and falls back to the project root, so a
// multi-worktree folder header opens its editor too.
func (m model) editorDir(r *row) string {
	if dir := m.lazygitDir(r); dir != "" {
		return dir
	}
	return m.projectRoot(r)
}

// commandVars builds the placeholder values for user commands on row r. Every key
// is always present (empty when it doesn't apply), so a command never sees a
// literal {name}:
//
//	{dir}          working directory of the row (the command's cwd)
//	{project}      project name (e.g. kmux)
//	{worktree}     linked-worktree name, empty on the main worktree
//	{project_root} main-worktree path of the project
//	{tmux_session} agent session name ([kmux][CC|OC]<projectPath>[@<worktree>])
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

// projectRoot returns the main-worktree path of the project Projects-panel row r
// belongs to, for any of its rows (header, main leaf, or worktree leaf). "" when
// no project matches.
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

// sessionDir resolves an agent session's working directory by matching its name
// against the scanned projects. "" when no project matches.
func (m model) sessionDir(name string) string {
	proj, wt, ok := agent.MatchProject(name, agent.ProjectPaths(m.projects))
	if !ok {
		return ""
	}
	return m.projectPath(proj, wt)
}

// projectPath returns the directory for a scanned project by its main-worktree
// path (wt == "") or a linked worktree (wt == its session-form segment). "" when
// none matches.
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
