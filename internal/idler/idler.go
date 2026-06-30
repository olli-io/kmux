// Package idler implements kmux-idler, the one-shot launcher picker kmux uses for
// its idle slots. It is shipped as a separate binary (cmd/kmux-idler).
//
// Crucially it is NOT a resting process: an idle slot is held by a tiny shell
// loop (see internal/layout.placeholderCmd) that just draws a hint and blocks on a
// single keypress, so an idle pane costs a shell, not a Go runtime. kmux-idler is
// only spawned for the brief moment the user is actually choosing what to launch,
// then exits. It takes an agent kind as its argument:
//
//	kmux-idler            ↵ path: pick a project, then pick the kind
//	kmux-idler claude     pick a project, launch Claude in it
//	kmux-idler opencode   pick a project, launch OpenCode in it
//
// On selection it creates the agent's tmux session detached (see Start) and exits,
// returning the slot to its idle hint; the running dashboard then gives the new
// session a kmux-managed pane on its next poll. Creating the session detached
// rather than becoming it in place keeps kmux in sole control of the splits, so the
// column layout always matches kitty's geometry.
package idler

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/olli-io/kmux/internal/agent"
	"github.com/olli-io/kmux/internal/kitty"
	"github.com/olli-io/kmux/internal/project"
	"github.com/olli-io/kmux/internal/tmux"
)

// Launch is the agent the picker chose: the resolved tmux session name, the
// directory to start it in, and the agent command to run. main turns it into a
// detached tmux session (see Start).
type Launch struct {
	Session  string
	Dir      string
	AgentCmd string
}

// Run shows the launcher picker for the given agent kind and blocks until the
// user picks something or cancels. kind is "" (ask for the kind after the
// project), "claude", or "opencode"; any other value is an error. It returns the
// chosen Launch, or nil when the user cancelled. AltScreen keeps the slot clean
// (the shell's idle hint is restored when the picker exits).
func Run(kind string) (*Launch, error) {
	switch kind {
	case "", "claude", "opencode":
	default:
		return nil, fmt.Errorf("unknown agent kind %q (want claude or opencode)", kind)
	}
	fm, err := tea.NewProgram(newModel(kind), tea.WithAltScreen()).Run()
	if err != nil {
		return nil, err
	}
	return fm.(model).launch, nil
}

// Start creates the agent's tmux session detached (running the agent command in
// dir) and returns, leaving the picker to exit so the idle slot's shell loop
// redraws its hint. The running kmux dashboard then picks up the new session on
// its next poll and gives it a kmux-managed pane — kmux owns every split, so the
// layout always matches kitty's geometry. (kmux-idler used to exec the tmux client
// in place and let the dashboard adopt that window; that in-place adoption was
// dropped because kitty's remote control can't reveal a user-split pane's geometry,
// which let manual splits desync the column model.)
func Start(l *Launch) error {
	return tmux.NewDetachedSession(l.Session, l.Dir, l.AgentCmd)
}

// minLayoutPanes is the floor the idle slot's quit key protects: the dashboard
// sidebar plus its fixed agent columns (layout.maxColumns). At or below it, `q` is
// inert — those panes are the core layout, and closing a core placeholder would
// just make the dashboard respawn it. Hardcoded rather than imported to keep idler
// free of an import cycle with layout (layout already imports idler).
const minLayoutPanes = 4 // sidebar + 3 agent columns

// spareWindow returns this idle pane's kitty window id and whether its tab holds
// more panes than the core layout (minLayoutPanes) — i.e. there is a spare pane
// `q` could close. spare is false (with no error) outside kitty. Shared by
// QuitIfSpare and CanQuit so the quit action and its hint use one rule.
func spareWindow() (id int, spare bool, err error) {
	id, err = strconv.Atoi(os.Getenv("KITTY_WINDOW_ID"))
	if err != nil {
		return 0, false, nil // not in a kitty window
	}
	n, err := kitty.WindowsInTab(id)
	if err != nil {
		return 0, false, err
	}
	return id, n > minLayoutPanes, nil
}

