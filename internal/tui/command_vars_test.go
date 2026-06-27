package tui

import (
	"maps"
	"testing"

	"github.com/olli-io/kmux/internal/project"
)

func TestCommandVars(t *testing.T) {
	m := model{projects: []project.Project{{
		Name:      "solo",
		Path:      "/g/solo",
		Worktrees: []project.Worktree{{Name: "feat", Path: "/g/solo-feat"}},
	}}}

	cases := []struct {
		name string
		row  *row
		want map[string]string
	}{
		{
			"main worktree leaf",
			&row{section: sectionProjects, dir: "/g/solo", session: "solo~cl"},
			map[string]string{"dir": "/g/solo", "project": "solo", "worktree": "", "project_root": "/g/solo", "tmux_session": "solo~cl"},
		},
		{
			"linked worktree leaf",
			&row{section: sectionProjects, dir: "/g/solo-feat", session: "solo/feat~oc"},
			map[string]string{"dir": "/g/solo-feat", "project": "solo", "worktree": "feat", "project_root": "/g/solo", "tmux_session": "solo/feat~oc"},
		},
		{
			"unmatched dir keeps project fields empty",
			&row{section: sectionProjects, dir: "/elsewhere"},
			map[string]string{"dir": "/elsewhere", "project": "", "worktree": "", "project_root": "", "tmux_session": ""},
		},
	}
	for _, c := range cases {
		if got := m.commandVars(c.row); !maps.Equal(got, c.want) {
			t.Errorf("%s: commandVars = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestExpandCommandVars(t *testing.T) {
	vars := map[string]string{"dir": "/g/my repo", "tmux_session": "solo~cl", "project": "solo"}

	// {dir} with a space is single-quoted; multiple placeholders all expand.
	if got := expandCommandVars("$EDITOR {dir}", vars); got != "$EDITOR '/g/my repo'" {
		t.Errorf("expandCommandVars dir = %q", got)
	}
	if got := expandCommandVars("tmux attach -t {tmux_session}", vars); got != "tmux attach -t 'solo~cl'" {
		t.Errorf("expandCommandVars session = %q", got)
	}
	// An unknown placeholder is left untouched.
	if got := expandCommandVars("echo {branch}", vars); got != "echo {branch}" {
		t.Errorf("expandCommandVars unknown = %q, want left as-is", got)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Errorf("shellQuote = %q", got)
	}
}
