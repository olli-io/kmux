package detect

import "testing"

// mustGate compiles a Gate for tests, failing the test on a compile error.
func mustGate(t *testing.T, g Gate) compiledGate {
	t.Helper()
	c, err := compileGate(g)
	if err != nil {
		t.Fatalf("compileGate(%+v): %v", g, err)
	}
	return c
}

func TestGateEval(t *testing.T) {
	cases := []struct {
		name string
		gate Gate
		text string
		want bool
	}{
		{"empty gate is true", Gate{}, "anything", true},
		{"contains present", Gate{Contains: []string{"esc to int"}}, "  esc to interrupt", true},
		{"contains case-insensitive", Gate{Contains: []string{"esc to int"}}, "  ESC TO INTERRUPT", true},
		{"contains absent", Gate{Contains: []string{"esc to int"}}, "idle", false},
		{"contains all present", Gate{Contains: []string{"do you", "proceed"}}, "do you want to proceed?", true},
		{"contains not all present", Gate{Contains: []string{"do you", "cancel"}}, "do you want to proceed?", false},

		{"regex full-text", Gate{Regex: []string{`pro.eed`}}, "please proceed now", true},
		{"regex no match", Gate{Regex: []string{`^yes$`}}, "yes sir", false},

		{"line_regex matches a line", Gate{LineRegex: []string{`^\s*❯\s*1\.\s*yes`}}, "prompt\n ❯ 1. Yes\n   2. No", false},
		{"line_regex ci matches a line", Gate{LineRegex: []string{`(?i)^\s*❯?\s*1\.\s*yes`}}, "prompt\n 1. Yes\n   2. No", true},
		{"line_regex no line matches", Gate{LineRegex: []string{`^done$`}}, "a\nb\nc", false},

		{"any one true", Gate{Any: []Gate{{Contains: []string{"x"}}, {Contains: []string{"proceed"}}}}, "proceed", true},
		{"any none true", Gate{Any: []Gate{{Contains: []string{"x"}}, {Contains: []string{"y"}}}}, "proceed", false},
		{"all true", Gate{All: []Gate{{Contains: []string{"do"}}, {Contains: []string{"proceed"}}}}, "do you proceed", true},
		{"all one false", Gate{All: []Gate{{Contains: []string{"do"}}, {Contains: []string{"cancel"}}}}, "do you proceed", false},
		{"not blocks", Gate{Contains: []string{"do"}, Not: []Gate{{Contains: []string{"proceed"}}}}, "do you proceed", false},
		{"not allows", Gate{Contains: []string{"do"}, Not: []Gate{{Contains: []string{"cancel"}}}}, "do you proceed", true},

		{
			"nested any+all",
			Gate{
				Contains: []string{"do you want to proceed?"},
				All: []Gate{{Any: []Gate{
					{Contains: []string{"1. yes"}},
					{Contains: []string{"2. no"}},
				}}},
			},
			"do you want to proceed?\n 2. No",
			true,
		},
	}
	for _, c := range cases {
		g := mustGate(t, c.gate)
		if got := g.eval(c.text); got != c.want {
			t.Errorf("%s: eval(%q) = %v, want %v", c.name, c.text, got, c.want)
		}
	}
}

func TestCompileGateBadRegex(t *testing.T) {
	if _, err := compileGate(Gate{Regex: []string{`(`}}); err == nil {
		t.Fatal("expected compile error for bad regex, got nil")
	}
	if _, err := compileGate(Gate{Any: []Gate{{LineRegex: []string{`[`}}}}); err == nil {
		t.Fatal("expected compile error for bad nested line_regex, got nil")
	}
}
