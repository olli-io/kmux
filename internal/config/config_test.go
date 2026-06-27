package config

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseConfigIdleTimeout(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		want   time.Duration // expected IdleDuration()
		setRaw bool          // expected idleSet
	}{
		{"unset uses default", "projects:\n  - ~/work\n", DefaultIdleTimeout, false},
		{"duration", "idle_timeout: 90m\n", 90 * time.Minute, true},
		{"hours", "idle_timeout: 3h\n", 3 * time.Hour, true},
		{"zero disables", "idle_timeout: 0\n", 0, true},
		{"off disables", "idle_timeout: off\n", 0, true},
		{"never disables", "idle_timeout: never\n", 0, true},
		{"quoted value", `idle_timeout: "45m"` + "\n", 45 * time.Minute, true},
		{"inline comment", "idle_timeout: 2h # reap after two hours\n", 2 * time.Hour, true},
		{"garbage falls back to default", "idle_timeout: soon\n", DefaultIdleTimeout, false},
	}
	for _, c := range cases {
		cfg, err := parseConfig(strings.NewReader(c.body))
		if err != nil {
			t.Fatalf("%s: parseConfig: %v", c.name, err)
		}
		if cfg.idleSet != c.setRaw {
			t.Errorf("%s: idleSet = %v, want %v", c.name, cfg.idleSet, c.setRaw)
		}
		if got := cfg.IdleDuration(); got != c.want {
			t.Errorf("%s: IdleDuration() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseConfigProjectsStillWorkAlongsideScalars(t *testing.T) {
	body := "idle_timeout: 1h\nprojects:\n  - ~/a\n  - ~/b\n"
	cfg, err := parseConfig(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.IdleDuration() != time.Hour {
		t.Errorf("IdleDuration() = %v, want 1h", cfg.IdleDuration())
	}
	if len(cfg.Projects) != 2 || !slices.ContainsFunc(cfg.Projects, func(p string) bool {
		return strings.HasSuffix(p, "/a")
	}) {
		t.Errorf("Projects = %v, want two entries ending /a and /b", cfg.Projects)
	}
}

func TestParseConfigCustomCommands(t *testing.T) {
	body := "customCommands:\n" +
		"  - key: e\n" +
		"    panel: both\n" +
		"    title: Editor\n" +
		"    cmd: $EDITOR {dir}\n" +
		"  - key: g\n" +
		"    panel: sessions\n" +
		"    cmd: lazygit\n" +
		"  - cmd: missing key is dropped\n"
	cfg, err := parseConfig(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if len(cfg.CustomCommands) != 2 {
		t.Fatalf("CustomCommands = %v, want 2 entries (key-less dropped)", cfg.CustomCommands)
	}
	e := cfg.CustomCommands[0]
	if e.Key != "e" || e.Title != "Editor" || e.Cmd != "$EDITOR {dir}" {
		t.Errorf("first command = %+v, want editor binding", e)
	}
	if !e.Matches("sessions") || !e.Matches("projects") {
		t.Errorf("panel:both should match both panels, got %q", e.Panel)
	}
	g := cfg.CustomCommands[1]
	if !g.Matches("sessions") || g.Matches("projects") {
		t.Errorf("panel:sessions should match only sessions, got %q", g.Panel)
	}
}

func TestCustomCommandEffectiveTarget(t *testing.T) {
	cases := map[string]string{
		"":           "tab",
		"tab":        "tab",
		"nonsense":   "tab",
		"window":     "window",
		"os-window":  "window",
		" WINDOW ":   "window",
		"detach":     "detach",
		"background": "detach",
		"BG":         "detach",
	}
	for in, want := range cases {
		if got := (CustomCommand{Target: in}).EffectiveTarget(); got != want {
			t.Errorf("EffectiveTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMergeConfigCustomCommandsByKey(t *testing.T) {
	base, err := parseConfig(strings.NewReader(
		"customCommands:\n" +
			"  - key: e\n    title: Editor\n    cmd: nvim {dir}\n" +
			"  - key: g\n    title: Lazygit\n    cmd: lazygit\n"))
	if err != nil {
		t.Fatalf("parseConfig base: %v", err)
	}
	over, err := parseConfig(strings.NewReader(
		"customCommands:\n" +
			"  - key: e\n    title: VS Code\n    cmd: code {dir}\n" + // override existing key
			"  - key: b\n    title: Btop\n    cmd: btop\n" + // add a new key
			"  - key: g\n    cmd: \"\"\n")) // empty cmd removes inherited binding
	if err != nil {
		t.Fatalf("parseConfig over: %v", err)
	}

	got := mergeConfig(base, over).CustomCommands
	byKey := map[string]CustomCommand{}
	for _, c := range got {
		byKey[c.Key] = c
	}
	if _, ok := byKey["g"]; ok {
		t.Errorf("key g should have been removed by empty run, got %+v", byKey["g"])
	}
	if byKey["e"].Cmd != "code {dir}" || byKey["e"].Title != "VS Code" {
		t.Errorf("key e = %+v, want overridden to VS Code", byKey["e"])
	}
	if byKey["b"].Cmd != "btop" {
		t.Errorf("key b = %+v, want appended btop binding", byKey["b"])
	}
	if len(got) != 2 {
		t.Errorf("merged commands = %v, want 2 (e overridden, b added, g removed)", got)
	}
}

func TestMergeConfigScalarsAndProjects(t *testing.T) {
	base, _ := parseConfig(strings.NewReader("projects:\n  - ~/a\nidle_timeout: 2h\n"))
	over, _ := parseConfig(strings.NewReader("projects:\n  - ~/b\nidle_timeout: 30m\n"))
	got := mergeConfig(base, over)
	if got.IdleDuration() != 30*time.Minute {
		t.Errorf("IdleDuration() = %v, want over's 30m", got.IdleDuration())
	}
	if len(got.Projects) != 2 {
		t.Errorf("Projects = %v, want both base and over concatenated", got.Projects)
	}

	// An unset idle_timeout in over leaves base's value in place.
	overNoIdle, _ := parseConfig(strings.NewReader("projects:\n  - ~/b\n"))
	if got := mergeConfig(base, overNoIdle); got.IdleDuration() != 2*time.Hour {
		t.Errorf("IdleDuration() = %v, want base's 2h preserved", got.IdleDuration())
	}
}

func TestParseConfigKeybindings(t *testing.T) {
	body := "keybindings:\n" +
		"  killAgent: '  x  '\n" + // trimmed to x
		"  quit: Q\n" +
		"  bogusAction: z\n" // unknown action dropped
	cfg, err := parseConfig(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	// parseConfig must NOT inject defaults: only the recognized, non-empty entries
	// from the file survive (defaults are layered in by mergeConfig instead).
	if len(cfg.Keybindings) != 2 {
		t.Fatalf("Keybindings = %v, want only the 2 recognized entries (no defaults injected)", cfg.Keybindings)
	}
	if cfg.Keybindings[ActionKillAgent] != "x" {
		t.Errorf("killAgent = %q, want trimmed %q", cfg.Keybindings[ActionKillAgent], "x")
	}
	if cfg.Keybindings[ActionQuit] != "Q" {
		t.Errorf("quit = %q, want Q", cfg.Keybindings[ActionQuit])
	}
	if _, ok := cfg.Keybindings["bogusAction"]; ok {
		t.Errorf("unknown action should be dropped, got %v", cfg.Keybindings)
	}
}

func TestMergeKeybindings(t *testing.T) {
	defaults := map[string]string{"a": "1", "b": "2", "c": "3"}
	base := map[string]string{"b": "B"}             // shipped layer overrides b
	over := map[string]string{"c": "C", "a": "   "} // user overrides c; empty a is ignored
	got := mergeKeybindings(defaults, base, over)
	if got["a"] != "1" {
		t.Errorf("a = %q, want default 1 (empty over value ignored)", got["a"])
	}
	if got["b"] != "B" {
		t.Errorf("b = %q, want base override B", got["b"])
	}
	if got["c"] != "C" {
		t.Errorf("c = %q, want over override C (over beats base and default)", got["c"])
	}

	// With no user/shipped keybindings at all, every action keeps its Go default.
	merged := mergeConfig(Config{}, Config{})
	for action, key := range DefaultKeybindings() {
		if merged.Keybindings[action] != key {
			t.Errorf("default %s = %q, want %q", action, merged.Keybindings[action], key)
		}
	}
}

func TestKeybindingConflicts(t *testing.T) {
	// Clean config: stock defaults plus a non-colliding custom command → no report.
	clean := Config{
		Keybindings:    DefaultKeybindings(),
		CustomCommands: []CustomCommand{{Key: "g", Cmd: "lazygit", Title: "Lazygit"}},
	}
	if got := clean.KeybindingConflicts(); got != nil {
		t.Errorf("clean config conflicts = %v, want nil", got)
	}

	// Two actions sharing a key are reported once.
	dup := DefaultKeybindings()
	dup[ActionKillAgent] = dup[ActionDetachAgent] // both now "d"
	if got := (Config{Keybindings: dup}).KeybindingConflicts(); len(got) != 1 {
		t.Fatalf("duplicate-key conflicts = %v, want exactly 1 line", got)
	}

	// A custom command landing on a fixed key (1/2/ctrl+c) is reported.
	onFixed := Config{
		Keybindings:    DefaultKeybindings(),
		CustomCommands: []CustomCommand{{Key: "1", Cmd: "echo hi", Title: "Hi"}},
	}
	if got := onFixed.KeybindingConflicts(); len(got) != 1 {
		t.Fatalf("custom-on-fixed-key conflicts = %v, want exactly 1 line", got)
	}
}