// QuitIfSpare closes the idle pane this process runs in, but only when its kitty
// tab holds more panes than the core layout (minLayoutPanes) — i.e. there is a
// spare pane the user added. It backs the idle loop's `q` key, letting the user
// dismiss an extra idle slot while never being able to quit away the dashboard and
// its three columns. It is a no-op outside kitty or when no spare pane exists.
func QuitIfSpare() error {
	id, spare, err := spareWindow()
	if err != nil || !spare {
		return err
	}
	return kitty.CloseWindow(id)
}

// CanQuit reports whether QuitIfSpare would actually close this pane — i.e. there
// is a spare pane beyond the core layout. The idle loop calls it (via the
// `--can-quit` flag) to show the `q` hint only when quitting would do something.
// Returns false with no error outside kitty or when no spare pane exists.
func CanQuit() (bool, error) {
	_, spare, err := spareWindow()
	return spare, err
}

// RunIdleLoop replaces the current process with the interactive idle-slot loop,
// run via `sh -c`. It is the entry point for `kmux-idler --idle-loop`: kmux sends
// it into a blank pane the user spawned so that pane behaves exactly like one of
// the dashboard's managed idle slots (the same hint, the same launch-on-keypress).
// The loop spawns this very binary for each launch, so it resolves its own path
// (through any symlink, mirroring layout's idler discovery). Exec only returns on
// failure.
func RunIdleLoop() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	shPath, err := exec.LookPath("sh")
	if err != nil {
		return err
	}
	return syscall.Exec(shPath, []string{"sh", "-c", IdleLoopScript(self)}, os.Environ())
}

// IdleLoopScript is the shell program that holds an interactive idle slot. It
// loops: draw the hint, read one raw keypress, and spawn the kmux-idler picker for
// the matching launch flow (c/o preselect a kind; any other key — Enter included —
// runs the kind-after-project flow; q quits the slot if it is spare). The `q` hint
// is shown only when `kmux-idler --can-quit` reports a spare pane (see CanQuit), so
// it never advertises an inert key. The raw single-byte read (stty + dd) matches
// the pattern kmux already uses for hold-on-error prompts. The picker exits as soon
// as it launches or is cancelled, returning the slot to this cheap loop. idlerPath
// is interpolated as a shell-quoted absolute path. It backs both layout's
// placeholder panes and `kmux-idler --idle-loop` (see RunIdleLoop).
func IdleLoopScript(idlerPath string) string {
	q := "'" + strings.ReplaceAll(idlerPath, "'", `'\''`) + "'"
	return `idler=` + q + `
while :; do
  quit=''
  "$idler" --can-quit 2>/dev/null && quit='  \033[33mq\033[0m \033[2mquit\033[0m\n'
  printf '\033[2J\033[H\n  \033[1;34mkmux\033[0m \033[2midle\033[0m\n\n  \033[33m↵\033[0m \033[2mlaunch agent\033[0m\n  \033[33mc\033[0m \033[2mclaude\033[0m\n  \033[33mo\033[0m \033[2mopencode\033[0m\n%b' "$quit"
  old=$(stty -g 2>/dev/null)
  stty -icanon -echo min 1 time 0 2>/dev/null
  key=$(dd bs=1 count=1 2>/dev/null)
  [ -n "$old" ] && stty "$old" 2>/dev/null
  case "$key" in
    c|C) "$idler" claude ;;
    o|O) "$idler" opencode ;;
    q|Q) "$idler" --quit ;;
    *) "$idler" ;;
  esac
done`
}

func newModel(kind string) model {
	return model{mode: modeProject, pendingKind: kind}
}

// Styles mirror the dashboard's palette (see internal/tui/render.go) so the picker
// reads as part of the same UI. They are intentionally redeclared here rather than
// imported: the idler must not pull in the heavyweight tui package.
var (
	clStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))  // claude (blue)
	ocStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("213")) // opencode (pink)
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	keyStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // keybind hint (yellow)
	selStyle    = lipgloss.NewStyle().Background(lipgloss.Color("238"))
	borderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240")) // box border (grey)
)

