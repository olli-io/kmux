package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Config is kmux's optional, user-authored configuration, read from
// ~/.config/kmux/config.yaml.
type Config struct {
	// Projects lists extra git project folders to show in the Projects panel
	// when kmux is launched without a directory argument, in addition to the
	// repos discovered under ~/git. Entries may use ~ and $ENV references and may
	// point at a main worktree, a linked worktree, or any subdirectory of one.
	Projects []string
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
// and `- item` list entries beneath them, plus `#` comments and blank lines. It
// is enough for kmux's flat config without pulling in a YAML dependency. Only
// the `projects:` list is currently recognized; unknown keys are ignored.
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
		// A top-level key opens a new section; any inline value is ignored, since
		// kmux only consumes list-valued keys.
		if name, _, ok := strings.Cut(line, ":"); ok {
			key = strings.TrimSpace(name)
		}
	}
	return cfg, sc.Err()
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
