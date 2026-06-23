package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config is kmux's optional, user-authored configuration, read from
// ~/.config/kmux/config.yaml.
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
}

// IdleDuration resolves the effective idle-kill timeout: the configured value if
// idle_timeout was set (including an explicit 0, which disables reaping), else
// the built-in idleTimeout default.
func (c Config) IdleDuration() time.Duration {
	if c.idleSet {
		return c.IdleTimeout
	}
	return idleTimeout
}

// configFile returns the path to kmux's config file
// (~/.config/kmux/config.yaml). Unlike stateFile it does not create the
// directory: the config is optional and only ever read.
func configFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "kmux", "config.yaml"), nil
}

// LoadConfig reads the kmux config file. A missing file yields a zero Config and
// no error, since configuration is entirely optional.
func LoadConfig() (Config, error) {
	path, err := configFile()
	if err != nil {
		return Config{}, err
	}
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

// parseConfig reads a deliberately small subset of YAML: top-level `key:` lines
// (with an optional inline scalar value) and `- item` list entries beneath them,
// plus `#` comments and blank lines. It is enough for kmux's flat config without
// pulling in a YAML dependency. The `projects:` list and `idle_timeout:` scalar
// are recognized; unknown keys are ignored.
func parseConfig(r io.Reader) (Config, error) {
	var cfg Config
	key := ""
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		raw := sc.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if item, ok := strings.CutPrefix(line, "- "); ok {
			if key == "projects" {
				if v := cleanScalar(item); v != "" {
					cfg.Projects = append(cfg.Projects, expandPath(v))
				}
			}
			continue
		}
		// A top-level key opens a new section. An inline scalar value is consumed
		// for the keys kmux recognizes; otherwise it just opens a list section.
		name, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(name)
		if v := cleanScalar(val); v != "" {
			applyScalar(&cfg, key, v)
		}
	}
	return cfg, sc.Err()
}

// applyScalar interprets an inline `key: value` setting. Unrecognized keys and
// unparseable values are ignored, keeping a typo from failing the whole config.
func applyScalar(cfg *Config, key, val string) {
	switch key {
	case "idle_timeout":
		// `0`, `off`, and `never` all mean "disable reaping"; otherwise parse a
		// Go duration like `2h` or `90m`.
		switch strings.ToLower(val) {
		case "0", "off", "never":
			cfg.IdleTimeout, cfg.idleSet = 0, true
		default:
			if d, err := time.ParseDuration(val); err == nil {
				cfg.IdleTimeout, cfg.idleSet = d, true
			}
		}
	}
}

// cleanScalar normalizes a scalar value: it unwraps surrounding single or double
// quotes, otherwise strips a trailing ` # ...` inline comment.
func cleanScalar(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') {
		if i := strings.IndexByte(s[1:], s[0]); i >= 0 {
			return s[1 : 1+i] // quoted: take the inner text, ignore any trailing comment
		}
	}
	if i := strings.Index(s, " #"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
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
