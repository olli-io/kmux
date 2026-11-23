package main

import (
	"testing"

	"github.com/olli-io/kmux/internal/tmux"
)

func TestRunningDash(t *testing.T) {
	const name = "[kmux][dash]~/git"
	dash := tmux.Pane{ID: "%1", Role: tmux.RoleDash, Dash: name, Session: name}
	renamed := dash
	renamed.Session = "[kmux][CC]~/git/a"
	other := tmux.Pane{ID: "%2", Role: tmux.RoleDash, Dash: "[kmux][dash]~/git/a", Session: "x"}
	dead := dash
	dead.Dead = true
	squatter := tmux.Pane{ID: "%3", Session: name}

	cases := []struct {
		label  string
		snap   []tmux.Pane
		pane   string
		rename bool
	}{
		{"none", nil, "", false},
		{"other scope", []tmux.Pane{other}, "", false},
		{"dead pane", []tmux.Pane{dead}, "", false},
		{"found", []tmux.Pane{other, dash}, "%1", false},
		{"session renamed", []tmux.Pane{renamed}, "%1", true},
		{"renamed, name taken", []tmux.Pane{renamed, squatter}, "%1", false},
	}
	for _, c := range cases {
		pane, rename := runningDash(c.snap, name)
		if pane != c.pane || rename != c.rename {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", c.label, pane, rename, c.pane, c.rename)
		}
	}
}
