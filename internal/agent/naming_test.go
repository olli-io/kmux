package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/olli-io/kmux/internal/config"
)

// ccPre / ocPre / piPre are the claude / opencode / pi agent prefixes spelled out for
// test literals (the [kmux] tag plus the bracketed kind marker).
const (
	ccPre = "[kmux][CC]"
	ocPre = "[kmux][OC]"
	piPre = "[kmux][PI]"
)

func homeOrSkip(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	return home
}

func TestExpectedSession(t *testing.T) {
	home := homeOrSkip(t)
	kmux := filepath.Join(home, "git", "kmux")
	if got := ExpectedSession(kmux, ""); got != ccPre+"~/git/kmux" {
		t.Errorf("main session = %q, want %q", got, ccPre+"~/git/kmux")
	}
	if got := ExpectedSession(kmux, "feat"); got != ccPre+"~/git/kmux@feat" {
		t.Errorf("worktree session = %q, want %q", got, ccPre+"~/git/kmux@feat")
	}
	// A '.' in the path is tmux-sanitized to '_'.
	dotted := filepath.Join(home, "git", "my.proj")
	if got := ExpectedSession(dotted, ""); got != ccPre+"~/git/my_proj" {
		t.Errorf("dotted session = %q, want %q", got, ccPre+"~/git/my_proj")
	}
	// Paths outside $HOME keep their absolute form.
	if got := ExpectedSession("/opt/x", ""); got != ccPre+"/opt/x" {
		t.Errorf("non-home session = %q, want %q", got, ccPre+"/opt/x")
	}
}

func TestSessionForKind(t *testing.T) {
	if got := SessionForKind(ccPre+"~/git/kmux", "claude"); got != ccPre+"~/git/kmux" {
		t.Errorf("claude = %q, want %q", got, ccPre+"~/git/kmux")
	}
	if got := SessionForKind(ccPre+"~/git/kmux", "opencode"); got != ocPre+"~/git/kmux" {
		t.Errorf("opencode = %q, want %q", got, ocPre+"~/git/kmux")
	}
	if got := SessionForKind(ccPre+"~/git/kmux@feat", "opencode"); got != ocPre+"~/git/kmux@feat" {
		t.Errorf("worktree opencode = %q, want %q", got, ocPre+"~/git/kmux@feat")
	}
}

