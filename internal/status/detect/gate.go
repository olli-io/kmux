package detect

import (
	"regexp"
	"strings"
)

// compiledGate mirrors Gate with regexes compiled and contains needles lower-cased,
// built once at load time so eval never compiles a regex on the poll path.
type compiledGate struct {
	contains  []string         // already lower-cased
	regex     []*regexp.Regexp // matched against the whole region
	lineRegex []*regexp.Regexp // each must match at least one line
	any       []compiledGate
	all       []compiledGate
	not       []compiledGate
}

// compileGate compiles one gate (recursively). A regex that fails to compile is a
// manifest error surfaced at load time.
func compileGate(g Gate) (compiledGate, error) {
	out := compiledGate{}
	for _, c := range g.Contains {
		out.contains = append(out.contains, lower(c))
	}
	for _, p := range g.Regex {
		re, err := regexp.Compile(p)
		if err != nil {
			return compiledGate{}, err
		}
		out.regex = append(out.regex, re)
	}
	for _, p := range g.LineRegex {
		re, err := regexp.Compile(p)
		if err != nil {
			return compiledGate{}, err
		}
		out.lineRegex = append(out.lineRegex, re)
	}
	for _, nested := range g.Any {
		c, err := compileGate(nested)
		if err != nil {
			return compiledGate{}, err
		}
		out.any = append(out.any, c)
	}
	for _, nested := range g.All {
		c, err := compileGate(nested)
		if err != nil {
			return compiledGate{}, err
		}
		out.all = append(out.all, c)
	}
	for _, nested := range g.Not {
		c, err := compileGate(nested)
		if err != nil {
			return compiledGate{}, err
		}
		out.not = append(out.not, c)
	}
	return out, nil
}

// eval reports whether the gate matches regionText. Every present clause is ANDed;
// an empty gate is vacuously true. Matching is case-insensitive: the region is
// lower-cased once here and contains needles were lower-cased at compile time.
// Regexes match the region verbatim (case folding is the pattern author's job via
// (?i)).
func (g compiledGate) eval(regionText string) bool {
	lc := lower(regionText)

	for _, sub := range g.contains {
		if !strings.Contains(lc, sub) {
			return false
		}
	}
	for _, re := range g.regex {
		if !re.MatchString(regionText) {
			return false
		}
	}
	if len(g.lineRegex) > 0 {
		lines := strings.Split(regionText, "\n")
		for _, re := range g.lineRegex {
			if !matchesAnyLine(re, lines) {
				return false
			}
		}
	}
	for _, nested := range g.all {
		if !nested.eval(regionText) {
			return false
		}
	}
	if len(g.any) > 0 {
		ok := false
		for _, nested := range g.any {
			if nested.eval(regionText) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	for _, nested := range g.not {
		if nested.eval(regionText) {
			return false
		}
	}
	return true
}

// matchesAnyLine reports whether re matches at least one of lines.
func matchesAnyLine(re *regexp.Regexp, lines []string) bool {
	for _, line := range lines {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}
