package config

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultIdleTimeout is the built-in default for how long an agent session may
// sit with a completely unchanged pane before kmux kills it to free the memory
// its agent process holds. The `idle_timeout:` config key overrides it; see
// IdleDuration.
const DefaultIdleTimeout = 2 * time.Hour

// Config is kmux's configuration, layered from the default file shipped beside
// the binary and the user's optional ~/.config/kmux/config.yaml (see LoadConfig).
type Config struct {
	// Projects lists extra git project folders to show in the Projects panel
	// when kmux is launched without a directory argument, in addition to the
	// repos discovered under ~/git. Entries may use ~ and $ENV references and may
	// point at a main worktree, a linked worktree, or any subdirectory of one.
	Projects []string

	// IdleTimeout overrides how long an agent session may sit with a completely
	// unchanged pane before kmux kills it to free memory (see idleTimeout). It is
	// a Go duration string such as `2h`, `90m`, or `30m`. A zero value (omitted,
	// or set to `0`/`off`/`never`) leaves the default in place; see IdleDuration.
	IdleTimeout time.Duration

	// idleSet records whether idle_timeout appeared in the file at all, so
	// IdleDuration can tell "use the default" from an explicit "never reap".
	idleSet bool

	// CustomCommands are the user-definable keybindings shown alongside the fixed
	// navigation keys: each binds a key to a shell command run in a new kitty tab.
	// The shipped default file defines the editor and lazygit bindings; the user's
	// config merges onto it by key (see mergeConfig).
	CustomCommands []CustomCommand

	// Keybindings maps each rebindable TUI action (see KeyActions) to the single
	// key that triggers it. Resolved by mergeConfig, which layers the user's config
	// over DefaultKeybindings, so a fully populated map is always present on a
	// loaded Config. Keys here may collide with CustomCommands or each other; such
	// conflicts are surfaced by KeybindingConflicts rather than rejected.
	Keybindings map[string]string
}

// CustomCommand is one configurable keybinding: pressing Key in a matching panel
// runs Cmd (via `sh -c`) in a new kitty tab whose cwd is the selected row's
// directory.
type CustomCommand struct {
	// Key is the keypress that triggers the command (e.g. "g", "e"). Keys that
	// collide with kmux's fixed bindings are ignored.
	Key string `yaml:"key"`
	// Cmd is the shell command line. `{dir}` is replaced with the selected row's
	// directory (shell-escaped); $ENV and other shell syntax expand normally.
	Cmd string `yaml:"cmd"`
	// Panel scopes the binding: "sessions", "projects", or "both" (the default
	// when empty). See Matches.
	Panel string `yaml:"panel"`
	// Title labels the binding in the Keys footer and names its kitty tab.
	Title string `yaml:"title"`
	// Target selects how the command runs:
	//   "tab"    (default) a new kitty tab in the current window — right for
	//            terminal programs like nvim or lazygit.
	//   "window" a new kitty OS window — a separate kitty instance.
	//   "detach" a detached background process with no kitty surface — right for
	//            GUI editors like Zed or VS Code that fork and return immediately.
	// See EffectiveTarget.
	Target string `yaml:"target"`
}

// EffectiveTarget normalizes Target to "tab" (the default), "window", or
// "detach", accepting a few synonyms; any unrecognized value falls back to "tab".
func (c CustomCommand) EffectiveTarget() string {
	switch strings.ToLower(strings.TrimSpace(c.Target)) {
	case "detach", "detached", "background", "bg":
		return "detach"
	case "window", "os-window", "oswindow", "win":
		return "window"
	default:
		return "tab"
	}
}

// Matches reports whether the command applies to the named panel ("sessions" or
// "projects"). An empty, "both", or "all" Panel matches either.
func (c CustomCommand) Matches(panel string) bool {
	switch strings.ToLower(strings.TrimSpace(c.Panel)) {
	case "", "both", "all":
		return true
	default:
		return strings.EqualFold(strings.TrimSpace(c.Panel), panel)
	}
}

