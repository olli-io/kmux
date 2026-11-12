package detect

import "testing"

// TestEmbeddedManifestsLoad proves the embedded YAML parsed and compiled at init:
// every shipped agent is Known. A malformed manifest would panic at init before this
// runs. kinds_test.go pins this list against the agent registry.
func TestEmbeddedManifestsLoad(t *testing.T) {
	for _, kind := range []string{"claude", "opencode", "pi"} {
		if !Known(kind) {
			t.Errorf("manifest for %q did not load", kind)
		}
	}
	if Known("vim") || Known("") {
		t.Error("Known should be false for non-agent kinds")
	}
}

// TestMatchesReproducesMarkers pins the engine to the exact behaviors the status
// package's attention_test.go depends on, so a manifest edit that regresses detection
// fails here (next to the YAML) as well.
func TestMatchesReproducesMarkers(t *testing.T) {
	cases := []struct {
		name string
		kind string
		pane string
		want State
	}{
		// Busy: the spinner-timer line "verb… (Ns · …)" is the width-robust signal; the
		// "esc to interrupt"/"esc to int" footer is a fallback.
		{"claude busy spinner", "claude", "  ✽ Processing… (5m 44s · ↓ 15.4k tokens)", StateWorking},
		{"claude busy esc hint", "claude", "│ > write the code │\n  Cogitating… (esc to interrupt)", StateWorking},
		{"claude busy truncated", "claude", "  ⏵⏵ auto mode on (shift+tab to cycle) · esc to int…", StateWorking},
		{"claude idle footer", "claude", "  ⏵⏵ accept edits on (shift+tab to cycle) · ← for a…", StateUnknown},
		{"claude bash permission", "claude", "Do you want to proceed?\n ❯ 1. Yes\n   2. No", StateBlocked},
		{"claude question box", "claude", "● Pick one\n  1: a  2: b\n────\n  esc to cancel   enter to confirm", StateBlocked},
		{"claude waiting", "claude", "│ > Try \"fix the bug\" │\n  ? for shortcuts", StateUnknown},
		{"opencode busy", "opencode", "⬝⬝⬝■■■■■  esc interrupt    tab agents", StateWorking},
		{"opencode busy truncated", "opencode", "⬝⬝⬝■■■■■  esc int…   tab agents", StateWorking},
		{"opencode permission", "opencode", "△ Permission required\n $ echo hi\n Allow once   Allow always   Reject", StateBlocked},
		{"opencode waiting", "opencode", "Build · Big Pickle OpenCode Zen\n  8.4K (4%)  ctrl+p commands", StateUnknown},

		// pi hangs its status indicator in the rule above the editor, so busy reads as a
		// braille spinner inside a run of box-drawing dashes. Panes below are real
		// captures (tmux capture-pane -p), trimmed in width.
		{"pi busy", "pi", "  41\n  42\n── ⠼ Working ──────────────\n\n──────────────────────────\n/g/proj\n1.0%/262k (auto)", StateWorking},
		{"pi busy narrow", "pi", "── ⠧ Working ────\n\n────────────────\n/g/proj\n1.0%/262k", StateWorking},
		{"pi busy status line", "pi", " ⠹ Compacting context... (escape to cancel)\n\n─────────\n/g/proj\n1.0%/262k", StateWorking},
		{"pi busy interrupt hint", "pi", "──────────\n Working (escape to interrupt)\n─────────\n/g/proj", StateWorking},
		{"pi waiting", "pi", "──────────────────────────\n>\n──────────────────────────\n/g/proj\n0.0%/262k (auto)   kimi-k2.6 • high", StateUnknown},
		{"pi startup banner not busy", "pi", " pi v0.85.0\n escape interrupt · ctrl+c/ctrl+d clear/exit · / commands · ! bash · ctrl+o more\n──────\n/g/proj", StateUnknown},
		{"pi select dialog", "pi", "→ ✓ kimi-k2.6 [opencode-go] · default\n    minimax-m3 [opencode-go]\n  (1/27)\n  Enter to select · Ctrl+S to set as default · Esc to cancel\n──────\n/g/proj", StateBlocked},
		{"pi select dialog while busy is blocked", "pi", "── ⠼ Working ────\n  Enter to select · Esc to cancel\n──────\n/g/proj", StateBlocked},
		{"pi cancel hint alone not blocked", "pi", "the docs say esc to cancel a request\n──────\n>\n──────\n/g/proj", StateUnknown},
		{"pi transcript word working not busy", "pi", "I am working on it now\n──────\n>\n──────\n/g/proj", StateUnknown},
		{"unknown kind", "vim", "esc to interrupt", StateUnknown},

		// Tail confinement: a marker only in scrollback (above the live bottom region)
		// must not match — the pane reads as waiting (no rule matches the tail).
		{
			"claude busy in scrollback ignored",
			"claude",
			"we discussed esc to interrupt earlier\n\n\n\n\n\n\n\n\n\n\n\n\n│ > Try \"fix the bug\" │\n  ? for shortcuts",
			StateUnknown,
		},
		{
			"pi busy in scrollback ignored",
			"pi",
			"── ⠼ Working ──────────────\n\n\n\n\n\n\n\n\n\n\n\n\n──────────────────────────\n>\n──────────────────────────\n/g/proj",
			StateUnknown,
		},
		{
			"claude permission in scrollback ignored",
			"claude",
			"do you want to proceed earlier\n\n\n\n\n\n\n\n\n\n\n\n\n│ > Try \"fix the bug\" │\n  ? for shortcuts",
			StateUnknown,
		},
	}
	for _, c := range cases {
		got, _ := Classify(c.kind, c.pane)
		if got != c.want {
			t.Errorf("%s: Classify(%q) = %q, want %q", c.name, c.kind, got, c.want)
		}
	}
}

func TestLoadManifestErrors(t *testing.T) {
	if _, err := loadManifest([]byte("rules: [this: is, : broken\n  - nope")); err == nil {
		t.Error("expected parse error for invalid yaml")
	}
	if _, err := loadManifest([]byte("agent: x\nrules:\n  - id: r\n    region: nonsense_region\n    contains: [\"a\"]\n")); err == nil {
		t.Error("expected error for unknown region")
	}
	if _, err := loadManifest([]byte("agent: x\nrules:\n  - id: r\n    region: whole_recent\n    regex: [\"(\"]\n")); err == nil {
		t.Error("expected error for bad regex")
	}
}

// TestClassifyPriority checks highest-priority-wins with tie-keeps-first, independent of
// the shipped manifests.
func TestClassifyPriority(t *testing.T) {
	man, err := loadManifest([]byte(`
agent: x
rules:
  - id: low
    state: idle
    priority: 10
    region: whole_recent
    contains: ["a"]
  - id: high
    state: working
    priority: 20
    region: whole_recent
    contains: ["a"]
  - id: tie
    state: blocked
    priority: 20
    region: whole_recent
    contains: ["a"]
`))
	if err != nil {
		t.Fatalf("loadManifest: %v", err)
	}
	manifests["__test__"] = man
	defer delete(manifests, "__test__")

	got, ok := Classify("__test__", "a")
	if !ok {
		t.Fatal("Classify ok=false for known test manifest")
	}
	// high (20) beats low (10); high precedes tie in file order so it wins the tie.
	if got != StateWorking {
		t.Errorf("Classify = %q, want %q (highest priority, first on tie)", got, StateWorking)
	}
	if _, ok := Classify("nope", "a"); ok {
		t.Error("Classify ok should be false for unknown kind")
	}
}
