package detect

import "testing"

func TestParseRegion(t *testing.T) {
	cases := []struct {
		spec     string
		wantName regionName
		wantN    int
		wantErr  bool
	}{
		{"whole_recent", regionWholeRecent, 0, false},
		{"", regionWholeRecent, 0, false}, // default
		{"bottom_non_empty_lines(6)", regionBottomNonEmpty, 6, false},
		{"  bottom_non_empty_lines(3)  ", regionBottomNonEmpty, 3, false},
		{"osc_title", regionUnavailable, 0, false},
		{"prompt_box_body", regionUnavailable, 0, false},
		{"after_last_horizontal_rule", regionUnavailable, 0, false},
		{"bottom_non_empty_lines()", 0, 0, true},
		{"bottom_non_empty_lines(0)", 0, 0, true},
		{"bottom_non_empty_lines(-2)", 0, 0, true},
		{"bottom_non_empty_lines(x)", 0, 0, true},
		{"totally_unknown", 0, 0, true},
	}
	for _, c := range cases {
		got, err := parseRegion(c.spec)
		if (err != nil) != c.wantErr {
			t.Errorf("parseRegion(%q) err=%v, wantErr=%v", c.spec, err, c.wantErr)
			continue
		}
		if c.wantErr {
			continue
		}
		if got.name != c.wantName || got.n != c.wantN {
			t.Errorf("parseRegion(%q) = %+v, want name=%d n=%d", c.spec, got, c.wantName, c.wantN)
		}
	}
}

func TestExtractRegion(t *testing.T) {
	text := "a\nb\nc\n"
	if got, ok := extractRegion(text, regionSpec{name: regionWholeRecent}); !ok || got != text {
		t.Errorf("whole_recent = %q, %v; want %q, true", got, ok, text)
	}
	if got, ok := extractRegion(text, regionSpec{name: regionBottomNonEmpty, n: 2}); !ok || got != "b\nc" {
		t.Errorf("bottom(2) = %q, %v; want %q, true", got, ok, "b\nc")
	}
	if _, ok := extractRegion(text, regionSpec{name: regionUnavailable}); ok {
		t.Errorf("unavailable region should report ok=false")
	}
}

// TestBottomNonEmptyLines pins the tail-confinement semantics inherited from the former
// status.paneTail: trailing blank lines are dropped, then the last n are kept. The
// transcript-spoof case (a marker sitting high in scrollback with blank padding below the
// live status) must fall outside the window.
func TestBottomNonEmptyLines(t *testing.T) {
	cases := []struct {
		name string
		text string
		n    int
		want string
	}{
		{"drops trailing blanks", "x\ny\n\n\n", 2, "x\ny"},
		{"fewer lines than n", "only", 6, "only"},
		{"empty", "", 6, ""},
		// The window takes the last 6 lines after dropping only TRAILING blanks; interior
		// blank padding is kept, so the busy/transcript marker high above is excluded even
		// though some blank lines fall inside the window. This is the exact former
		// status.paneTail behavior the status attention tests rely on.
		{
			"transcript marker excluded",
			"we discussed esc to interrupt earlier\n\n\n\n\n\n\n\n\n\n\n\n\n│ > Try \"fix the bug\"        │\n  ? for shortcuts",
			6,
			"\n\n\n\n│ > Try \"fix the bug\"        │\n  ? for shortcuts",
		},
	}
	for _, c := range cases {
		if got := bottomNonEmptyLines(c.text, c.n); got != c.want {
			t.Errorf("%s: bottomNonEmptyLines(_, %d) = %q, want %q", c.name, c.n, got, c.want)
		}
	}
}