// Rebindable TUI action names. Each is a stable identifier used as a key in the
// keybindings config map and in the action→key / key→action maps the TUI builds
// from it. The fixed keys 1, 2, and ctrl+c are intentionally not actions here —
// they are never rebindable (see KeybindingConflicts and the TUI dispatch).
const (
	ActionPrevItem              = "prevItem"
	ActionNextItem              = "nextItem"
	ActionPrevItemAlt           = "prevItemAlt"
	ActionNextItemAlt           = "nextItemAlt"
	ActionPrevPanel             = "prevPanel"
	ActionNextPanel             = "nextPanel"
	ActionPrevPanelAlt          = "prevPanelAlt"
	ActionNextPanelAlt          = "nextPanelAlt"
	ActionCreateOrAttachAgent   = "createOrAttachAgent"
	ActionDetachAgent           = "detachAgent"
	ActionKillAgent             = "killAgent"
	ActionFullscreenAgent       = "fullscreenAgent"
	ActionCreateOrFocusClaude   = "createOrFocusClaude"
	ActionCreateOrFocusOpencode = "createOrFocusOpencode"
	ActionLaunchKmuxInProject   = "launchKmuxInProject"
	ActionQuit                  = "quit"
)

// KeyActions returns every rebindable action in canonical order. The order is
// authoritative: it drives deterministic conflict reporting and the first-wins
// resolution of the key→action map (when two actions share a key, the one listed
// first here is the one that fires). Keep it in sync with DefaultKeybindings.
func KeyActions() []string {
	return []string{
		ActionPrevItem, ActionNextItem,
		ActionPrevItemAlt, ActionNextItemAlt,
		ActionPrevPanel, ActionNextPanel,
		ActionPrevPanelAlt, ActionNextPanelAlt,
		ActionCreateOrAttachAgent,
		ActionDetachAgent, ActionKillAgent, ActionFullscreenAgent,
		ActionCreateOrFocusClaude, ActionCreateOrFocusOpencode,
		ActionLaunchKmuxInProject,
		ActionQuit,
	}
}

// DefaultKeybindings returns the built-in action→key map, matching kmux's
// historical fixed keys. These defaults live in Go (not the shipped config) so the
// TUI is fully bound even on a bare build with no config file; the user's config
// only overrides individual actions (see mergeKeybindings).
func DefaultKeybindings() map[string]string {
	return map[string]string{
		ActionPrevItem:              "k",
		ActionNextItem:              "j",
		ActionPrevItemAlt:           "up",
		ActionNextItemAlt:           "down",
		ActionPrevPanel:             "h",
		ActionNextPanel:             "l",
		ActionPrevPanelAlt:          "left",
		ActionNextPanelAlt:          "right",
		ActionCreateOrAttachAgent:   "enter",
		ActionDetachAgent:           "d",
		ActionKillAgent:             "D",
		ActionFullscreenAgent:       "f",
		ActionCreateOrFocusClaude:   "c",
		ActionCreateOrFocusOpencode: "o",
		ActionLaunchKmuxInProject:   "t",
		ActionQuit:                  "q",
	}
}

// fixedKeyLabels are the keys kmux always handles before the rebindable actions,
// in deterministic report order. A user binding (action or custom command) that
// lands on one of these never wins, so KeybindingConflicts flags it.
var fixedKeyLabels = []struct{ key, label string }{
	{"1", "focus Projects panel"},
	{"2", "focus Sessions panel"},
	{"ctrl+c", "quit (always)"},
}

