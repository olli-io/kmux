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
