package main

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
		{"unset uses default", "projects:\n  - ~/work\n", idleTimeout, false},
		{"duration", "idle_timeout: 90m\n", 90 * time.Minute, true},
		{"hours", "idle_timeout: 3h\n", 3 * time.Hour, true},
		{"zero disables", "idle_timeout: 0\n", 0, true},
		{"off disables", "idle_timeout: off\n", 0, true},
		{"never disables", "idle_timeout: never\n", 0, true},
		{"quoted value", `idle_timeout: "45m"` + "\n", 45 * time.Minute, true},
		{"inline comment", "idle_timeout: 2h # reap after two hours\n", 2 * time.Hour, true},
		{"garbage falls back to default", "idle_timeout: soon\n", idleTimeout, false},
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