// KeybindingConflicts reports keys bound to more than one thing across the
// resolved keybindings, the fixed keys (1, 2, ctrl+c), and the custom commands —
// one human-readable line per conflicted key, in a deterministic order
// (KeyActions, then fixed keys, then custom commands). Custom commands are scanned
// as-is (not after effectiveCommands drops collisions) so a binding the TUI
// silently ignores is still reported. An empty result means no conflicts.
func (c Config) KeybindingConflicts() []string {
	type binding struct{ key, label string }
	var bindings []binding
	for _, action := range KeyActions() {
		if key := c.Keybindings[action]; key != "" {
			bindings = append(bindings, binding{key, action})
		}
	}
	for _, f := range fixedKeyLabels {
		bindings = append(bindings, binding{f.key, f.label})
	}
	for _, cmd := range c.CustomCommands {
		if cmd.Key == "" {
			continue
		}
		label := cmd.Title
		if label == "" {
			label = cmd.Cmd
		}
		bindings = append(bindings, binding{cmd.Key, "command: " + label})
	}

	// Group labels by key, preserving first-seen key order for deterministic output.
	var order []string
	byKey := map[string][]string{}
	for _, b := range bindings {
		if _, seen := byKey[b.key]; !seen {
			order = append(order, b.key)
		}
		byKey[b.key] = append(byKey[b.key], b.label)
	}

	var out []string
	for _, key := range order {
		if labels := byKey[key]; len(labels) > 1 {
			out = append(out, "key "+keyDisplay(key)+" → "+strings.Join(labels, ", "))
		}
	}
	return out
}

// keyDisplay renders a key name for conflict messages, spelling out the space key
// (which would otherwise be invisible) and quoting the rest.
func keyDisplay(key string) string {
	if key == " " {
		return "space"
	}
	return "'" + key + "'"
}

// IdleDuration resolves the effective idle-kill timeout: the configured value if
// idle_timeout was set (including an explicit 0, which disables reaping), else
// DefaultIdleTimeout.
func (c Config) IdleDuration() time.Duration {
	if c.idleSet {
		return c.IdleTimeout
	}
	return DefaultIdleTimeout
}

// configFile returns the path to the user's config file
// (~/.config/kmux/config.yaml). Unlike stateFile it does not create the
// directory: the config is optional and only ever read.
func configFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "kmux", "config.yaml"), nil
}

// defaultConfigPath returns the default config shipped next to the kmux binary
// (install.sh drops scripts/config.yaml there). Resolving relative to the
// executable keeps kmux and its defaults versioned together; it mirrors how the
// nvim-tab helper is located.
func defaultConfigPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Join(filepath.Dir(exe), "config.yaml"), nil
}

// LoadConfig reads the default config shipped beside the binary as a base layer,
// then overlays the user's optional ~/.config/kmux/config.yaml on top (see
// mergeConfig). Missing files yield an empty layer and no error, since the user
// config is optional and the default may be absent on a bare build.
func LoadConfig() (Config, error) {
	var base Config
	if path, err := defaultConfigPath(); err == nil {
		if base, err = loadFile(path); err != nil {
			return Config{}, err
		}
	}
	path, err := configFile()
	if err != nil {
		return Config{}, err
	}
	over, err := loadFile(path)
	if err != nil {
		return Config{}, err
	}
	return mergeConfig(base, over), nil
}

// loadFile parses one config file, treating a missing file as an empty Config.
func loadFile(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	defer f.Close()
	return parseConfig(f)
}

// rawConfig mirrors the YAML document. idle_timeout is decoded as a node so a
// bare scalar (`0`, `2h`, or quoted `"45m"`) all read uniformly, and a missing
// key (Kind == 0) is distinguishable from an explicit one.
type rawConfig struct {
	Projects       []string          `yaml:"projects"`
	IdleTimeout    yaml.Node         `yaml:"idle_timeout"`
	CustomCommands []CustomCommand   `yaml:"customCommands"`
	Keybindings    map[string]string `yaml:"keybindings"`
}

