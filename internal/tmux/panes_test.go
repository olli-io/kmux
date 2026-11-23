package tmux

import (
	"reflect"
	"strings"
	"testing"
)

func snapLine(f ...string) string { return strings.Join(f, "\t") + "\n" }

func TestParseSnapshot(t *testing.T) {
	out := snapLine("%1", "", "", "dash", "", "", "0", "0", "[kmux][dash]~", "/h", "@1", "1", "200", "48", "0", "2", "33", "0", "83", "48", "1", "[kmux][dash]~") +
		snapLine("%2", "[kmux][CC]~/a", "/h/a", "", "1", "2", "1", "0", "[kmux][dash]~", "/h", "@1", "1", "200", "48", "0", "2", "33", "0", "83", "48", "1", "") +
		"short\tline\n"
	got := parseSnapshot(out)
	if len(got) != 2 {
		t.Fatalf("got %d panes, want 2", len(got))
	}
	want := Pane{ID: "%2", Name: "[kmux][CC]~/a", Dir: "/h/a", Attn: true, Col: 2, Index: 1,
		Session: "[kmux][dash]~", SessDir: "/h", Window: "@1", Grid: "1", WinW: 200, WinH: 48,
		Panes: 2, X: 33, W: 83, H: 48, Clients: 1}
	if !reflect.DeepEqual(got[1], want) {
		t.Errorf("got %#v\nwant %#v", got[1], want)
	}
	if got[0].Role != RoleDash || got[0].Dash != "[kmux][dash]~" {
		t.Errorf("role = %q, want dash", got[0].Role)
	}
}

func TestAdoptOldSession(t *testing.T) {
	var calls [][]string
	old := run
	run = func(args ...string) (string, error) { calls = append(calls, args); return "", nil }
	defer func() { run = old }()

	panes := adopt([]Pane{
		{ID: "%5", Session: "[!!][kmux][CC]~/a", SessDir: "/h/a", Panes: 1},
		{ID: "%6", Session: "scratch", SessDir: "/tmp", Panes: 1},
		{ID: "%7", Session: "[kmux][OC]~/b", SessDir: "/h/b", Panes: 2},
		{ID: "%8", Name: "[kmux][PI]~/c", Session: "[kmux][PI]~/c", Panes: 1},
	})
	if panes[0].Name != "[kmux][CC]~/a" || panes[0].Dir != "/h/a" {
		t.Errorf("pane not adopted: %#v", panes[0])
	}
	if panes[1].Name != "" || panes[2].Name != "" {
		t.Errorf("adopted a pane that is not a single-pane agent session")
	}
	if len(calls) != 1 || !strings.Contains(strings.Join(calls[0], " "), "-t %5 @kmux [kmux][CC]~/a") {
		t.Errorf("calls = %v", calls)
	}
}

func TestChainJoinsWithSeparator(t *testing.T) {
	var got []string
	old := run
	run = func(args ...string) (string, error) { got = args; return "", nil }
	defer func() { run = old }()

	_ = Chain([][]string{{"a", "1"}, SetPaneOption("%1", OptAttn, ""), {"c"}})
	want := []string{"a", "1", ";", "set-option", "-p", "-u", "-t", "%1", OptAttn, ";", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