// selOpenSeq is selStyle's background as a bare SGR open sequence, derived (not
// hardcoded) so it tracks the color above. selectLine re-emits it after inner
// ANSI resets so a highlighted row stays one solid color.
var selOpenSeq = func() string {
	const sentinel = "\x00"
	if open, _, ok := strings.Cut(selStyle.Render(sentinel), sentinel); ok {
		return open
	}
	return ""
}()

// mode is the picker's current screen.
type mode int

const (
	modeProject mode = iota // the project picker (the entry screen)
	modeKind                // the claude/opencode picker (only on the ↵ path)
)

// target is one launchable project or linked worktree in the picker, mirroring a
// row of the dashboard's [1]-Projects panel. session is its claude session name
// (the base agent.SessionForKind rewrites for the chosen kind), so a session the
// idler plants is byte-identical to one the dashboard would create.
type target struct {
	label   string // display label: project name, or "project/worktree"
	branch  string // current branch, shown dim
	dir     string // working directory the agent launches in
	session string // claude session name (base for agent.SessionForKind)
}

// kindOption is one agent kind offered by the ↵-path kind picker.
type kindOption struct {
	kind, label string
	style       lipgloss.Style
}

var kindOptions = []kindOption{
	{"claude", "Claude", clStyle},
	{"opencode", "OpenCode", ocStyle},
}

type model struct {
	mode    mode
	targets []target
	pcursor int // index into targets (project picker)
	kcursor int // index into kindOptions (kind picker)

	// pendingKind is the agent kind chosen before the project: "claude"/"opencode"
	// when invoked for a specific kind (launch directly on select), "" when invoked
	// for the ↵ path (ask for the kind after the project).
	pendingKind string
	chosen      *target // project awaiting a kind on the ↵ path
	launch      *Launch // set once the user confirms; the picker then quits

	width, height int
}

// messages
type projectsMsg struct{ targets []target }

func (m model) Init() tea.Cmd {
	return scanCmd(m.pendingKind)
}

// scanCmd scans projects off the UI goroutine. It reuses project.ScanProjects so
// the idler's list matches the dashboard's Projects panel exactly, configured
// extra folders included, then drops any project/worktree whose agent session is
// already running (see buildTargets). Both the scan and the running-session
// listing fall back to empty on error rather than failing the picker.
func scanCmd(kind string) tea.Cmd {
	return func() tea.Msg {
		ps, _ := project.ScanProjects()
		running, _ := tmux.ListAgentSessions()
		return projectsMsg{targets: buildTargets(ps, kind, running)}
	}
}

// buildTargets flattens scanned projects into the picker's launch list: each
// project's main worktree, then each of its linked worktrees, in scan order.
// Targets whose agent session is already running are dropped — an idle slot is
// for launching new work, and the dashboard already surfaces active sessions.
// running comes from tmux.ListAgentSessions, which lists every live tmux session,
// so detached sessions count as occupied too. For a preselected kind only that
// kind's session marks a target occupied; on the ↵ path (kind == "") a target is
// dropped only once every kind is running, leaving any free kind launchable.
func buildTargets(ps []project.Project, kind string, running []string) []target {
	live := make(map[string]bool, len(running))
	for _, s := range running {
		live[s] = true
	}
	var ts []target
	add := func(label, branch, dir, session string) {
		if occupied(session, kind, live) {
			return
		}
		ts = append(ts, target{label: label, branch: branch, dir: dir, session: session})
	}
	for _, p := range ps {
		add(p.Name, p.Branch, p.Path, agent.ExpectedSession(p.Path, ""))
		for _, w := range p.Worktrees {
			add(p.Name+"/"+w.Name, w.Branch, w.Path, agent.ExpectedSession(p.Path, w.Name))
		}
	}
	return ts
}

// occupied reports whether a target's session is already running for the kind the
// picker would launch. session is the base claude session name; live is the set
// of running agent session names. With a preselected kind it checks just that
// kind's session; on the ↵ path (kind == "") it reports occupied only when every
// offered kind is already running, so a target with a free kind stays listed.
func occupied(session, kind string, live map[string]bool) bool {
	if kind != "" {
		return live[agent.SessionForKind(session, kind)]
	}
	for _, o := range kindOptions {
		if !live[agent.SessionForKind(session, o.kind)] {
			return false
		}
	}
	return true
}

