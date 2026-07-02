package tmux

import (
	"reflect"
	"testing"
)

func TestParseCapturePanes(t *testing.T) {
	sessions := []string{"[kmux][CC]a", "[kmux][OC]b"}
	sent := captureSentinel + "\n"
	out := sent + "pane one\nline2\n" + sent + "pane two\n"
	got, err := parseCapturePanes(out, sessions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]string{
		"[kmux][CC]a": "pane one\nline2\n",
		"[kmux][OC]b": "pane two\n",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestParseCapturePanesCountMismatch(t *testing.T) {
	// An aborted chain (fewer sections than sessions) or the sentinel leaking into
	// captured text (more sections) must error so the caller falls back.
	sent := captureSentinel + "\n"
	cases := map[string]string{
		"too few":  sent + "only one pane\n",
		"too many": sent + "a\n" + sent + "b\n" + sent + "c\n",
	}
	for name, out := range cases {
		if _, err := parseCapturePanes(out, []string{"s1", "s2"}); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestAgentSessionRegex(t *testing.T) {
	match := []string{"[kmux][CC]~/git/a", "[kmux][OC]~/git/a@b", "[kmux][cc]/x/y/z"}
	noMatch := []string{"scratch", "CC", "kmux[CC]~/git/a", "~/git/a[kmux][CC]", "oc_thing"}
	for _, s := range match {
		if !agentSession.MatchString(s) {
			t.Errorf("expected %q to match", s)
		}
	}
	for _, s := range noMatch {
		if agentSession.MatchString(s) {
			t.Errorf("expected %q NOT to match", s)
		}
	}
}