func TestMatchProject(t *testing.T) {
	home := homeOrSkip(t)
	kmux := filepath.Join(home, "git", "kmux")
	gstack := filepath.Join(home, "git", "gstack")
	gstackExtra := filepath.Join(home, "git", "gstack_extra")
	dotted := filepath.Join(home, "git", "my.proj")
	paths := []string{kmux, gstack, gstackExtra, dotted}

	cases := []struct {
		session  string
		proj, wt string
		ok       bool
	}{
		{ExpectedSession(kmux, ""), kmux, "", true},                               // main worktree
		{ExpectedSession(gstack, "feature"), gstack, "feature", true},             // project + worktree
		{ExpectedSession(gstackExtra, ""), gstackExtra, "", true},                 // exact, longest prefix wins over gstack
		{ExpectedSession(gstackExtra, "wt"), gstackExtra, "wt", true},             // longest prefix + worktree
		{ExpectedSession(dotted, ""), dotted, "", true},                           // '.'-in-path resolves to the real path
		{SessionForKind(ExpectedSession(kmux, "x"), "opencode"), kmux, "x", true}, // opencode suffix
		{ccPre + "~/git/unknown", "", "", false},                                  // no match
	}
	for _, c := range cases {
		proj, wt, ok := MatchProject(c.session, paths)
		if proj != c.proj || wt != c.wt || ok != c.ok {
			t.Errorf("MatchProject(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.session, proj, wt, ok, c.proj, c.wt, c.ok)
		}
	}
}

func TestExtractors(t *testing.T) {
	home := homeOrSkip(t)
	kmux := filepath.Join(home, "git", "kmux")

	main := ExpectedSession(kmux, "")
	if got := ProjectPath(main); got != kmux {
		t.Errorf("ProjectPath(main) = %q, want %q", got, kmux)
	}
	if got := ProjectName(main); got != "kmux" {
		t.Errorf("ProjectName(main) = %q, want %q", got, "kmux")
	}
	if got := WorktreeName(main); got != "" {
		t.Errorf("WorktreeName(main) = %q, want %q", got, "")
	}
	if got := AgentKind(main); got != "claude" {
		t.Errorf("AgentKind(main) = %q, want %q", got, "claude")
	}

	wt := ExpectedSession(kmux, "feat")
	if got := ProjectPath(wt); got != kmux {
		t.Errorf("ProjectPath(wt) = %q, want %q", got, kmux)
	}
	if got := WorktreeName(wt); got != "feat" {
		t.Errorf("WorktreeName(wt) = %q, want %q", got, "feat")
	}
	if got := AgentKind(SessionForKind(wt, "opencode")); got != "opencode" {
		t.Errorf("AgentKind(opencode) = %q, want %q", got, "opencode")
	}
}

func TestOrphanSession(t *testing.T) {
	home := homeOrSkip(t)
	dir := filepath.Join(home, "scratch", "notes")

	name := OrphanSession(dir)
	if want := ccPre + "[∅]~/scratch/notes"; name != want {
		t.Errorf("OrphanSession = %q, want %q", name, want)
	}
	if !IsOrphan(name) {
		t.Errorf("IsOrphan(%q) = false, want true", name)
	}
	// A normal (repo) session is not an orphan.
	if IsOrphan(ExpectedSession(dir, "")) {
		t.Errorf("IsOrphan(repo session) = true, want false")
	}
	// The marker leaves agent-kind detection and suffix swapping intact.
	if got := AgentKind(name); got != "claude" {
		t.Errorf("AgentKind = %q, want claude", got)
	}
	if got := SessionForKind(name, "opencode"); got != ocPre+"[∅]~/scratch/notes" {
		t.Errorf("SessionForKind = %q, want %q", got, ocPre+"[∅]~/scratch/notes")
	}
	// The path round-trips; no worktree; never binds to a project.
	if got := ProjectPath(name); got != dir {
		t.Errorf("ProjectPath = %q, want %q", got, dir)
	}
	if got := ProjectName(name); got != "notes" {
		t.Errorf("ProjectName = %q, want notes", got)
	}
	if got := WorktreeName(name); got != "" {
		t.Errorf("WorktreeName = %q, want \"\"", got)
	}
	if _, _, ok := MatchProject(name, []string{dir}); ok {
		t.Errorf("MatchProject(%q) ok = true, want false", name)
	}

	// An orphan directory containing '@' must not be parsed as a worktree.
	at := filepath.Join(home, "tmp", "foo@bar")
	atName := OrphanSession(at)
	if got := ProjectPath(atName); got != at {
		t.Errorf("ProjectPath(@-path) = %q, want %q", got, at)
	}
	if got := WorktreeName(atName); got != "" {
		t.Errorf("WorktreeName(@-path) = %q, want \"\"", got)
	}
}

func TestAgentKind(t *testing.T) {
	cases := map[string]string{
		ccPre + "~/git/proj@wt":   "claude",
		ocPre + "~/git/proj@wt":   "opencode",
		"[kmux][oc]~/git/proj@wt": "opencode", // case-insensitive
		"scratch":                 "",
		"~/git/proj[kmux][CC]":    "", // marker must be a prefix, not mid-name
	}
	for name, want := range cases {
		if got := AgentKind(name); got != want {
			t.Errorf("AgentKind(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestAbbrevExpandHome(t *testing.T) {
	home := homeOrSkip(t)
	p := filepath.Join(home, "git", "x")
	if got := abbrevHome(p); got != "~/git/x" {
		t.Errorf("abbrevHome(%q) = %q, want %q", p, got, "~/git/x")
	}
	if got := expandHome("~/git/x"); got != p {
		t.Errorf("expandHome = %q, want %q", got, p)
	}
	if got := abbrevHome("/opt/x"); got != "/opt/x" {
		t.Errorf("abbrevHome(/opt/x) = %q, want %q", got, "/opt/x")
	}
	if got := abbrevHome(home); got != "~" {
		t.Errorf("abbrevHome(home) = %q, want %q", got, "~")
	}
}

func TestAgentCommand(t *testing.T) {
	// --continue resumes a prior conversation; the `|| <agent>` fallback launches
	// fresh when --continue exits non-zero (no conversation to continue), so an
	// idle-launched pane never dies on a first launch.
	var off config.Sandbox
	cases := map[string]string{
		"claude":   "claude --continue || claude",
		"opencode": "opencode --continue || opencode",
		// pi runs through the pi-sandbox wrapper, so the command is not the kind's name.
		"pi": "pi-sandbox --continue || pi-sandbox",
		// An unregistered kind falls back to claude, matching SessionForKind: a bad kind
		// yields a working claude pane rather than a session running a nonexistent binary.
		"nope": "claude --continue || claude",
	}
	for kind, want := range cases {
		if got := BuildAgentCommand(kind, "/g/proj", off); got != want {
			t.Errorf("%s cmd = %q, want %q", kind, got, want)
		}
	}
	// "off" spells a disabled runner too.
	if got := BuildAgentCommand("claude", "/g/proj", config.Sandbox{Command: "off"}); got != cases["claude"] {
		t.Errorf("command off: cmd = %q, want %q", got, cases["claude"])
	}
}

func TestAgentCommandSandboxed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	cfgSettings := filepath.Join(home, "cfg.json")
	sb := config.Sandbox{Command: "srt", ConfigSettings: cfgSettings}

	// The whole --continue line runs inside the runner, with the kind's sandbox
	// flags; pi swaps its container wrapper for the bare binary.
	cases := map[string]string{
		"claude":   `srt -c 'claude --dangerously-skip-permissions --continue || claude --dangerously-skip-permissions'`,
		"opencode": `srt -c 'opencode --auto --continue || opencode --auto'`,
		"pi":       `srt -c 'pi --continue || pi'`,
	}
	for kind, want := range cases {
		if got := BuildAgentCommand(kind, "/g/proj", sb); got != want {
			t.Errorf("%s cmd = %q, want %q", kind, got, want)
		}
	}

	// A config session whose settings file is missing keeps the normal profile:
	// never a weaker one, never no sandbox.
	dotcfg := filepath.Join(home, ".config", "hypr")
	if got := BuildAgentCommand("pi", dotcfg, sb); got != cases["pi"] {
		t.Errorf("config session, no settings file: cmd = %q, want %q", got, cases["pi"])
	}

	// With the file present, a config session (in or below ~/.config) uses it.
	if err := os.WriteFile(cfgSettings, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := "srt -s '" + cfgSettings + "' -c 'pi --continue || pi'"
	for _, dir := range []string{filepath.Join(home, ".config"), dotcfg} {
		if got := BuildAgentCommand("pi", dir, sb); got != want {
			t.Errorf("config session %s: cmd = %q, want %q", dir, got, want)
		}
	}

	// A sibling dir that only shares the prefix is not a config session; an
	// explicit normal settings file is passed with -s.
	sb.Settings = "/s/normal.json"
	want = "srt -s '/s/normal.json' -c 'pi --continue || pi'"
	if got := BuildAgentCommand("pi", filepath.Join(home, ".config-old"), sb); got != want {
		t.Errorf("prefix sibling: cmd = %q, want %q", got, want)
	}
}

// TestKindRegistryRoundTrip exercises every registered kind through the naming
// grammar generically, so an agent added to the registry is covered without adding a
// case: its session must carry a distinct marker, report its own AgentKind, and keep
// the project and worktree it was built from.
func TestKindRegistryRoundTrip(t *testing.T) {
	const proj = "/home/u/git/proj"
	base := ExpectedSession(proj, "feat")
	seen := map[string]string{}
	for _, k := range Kinds() {
		if k.Name == "" || k.Marker == "" || k.Bin == "" || k.Label == "" || k.Badge == "" {
			t.Errorf("kind %+v has an empty field", k)
		}
		if prev, dup := seen[k.Marker]; dup {
			t.Errorf("kinds %q and %q share the marker %q; session names would be ambiguous", prev, k.Name, k.Marker)
		}
		seen[k.Marker] = k.Name

		name := SessionForKind(base, k.Name)
		if got := AgentKind(name); got != k.Name {
			t.Errorf("AgentKind(%q) = %q, want %q", name, got, k.Name)
		}
		if got := WorktreeName(name); got != "feat" {
			t.Errorf("WorktreeName(%q) = %q, want %q", name, got, "feat")
		}
		if got, _, ok := MatchProject(name, []string{proj}); !ok || got != proj {
			t.Errorf("MatchProject(%q) = %q, %v; want %q, true", name, got, ok, proj)
		}
		if !ValidKind(k.Name) {
			t.Errorf("ValidKind(%q) = false, want true", k.Name)
		}
		if lk, ok := LookupKind(k.Name); !ok || lk != k {
			t.Errorf("LookupKind(%q) = %+v, %v; want %+v, true", k.Name, lk, ok, k)
		}
	}
	if ValidKind("nope") || ValidKind("") {
		t.Error("ValidKind should reject unregistered kinds")
	}
}

// TestPiSessionNaming pins pi's own marker, since the tmux listing regex spells it
// out separately (see tmux.IsAgentSession).
func TestPiSessionNaming(t *testing.T) {
	// Route the expectation through sessionPrefix so the assertion holds whatever
	// $HOME is (the path is home-abbreviated and tmux-sanitized on the way in).
	const proj = "/home/u/git/proj"
	base := ExpectedSession(proj, "")
	if want := piPre + sessionPrefix(proj); SessionForKind(base, "pi") != want {
		t.Errorf("pi session = %q, want %q", SessionForKind(base, "pi"), want)
	}
	if got := AgentKind("[kmux][pi]~/git/proj"); got != "pi" {
		t.Errorf("AgentKind lower-case marker = %q, want pi", got)
	}
}

func TestAttnSession(t *testing.T) {
	const canon = ccPre + "~/git/kmux"
	marked := "[!!]" + canon
	cases := []struct {
		in        string
		attention bool
		want      string
	}{
		{canon, true, marked},  // mark a canonical name
		{canon, false, canon},  // no-op on an unmarked name
		{marked, false, canon}, // clear a marked name
		{marked, true, marked}, // re-marking must not double the prefix
	}
	for _, c := range cases {
		if got := AttnSession(c.in, c.attention); got != c.want {
			t.Errorf("AttnSession(%q, %v) = %q, want %q", c.in, c.attention, got, c.want)
		}
	}
}

func TestCanonicalSessionAndHasAttn(t *testing.T) {
	const canon = ocPre + "~/git/proj@feat"
	marked := "[!!]" + canon
	if got := CanonicalSession(marked); got != canon {
		t.Errorf("CanonicalSession(%q) = %q, want %q", marked, got, canon)
	}
	if got := CanonicalSession(canon); got != canon {
		t.Errorf("CanonicalSession(%q) = %q, want unchanged", canon, got)
	}
	if !HasAttn(marked) || HasAttn(canon) {
		t.Errorf("HasAttn: marked=%v canon=%v, want true/false", HasAttn(marked), HasAttn(canon))
	}
}

// TestParsersTolerateAttnMark asserts the identity parsers ignore a leading "[!!]", so a
// blocked session still resolves to the same kind/path/project as its canonical form.
func TestParsersTolerateAttnMark(t *testing.T) {
	home := homeOrSkip(t)
	proj := filepath.Join(home, "git", "kmux")
	canon := ccPre + "~/git/kmux@feat"
	marked := "[!!]" + canon

	if AgentKind(marked) != "claude" {
		t.Errorf("AgentKind(%q) = %q, want claude", marked, AgentKind(marked))
	}
	if got := ProjectPath(marked); got != proj {
		t.Errorf("ProjectPath(%q) = %q, want %q", marked, got, proj)
	}
	if got := WorktreeName(marked); got != "feat" {
		t.Errorf("WorktreeName(%q) = %q, want feat", marked, got)
	}
	p, wt, ok := MatchProject(marked, []string{proj})
	if !ok || p != proj || wt != "feat" {
		t.Errorf("MatchProject(%q) = (%q, %q, %v), want (%q, feat, true)", marked, p, wt, ok, proj)
	}
}

func TestDashTabTitle(t *testing.T) {
	home := homeOrSkip(t)
	kmux := filepath.Join(home, "git", "kmux")
	// The unscoped default stands in the ~/git scan root for the empty path.
	root := "[kmux][dash]~/git"
	scoped := "[kmux][dash]~/git/kmux"
	cases := []struct {
		name      string
		scopeDir  string
		attention bool
		want      string
	}{
		{"unscoped", "", false, root},
		{"unscoped attention", "", true, "[!!]" + root},
		{"scoped", kmux, false, scoped},
		{"scoped attention", kmux, true, "[!!]" + scoped},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DashTabTitle(c.scopeDir, c.attention); got != c.want {
				t.Errorf("DashTabTitle(%q, %v) = %q, want %q", c.scopeDir, c.attention, got, c.want)
			}
		})
	}
}