// chooseLaunch records the chosen agent (resolved session/dir/command) and quits
// the picker; main then creates it as a detached tmux session. The session name
// follows kmux's naming convention (agent.SessionForKind on the target's claude
// session), so the session the dashboard later attaches is exactly this one.
func (m model) chooseLaunch(t target, kind string) (tea.Model, tea.Cmd) {
	m.launch = &Launch{
		Session:  agent.SessionForKind(t.session, kind),
		Dir:      t.dir,
		AgentCmd: agent.AgentCommand(kind),
	}
	return m, tea.Quit
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case projectsMsg:
		m.targets = msg.targets
		if m.pcursor >= len(m.targets) {
			m.pcursor = 0
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.mode == modeKind {
		return m.handleKind(msg)
	}
	return m.handleProject(msg)
}

// handleProject drives the project picker. esc/q (and ctrl+c) cancel the picker
// entirely — the idle slot's shell loop redraws its hint. Selecting a project
// either launches directly (a kind was preselected) or advances to the kind
// picker (the ↵ path).
func (m model) handleProject(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "esc", "q", "h", "left":
		return m, tea.Quit
	case "j", "down":
		if m.pcursor < len(m.targets)-1 {
			m.pcursor++
		}
	case "k", "up":
		if m.pcursor > 0 {
			m.pcursor--
		}
	case "enter", " ", "l", "right":
		if len(m.targets) == 0 {
			return m, nil
		}
		t := m.targets[m.pcursor]
		if m.pendingKind != "" {
			return m.chooseLaunch(t, m.pendingKind)
		}
		// ↵ path: pick the kind next.
		m.chosen = &t
		m.kcursor = 0
		m.mode = modeKind
	}
	return m, nil
}

func (m model) handleKind(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q", "h", "left":
		m.mode = modeProject // back to the project list
	case "j", "down":
		if m.kcursor < len(kindOptions)-1 {
			m.kcursor++
		}
	case "k", "up":
		if m.kcursor > 0 {
			m.kcursor--
		}
	case "tab":
		m.kcursor = (m.kcursor + 1) % len(kindOptions)
	case "enter", " ", "l", "right":
		if m.chosen != nil {
			return m.chooseLaunch(*m.chosen, kindOptions[m.kcursor].kind)
		}
	}
	return m, nil
}

func (m model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	if m.mode == modeKind {
		return m.center(m.kindBox())
	}
	return m.center(m.projectBox())
}

// center places content in the middle of the pane.
func (m model) center(content string) string {
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
}

// projectBox renders the project picker, sized to its content.
func (m model) projectBox() string {
	launchWord := "launch"
	if m.pendingKind == "" {
		launchWord = "next" // ↵ leads to the kind picker, not a launch
	}
	title := "Select project"
	hint := keyStyle.Render("↵") + dimStyle.Render(" "+launchWord+"  ") +
		keyStyle.Render("esc") + dimStyle.Render(" cancel")

	inner := max(lipgloss.Width(title), lipgloss.Width(hint))
	for _, t := range m.targets {
		if w := lipgloss.Width("  " + targetLabel(t)); w > inner {
			inner = w
		}
	}
	inner = clampInner(inner+2, m.width)

	body := m.projectRows(inner)
	body = append(body, "", hint)
	return box(title, body, inner+2)
}

// projectRows builds the (possibly windowed) project list, the selected row
// highlighted full-width. The visible window is bounded by the pane height so the
// box never grows past the slot.
func (m model) projectRows(inner int) []string {
	if len(m.targets) == 0 {
		return []string{dimStyle.Render("no projects found")}
	}
	// Box chrome is the two borders plus the blank+hint footer: 4 rows.
	height := m.height - 4
	if height < 1 {
		height = 1
	}
	start, end := scrollWindow(len(m.targets), m.pcursor, height)
	rows := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		marker := "  "
		if i == m.pcursor {
			marker = keyStyle.Render("› ")
		}
		line := marker + targetLabel(m.targets[i])
		if i == m.pcursor {
			line = selectLine(line, inner)
		}
		rows = append(rows, line)
	}
	return rows
}

