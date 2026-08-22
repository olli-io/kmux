// Package idler implements kmux-idler, the one-shot launcher picker kmux uses for
// its idle slots. It is shipped as a separate binary (cmd/kmux-idler).
//
// It is not a resting process: an idle slot is held by a tiny shell loop that just
// draws a hint and blocks on a keypress, so kmux-idler is only spawned for the brief
// moment the user is choosing what to launch. It takes an agent kind as its argument:
//
//	kmux-idler            ↵ path: pick a project, then pick the kind
//	kmux-idler claude     pick a project, launch Claude in it
//	kmux-idler opencode   pick a project, launch OpenCode in it
//
// On selection it launches the agent in place (see Start), execing the tmux client
// into the same kitty window; the dashboard then adopts that window as the session's
// managed pane on its next poll.
package idler

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/olli-io/kmux/internal/agent"
	"github.com/olli-io/kmux/internal/config"
	"github.com/olli-io/kmux/internal/kitty"
	"github.com/olli-io/kmux/internal/project"
	"github.com/olli-io/kmux/internal/status"
	"github.com/olli-io/kmux/internal/tmux"
)

// Launch is the agent the picker chose, launched in the current idle pane (see Start).
type Launch struct {
	Session  string
	Dir      string
	AgentCmd string
}

// Run shows the launcher picker and blocks until the user picks or cancels,
// returning the chosen Launch or nil on cancel. kind is "", "claude", or
// "opencode"; any other value is an error.
func Run(kind string) (*Launch, error) {
	switch kind {
	case "", "claude", "opencode":
	default:
		return nil, fmt.Errorf("unknown agent kind %q (want claude or opencode)", kind)
	}
	fm, err := tea.NewProgram(newModel(kind)).Run()
	if err != nil {
		return nil, err
	}
	return fm.(model).launch, nil
}

// Start launches the chosen agent in place: it execs the session's tmux client
// into the same kitty window. `tmux new-session -A` creates-or-attaches atomically,
// so the session appears to the dashboard the instant its client occupies the pane.
// The adopt hint is written before exec — before the session even exists — so the
// dashboard adopts this exact window rather than opening a second pane. On failure
// Start falls back to a detached session so the launch is not lost.
func Start(l *Launch) error {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return tmux.NewDetachedSession(l.Session, l.Dir, l.AgentCmd)
	}
	wid, hasWID := kittyWindowID()
	if hasWID {
		_ = writeAdoptHint(wid, l.Session)
	}
	// new-session -A attaches or creates; foreground (no -d) makes this pane the client.
	argv := []string{"tmux", "new-session", "-A", "-s", l.Session, "-c", l.Dir, l.AgentCmd}
	if err := syscall.Exec(tmuxPath, argv, os.Environ()); err != nil {
		if hasWID {
			_ = RemoveAdoptHint(wid) // exec failed: drop the hint so the fallback isn't mis-adopted
		}
		return tmux.NewDetachedSession(l.Session, l.Dir, l.AgentCmd)
	}
	return nil // unreachable: exec replaced the process
}

func kittyWindowID() (int, bool) {
	id, err := strconv.Atoi(os.Getenv("KITTY_WINDOW_ID"))
	if err != nil {
		return 0, false
	}
	return id, true
}

// adoptDir holds adopt hints, one file per kitty window named by window id. It uses
// config.ConfigDir — resolved from $XDG_CONFIG_HOME/$HOME alone, unlike
// $XDG_RUNTIME_DIR which `kitten @ launch` need not propagate — so a hint the idler
// writes is always found by the dashboard.
func adoptDir() string {
	if dir, err := config.ConfigDir(); err == nil {
		return filepath.Join(dir, "adopt")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("kmux-%d", os.Getuid()), "adopt")
}

// writeAdoptHint records that a kitty window is launching a session, so the
// dashboard adopts that window instead of opening a second pane.
func writeAdoptHint(windowID int, session string) error {
	dir := adoptDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, strconv.Itoa(windowID)), []byte(session), 0o600)
}