// parseConfig decodes a kmux config document. Recognized keys are projects,
// idle_timeout, customCommands, and keybindings; unknown keys are ignored.
// Unparseable scalars (e.g. a bad duration) fall back to defaults rather than
// failing the load. Keybinding defaults are NOT injected here — they are layered
// in by mergeConfig, so parse/merge can tell "unset" from "set".
func parseConfig(r io.Reader) (Config, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return Config{}, err
	}
	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Config{}, err
	}
	var cfg Config
	for _, p := range raw.Projects {
		if v := strings.TrimSpace(p); v != "" {
			cfg.Projects = append(cfg.Projects, expandPath(v))
		}
	}
	if raw.IdleTimeout.Kind != 0 {
		applyIdle(&cfg, raw.IdleTimeout.Value)
	}
	for _, c := range raw.CustomCommands {
		c.Key = strings.TrimSpace(c.Key)
		c.Cmd = strings.TrimSpace(c.Cmd)
		c.Panel = strings.TrimSpace(c.Panel)
		c.Title = strings.TrimSpace(c.Title)
		c.Target = strings.TrimSpace(c.Target)
		if c.Key == "" {
			continue // a binding with no key is meaningless
		}
		cfg.CustomCommands = append(cfg.CustomCommands, c)
	}
	// Keybindings: trim each value (trim only — `d` vs `D` are distinct keys) and
	// drop entries naming an action kmux doesn't know, so an unknown action can't
	// shadow a real one. Defaults are layered in later by mergeConfig.
	for _, action := range KeyActions() {
		if key, ok := raw.Keybindings[action]; ok {
			if key = strings.TrimSpace(key); key != "" {
				if cfg.Keybindings == nil {
					cfg.Keybindings = map[string]string{}
				}
				cfg.Keybindings[action] = key
			}
		}
	}
	return cfg, nil
}

// applyIdle interprets an idle_timeout scalar. `0`, `off`, and `never` disable
// reaping; otherwise it parses a Go duration like `2h` or `90m`. An unparseable
// value is left unset, so IdleDuration falls back to the default.
func applyIdle(cfg *Config, val string) {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "0", "off", "never":
		cfg.IdleTimeout, cfg.idleSet = 0, true
	default:
		if d, err := time.ParseDuration(strings.TrimSpace(val)); err == nil {
			cfg.IdleTimeout, cfg.idleSet = d, true
		}
	}
}

// mergeConfig overlays the user config (over) onto the default (base): projects
// are concatenated, an explicit idle_timeout in over wins, custom commands merge
// by key (see mergeCommands), and keybindings are layered over the Go defaults
// (see mergeKeybindings).
func mergeConfig(base, over Config) Config {
	out := base
	out.Projects = append(append([]string(nil), base.Projects...), over.Projects...)
	if over.idleSet {
		out.IdleTimeout, out.idleSet = over.IdleTimeout, true
	}
	out.CustomCommands = mergeCommands(base.CustomCommands, over.CustomCommands)
	out.Keybindings = mergeKeybindings(DefaultKeybindings(), base.Keybindings, over.Keybindings)
	return out
}

// mergeCommands merges over onto base by Key: a matching key is overridden, a new
// key is appended, and an entry with an empty Cmd removes an inherited binding.
func mergeCommands(base, over []CustomCommand) []CustomCommand {
	out := append([]CustomCommand(nil), base...)
	for _, oc := range over {
		idx := slices.IndexFunc(out, func(c CustomCommand) bool { return c.Key == oc.Key })
		if oc.Cmd == "" { // empty cmd removes an inherited binding
			if idx >= 0 {
				out = slices.Delete(out, idx, idx+1)
			}
			continue
		}
		if idx >= 0 {
			out[idx] = oc
		} else {
			out = append(out, oc)
		}
	}
	return out
}

// mergeKeybindings resolves the effective action→key map: it starts from
// defaults, then overlays each non-empty value from base (the shipped config) and
// over (the user's config) in turn. Empty values are ignored, so a layer can
// remap an action but not unbind it; an action absent from every layer keeps its
// default. The result always covers every action in KeyActions.
func mergeKeybindings(defaults, base, over map[string]string) map[string]string {
	out := make(map[string]string, len(defaults))
	for action, key := range defaults {
		out[action] = key
	}
	for _, layer := range []map[string]string{base, over} {
		for action, key := range layer {
			if key = strings.TrimSpace(key); key != "" {
				out[action] = key
			}
		}
	}
	return out
}

// expandPath resolves $ENV references and a leading ~ to an absolute-ish path.
func expandPath(p string) string {
	p = os.ExpandEnv(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p[1:], "/"))
		}
	}
	return p
}