// targetLabel renders a target's label with its dim branch tail.
func targetLabel(t target) string {
	if t.branch == "" {
		return t.label
	}
	return t.label + dimStyle.Render("  "+t.branch)
}

// kindBox renders the claude/opencode picker shown after a project is chosen on
// the ↵ path.
func (m model) kindBox() string {
	title := "Launch agent"
	if m.chosen != nil {
		title = "Launch · " + m.chosen.label
	}
	hint := keyStyle.Render("↵") + dimStyle.Render(" launch  ") +
		keyStyle.Render("esc") + dimStyle.Render(" back")

	inner := max(lipgloss.Width(title), lipgloss.Width(hint))
	for _, o := range kindOptions {
		if w := lipgloss.Width("  " + o.label); w > inner {
			inner = w
		}
	}
	inner = clampInner(inner+2, m.width)

	body := make([]string, 0, len(kindOptions)+2)
	for i, o := range kindOptions {
		marker := "  "
		if i == m.kcursor {
			marker = keyStyle.Render("› ")
		}
		line := marker + o.style.Render(o.label)
		if i == m.kcursor {
			line = selectLine(line, inner)
		}
		body = append(body, line)
	}
	body = append(body, "", hint)
	return box(title, body, inner+2)
}

// scrollWindow returns the [start,end) slice of n rows to show in height rows,
// keeping cursor roughly centered and clamped to the ends. With room for every
// row it returns the whole range.
func scrollWindow(n, cursor, height int) (start, end int) {
	if height >= n {
		return 0, n
	}
	start = cursor - height/2
	if start < 0 {
		start = 0
	}
	end = start + height
	if end > n {
		end = n
		start = end - height
	}
	return start, end
}

// clampInner bounds a desired inner box width to at least 1 and at most what fits
// inside the pane's vertical borders.
func clampInner(inner, width int) int {
	if maxInner := width - 2; inner > maxInner {
		inner = maxInner
	}
	if inner < 1 {
		inner = 1
	}
	return inner
}

// selectLine paints a composed row with the selection background, re-emitting the
// background after each inner ANSI reset so it stays one solid bar, and sizing the
// line to exactly width first (lipgloss .Width would wrap an over-long line).
// Mirrors the dashboard's selectLine.
func selectLine(line string, width int) string {
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+selOpenSeq)
	return selStyle.Render(padCell(line, width))
}

// box draws a rounded, titled frame in the default border color.
func box(title string, body []string, width int) string {
	return boxStyled(title, body, width, borderStyle)
}

// boxStyled draws a rounded, titled frame sized to width (border included),
// padding or clipping each body line to the inner width, with border/title color
// bs. A pared-down copy of the dashboard's box.
func boxStyled(title string, body []string, width int, bs lipgloss.Style) string {
	inner := width - 2
	if inner < 1 {
		inner = 1
	}
	ts := bs.Bold(true)
	if maxTitle := inner - 2; maxTitle >= 1 && lipgloss.Width(title) > maxTitle {
		title = ansi.Truncate(title, maxTitle, "…")
	}
	fill := inner - lipgloss.Width(title) - 1 // leading "─" before the title
	if fill < 0 {
		fill = 0
	}
	out := make([]string, 0, len(body)+2)
	out = append(out, bs.Render("╭─")+ts.Render(title)+bs.Render(strings.Repeat("─", fill)+"╮"))
	for _, raw := range body {
		out = append(out, bs.Render("│")+padCell(raw, inner)+bs.Render("│"))
	}
	out = append(out, bs.Render("╰"+strings.Repeat("─", inner)+"╯"))
	return strings.Join(out, "\n")
}

// padCell pads (or clips) s to exactly w display columns, ANSI-aware.
func padCell(s string, w int) string {
	sw := lipgloss.Width(s)
	if sw > w {
		return ansi.Truncate(s, w, "")
	}
	return s + strings.Repeat(" ", w-sw)
}