// ReadAdoptHints returns the adopt hints as window id -> session name; a missing
// directory yields an empty map, not an error.
func ReadAdoptHints() (map[int]string, error) {
	entries, err := os.ReadDir(adoptDir())
	if err != nil {
		if os.IsNotExist(err) {
			return map[int]string{}, nil
		}
		return nil, err
	}
	out := make(map[int]string, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		id, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(adoptDir(), e.Name()))
		if err != nil {
			continue
		}
		if s := strings.TrimSpace(string(data)); s != "" {
			out[id] = s
		}
	}
	return out, nil
}

// RemoveAdoptHint deletes a window's adopt hint (a no-op if absent).
func RemoveAdoptHint(windowID int) error {
	err := os.Remove(filepath.Join(adoptDir(), strconv.Itoa(windowID)))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// minLayoutPanes is the floor the idle slot's quit key protects: sidebar plus fixed
// agent columns. Hardcoded rather than imported from layout to avoid an import cycle
// (layout already imports idler).
const minLayoutPanes = 4 // sidebar + 3 agent columns

// spareWindow returns this pane's kitty window id and whether its tab holds a spare
// pane beyond the core layout. Shared by QuitIfSpare and CanQuit so the quit action
// and its hint use one rule. spare is false (no error) outside kitty.
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

// QuitIfSpare closes this idle pane, but only when its tab holds a spare pane beyond
// the core layout, so the user can never quit away the dashboard's columns. Backs
// the idle loop's `q` key.
func QuitIfSpare() error {
	id, spare, err := spareWindow()
	if err != nil || !spare {
		return err
	}
	return kitty.CloseWindow(id)
}

// CanQuit reports whether QuitIfSpare would close this pane. The idle loop calls it
// (via --can-quit) to show the `q` hint only when quitting would do something.
func CanQuit() (bool, error) {
	_, spare, err := spareWindow()
	return spare, err
}

// RunIdleLoop execs the interactive idle-slot loop, the entry point for
// `kmux-idler --idle-loop`: kmux sends it into a user-spawned blank pane so it
// behaves like a managed idle slot. The loop spawns this binary for each launch, so
// it resolves its own path through any symlink.
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

// IdleLoopScript is the shell program that holds an interactive idle slot: it draws
// the hint, reads one raw keypress, and spawns the picker for the matching flow (c/o
// preselect a kind, Enter asks for the kind after the project, q quits a spare slot).
// The `q` hint shows only when --can-quit reports a spare pane. Backs both layout's
// placeholder panes and `kmux-idler --idle-loop`.
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
    '') "$idler" ;;
  esac
done`
}

func newModel(kind string) model {
	return model{mode: modeProject, pendingKind: kind}
}

// Styles mirror the dashboard's palette, redeclared here rather than imported so
// the idler doesn't pull in the heavyweight tui package.
var (
	clStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))  // claude (blue)
	ocStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("213")) // opencode (pink)
	ocTagStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))   // running [OC] tag (red)
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	keyStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // keybind hint (yellow)
	selStyle    = lipgloss.NewStyle().Background(lipgloss.Color("238"))
	borderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240")) // box border (grey)
)

// selOpenSeq is selStyle's background as a bare SGR open sequence, derived so it
// tracks the color above. selectLine re-emits it after inner ANSI resets to keep a
// highlighted row one solid color.
var selOpenSeq = func() string {
	const sentinel = "\x00"
	if open, _, ok := strings.Cut(selStyle.Render(sentinel), sentinel); ok {
		return open
	}
	return ""
}()

type mode int

const (
	modeProject mode = iota // the project picker (the entry screen)
	modeKind                // the claude/opencode picker (only on the ↵ path)
)

// target is one project or linked worktree in the picker, mirroring a row of the
// dashboard's Projects panel. Its session name matches what the dashboard would
// create, so an idler-planted session is byte-identical.
type target struct {
	label   string          // display label: project name, or "project/worktree"
	branch  string          // current branch, shown dim
	dir     string          // working directory the agent launches in
	session string          // claude session name (base for agent.SessionForKind)
	running map[string]bool // agent kind -> its session is already live
}

// disabledFor reports whether selecting this target would duplicate a running
// session. On the ↵ path (kind == "") it is disabled only once every offered kind
// is running, since the kind picker can still launch any free kind.
func (t target) disabledFor(kind string) bool {
	if kind != "" {
		return t.running[kind]
	}
	for _, o := range kindOptions {
		if !t.running[o.kind] {
			return false
		}
	}
	return true
}

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

	// pendingKind is the kind chosen before the project, or "" for the ↵ path that
	// asks for the kind after.
	pendingKind string
	chosen      *target // project awaiting a kind on the ↵ path
	launch      *Launch // set once the user confirms; the picker then quits

	width, height int
}

// messages
type projectsMsg struct{ targets []target }

func (m model) Init() tea.Cmd {
	return scanCmd()
}

// scanCmd builds the picker's project list off the UI goroutine. The idler is a
// one-shot process, so a live project.ScanProjects would re-spend dozens of serial
// git calls (a worktree-list + status per repo under ~/git) on every launch — the
// delay the user feels when picking a project. Instead it reads the list the
// running dashboard cached to disk after its last full scan (status.LoadProjects),
// which matches the dashboard's Projects panel exactly (config extras included) and
// costs a single file read. Only when no cache exists yet (no dashboard has run) or
// it is empty does it fall back to a live scan, so a first launch still works.
//
// The list mirrors the dashboard's last scan, so its dirty/ahead/behind flags may
// lag until the dashboard next writes the cache; the running-session tagging, which
// decides what the picker greys out, is always live (tmux.ListAgentSessions, one
// cheap call). Both the load and the session listing fall back to empty on error
// rather than failing the picker.
func scanCmd() tea.Cmd {
	return func() tea.Msg {
		ps, ok, _ := status.LoadProjects()
		if !ok {
			ps, _ = project.ScanProjects()
		}
		running, _ := tmux.ListAgentSessions()
		return projectsMsg{targets: buildTargets(ps, running)}
	}
}

// buildTargets flattens projects into the launch list: each main worktree then its
// linked worktrees, in scan order. Running targets are greyed rather than dropped
// (so the user sees active work but can't duplicate it) and hoisted stably to the
// top.
func buildTargets(ps []project.Project, running []string) []target {
	live := make(map[string]bool, len(running))
	for _, s := range running {
		live[s] = true
	}
	var ts []target
	add := func(label, branch, dir, session string) {
		r := make(map[string]bool, len(kindOptions))
		for _, o := range kindOptions {
			r[o.kind] = live[agent.SessionForKind(session, o.kind)]
		}
		ts = append(ts, target{label: label, branch: branch, dir: dir, session: session, running: r})
	}
	for _, p := range ps {
		add(p.Name, p.Branch, p.Path, agent.ExpectedSession(p.Path, ""))
		for _, w := range p.Worktrees {
			add(p.Name+"/"+w.Name, w.Branch, w.Path, agent.ExpectedSession(p.Path, w.Name))
		}
	}
	sort.SliceStable(ts, func(i, j int) bool {
		return ts[i].hasRunning() && !ts[j].hasRunning()
	})
	return ts
}

func (t target) hasRunning() bool {
	for _, o := range kindOptions {
		if t.running[o.kind] {
			return true
		}
	}
	return false
}

// chooseLaunch records the chosen agent and quits the picker; the session name
// follows kmux's naming convention so the dashboard later adopts exactly this one.
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
		if !m.selectable(m.pcursor) {
			m.pcursor = m.firstSelectable()
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.mode == modeKind {
		return m.handleKind(msg)
	}
	return m.handleProject(msg)
}

// handleProject drives the project picker. Cursor moves skip disabled rows;
// selecting a project either launches directly (kind preselected) or advances to
// the kind picker (the ↵ path).
func (m model) handleProject(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "esc", "q", "h", "left":
		return m, tea.Quit
	case "j", "down":
		m.pcursor = m.nextSelectable(m.pcursor, 1)
	case "k", "up":
		m.pcursor = m.nextSelectable(m.pcursor, -1)
	case "enter", "space", "l", "right":
		if !m.selectable(m.pcursor) {
			return m, nil // empty list or a greyed-out running row
		}
		t := m.targets[m.pcursor]
		if m.pendingKind != "" {
			return m.chooseLaunch(t, m.pendingKind)
		}
		// ↵ path: pick the kind next, starting on the first free kind.
		m.chosen = &t
		m.mode = modeKind
		m.kcursor = m.firstKind()
	}
	return m, nil
}

// selectable reports whether target i is in range and launchable for the pending kind.
func (m model) selectable(i int) bool {
	return i >= 0 && i < len(m.targets) && !m.targets[i].disabledFor(m.pendingKind)
}

// nextSelectable returns the next launchable index from just past `from` in
// direction step, or `from` when none is found (disabled rows skipped).
func (m model) nextSelectable(from, step int) int {
	for i := from + step; i >= 0 && i < len(m.targets); i += step {
		if m.selectable(i) {
			return i
		}
	}
	return from
}

// firstSelectable returns the first launchable index, or 0 when every target is
// disabled (the cursor then rests on a greyed row and enter is inert).
func (m model) firstSelectable() int {
	for i := range m.targets {
		if m.selectable(i) {
			return i
		}
	}
	return 0
}

func (m model) handleKind(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q", "h", "left":
		m.mode = modeProject // back to the project list
	case "j", "down":
		m.kcursor = m.nextKind(m.kcursor, 1)
	case "k", "up":
		m.kcursor = m.nextKind(m.kcursor, -1)
	case "tab":
		m.kcursor = m.nextKindWrap(m.kcursor)
	case "enter", "space", "l", "right":
		if m.chosen != nil && m.kindSelectable(m.kcursor) {
			return m.chooseLaunch(*m.chosen, kindOptions[m.kcursor].kind)
		}
	}
	return m, nil
}

// kindSelectable reports whether kind option i is launchable for the chosen project.
func (m model) kindSelectable(i int) bool {
	if i < 0 || i >= len(kindOptions) || m.chosen == nil {
		return false
	}
	return !m.chosen.running[kindOptions[i].kind]
}

// firstKind returns the first launchable kind index, or 0 when none is free (a
// project only reaches the kind picker with a free kind, so 0 shouldn't arise).
func (m model) firstKind() int {
	for i := range kindOptions {
		if m.kindSelectable(i) {
			return i
		}
	}
	return 0
}

// nextKind returns the next launchable kind scanning from just past `from` in
// direction step, or `from` unchanged when none is found (running kinds skipped).
func (m model) nextKind(from, step int) int {
	for i := from + step; i >= 0 && i < len(kindOptions); i += step {
		if m.kindSelectable(i) {
			return i
		}
	}
	return from
}

// nextKindWrap advances to the next launchable kind, wrapping past the end, for
// the tab key.
func (m model) nextKindWrap(from int) int {
	for off := 1; off <= len(kindOptions); off++ {
		if i := (from + off) % len(kindOptions); m.kindSelectable(i) {
			return i
		}
	}
	return from
}

// View wraps the rendered frame; AltScreen keeps the picker on its own screen
// so the pane's prior contents are restored when it exits.
func (m model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m model) render() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	if m.mode == modeKind {
		return m.center(m.kindBox())
	}
	return m.center(m.projectBox())
}

func (m model) center(content string) string {
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
}

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
		if w := lipgloss.Width("  " + targetLabel(t, t.disabledFor(m.pendingKind))); w > inner {
			inner = w
		}
	}
	inner = clampInner(inner+2, m.width)

	body := m.projectRows(inner)
	body = append(body, "", hint)
	return box(title, body, inner+2)
}

// projectRows builds the (windowed) project list, the selected row highlighted
// full-width. The window is bounded by the pane height so the box never overgrows.
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
		disabled := m.targets[i].disabledFor(m.pendingKind)
		// The cursor never rests on a disabled row, so only enabled rows get selected.
		selected := i == m.pcursor && !disabled
		marker := "  "
		if selected {
			marker = keyStyle.Render("› ")
		}
		line := marker + targetLabel(m.targets[i], disabled)
		if selected {
			line = selectLine(line, inner)
		}
		rows = append(rows, line)
	}
	return rows
}

// targetLabel renders a target's label with its dim branch tail and a running-kind
// marker for each live kind. A disabled target is dimmed whole.
func targetLabel(t target, disabled bool) string {
	tail := ""
	if t.branch != "" {
		tail = "  " + t.branch
	}
	head := t.label + dimStyle.Render(tail)
	if disabled {
		head = dimStyle.Render(t.label + tail)
	}
	if tags := runningTags(t); tags != "" {
		head += "  " + tags
	}
	return head
}

// runningTags renders the running-kind markers for t, in kindOptions order.
func runningTags(t target) string {
	var tags []string
	for _, o := range kindOptions {
		if t.running[o.kind] {
			tags = append(tags, kindTag(o.kind))
		}
	}
	return strings.Join(tags, " ")
}

// kindTag renders a running-agent marker: grey brackets around the kind's short code.
func kindTag(kind string) string {
	mark, style := "CC", clStyle
	if kind == "opencode" {
		mark, style = "OC", ocTagStyle
	}
	return dimStyle.Render("[") + style.Render(mark) + dimStyle.Render("]")
}

// kindOptionLabel renders one kind-picker row: the colored name, or dimmed with a
// running marker when that kind is already live for the chosen project.
func (m model) kindOptionLabel(i int, o kindOption) string {
	if m.kindSelectable(i) {
		return o.style.Render(o.label)
	}
	return dimStyle.Render(o.label) + "  " + kindTag(o.kind)
}

// kindBox renders the kind picker shown after a project is chosen on the ↵ path.
func (m model) kindBox() string {
	title := "Launch agent"
	if m.chosen != nil {
		title = "Launch · " + m.chosen.label
	}
	hint := keyStyle.Render("↵") + dimStyle.Render(" launch  ") +
		keyStyle.Render("esc") + dimStyle.Render(" back")

	inner := max(lipgloss.Width(title), lipgloss.Width(hint))
	for i, o := range kindOptions {
		if w := lipgloss.Width("  " + m.kindOptionLabel(i, o)); w > inner {
			inner = w
		}
	}
	inner = clampInner(inner+2, m.width)

	body := make([]string, 0, len(kindOptions)+2)
	for i, o := range kindOptions {
		selected := i == m.kcursor && m.kindSelectable(i)
		marker := "  "
		if selected {
			marker = keyStyle.Render("› ")
		}
		line := marker + m.kindOptionLabel(i, o)
		if selected {
			line = selectLine(line, inner)
		}
		body = append(body, line)
	}
	body = append(body, "", hint)
	return box(title, body, inner+2)
}

// scrollWindow returns the [start,end) slice of n rows to show in height rows,
// keeping the cursor roughly centered and clamped to the ends.
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

// selectLine paints a row with the selection background, re-emitting it after each
// inner ANSI reset so it stays one solid bar. Mirrors the dashboard's selectLine.
func selectLine(line string, width int) string {
	line = strings.ReplaceAll(line, ansi.ResetStyle, ansi.ResetStyle+selOpenSeq)
	return selStyle.Render(padCell(line, width))
}

// box draws a rounded, titled frame in the default border color.
func box(title string, body []string, width int) string {
	return boxStyled(title, body, width, borderStyle)
}

// boxStyled draws a rounded, titled frame sized to width, padding or clipping each
// body line to the inner width. A pared-down copy of the dashboard's box.
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
