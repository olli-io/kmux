// Package detect is kmux's YAML-driven, herdr-style agent-state detector. Each
// supported agent ships an embedded manifest (claude.yaml, opencode.yaml) holding a
// list of prioritized rules; a rule is a matcher gate (contains/regex/line_regex plus
// nested any/all/not) evaluated against a named region of the captured pane text. The
// modelled states are herdr's (working/blocked/idle/unknown); status.ClassifyAttention
// maps them onto kmux's AttentionState (working=>busy, blocked=>permission,
// idle/unknown=>waiting).
//
// Only pane text is available to kmux (tmux capture-pane -p), so herdr regions that
// need terminal escape data (osc_title, osc_progress) are treated as unavailable and
// their rules never match — that lets us mirror herdr's manifests without those rules
// firing. Manifests are parsed and compiled once at init (regexes compiled, contains
// needles lower-cased) so the dashboard poll never pays compile cost.
package detect

import (
	_ "embed"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// State is herdr's agent-state vocabulary. kmux maps it to its own AttentionState:
// working => busy, blocked => a visible permission prompt, idle/unknown => not busy.
type State string

const (
	StateWorking State = "working" // agent is actively generating
	StateBlocked State = "blocked" // agent is showing a prompt awaiting the user
	StateIdle    State = "idle"    // agent finished, plain prompt visible
	StateUnknown State = "unknown" // unrecognized / no rule matched
)

// Manifest is one agent's ruleset as parsed from its embedded YAML file. Agent is
// documentation only; the registry key comes from the embed wiring below.
type Manifest struct {
	Agent string `yaml:"agent"`
	Rules []Rule `yaml:"rules"`
}

// Rule is one detection rule: its Gate is evaluated against Region of the pane, and
// when it matches the rule contributes State at Priority. Highest priority among
// matching rules wins (see Classify); ties keep the first in file order. The Gate is
// inlined so a rule writes contains:/regex:/any:… at the top level, herdr-style.
//
// herdr's visible_* and skip_state_update flags are deliberately omitted: they drive
// herdr's richer multi-state UI, which kmux does not adopt (it only needs working vs.
// blocked). Add them here if that ever changes.
type Rule struct {
	ID       string `yaml:"id"`
	State    State  `yaml:"state"`
	Priority int    `yaml:"priority"`
	Region   string `yaml:"region"`
	Gate     `yaml:",inline"`
}

// Gate is a boolean matcher over a region's text. Every present clause is ANDed:
// the gate is true iff all of Contains, Regex, LineRegex, Any, All and Not that are
// specified hold. An empty gate is vacuously true. Gates nest recursively via
// Any/All/Not.
type Gate struct {
	Contains  []string `yaml:"contains,omitempty"`   // ALL substrings present (case-insensitive)
	Regex     []string `yaml:"regex,omitempty"`      // ALL match against the whole region
	LineRegex []string `yaml:"line_regex,omitempty"` // each pattern matches at least one line
	Any       []Gate   `yaml:"any,omitempty"`        // at least one nested gate true
	All       []Gate   `yaml:"all,omitempty"`        // every nested gate true
	Not       []Gate   `yaml:"not,omitempty"`        // no nested gate true
}

// compiledRule mirrors Rule with the region parsed and gate compiled, so the hot poll
// path only does string/regex matching, never parsing or regexp.Compile.
type compiledRule struct {
	state    State
	priority int
	region   regionSpec
	gate     compiledGate
}

// compiledManifest is a manifest ready for evaluation.
type compiledManifest struct {
	rules []compiledRule
}

//go:embed claude.yaml
var claudeYAML []byte

//go:embed opencode.yaml
var opencodeYAML []byte

// manifests is the compiled registry, keyed by agent kind (see agent.AgentKind).
var manifests = map[string]compiledManifest{}

func init() {
	for kind, raw := range map[string][]byte{
		"claude":   claudeYAML,
		"opencode": opencodeYAML,
	} {
		m, err := loadManifest(raw)
		if err != nil {
			// The manifests are embedded at build time: a parse/compile failure is a
			// shipped defect, not runtime input. Fail loud at startup rather than
			// silently degrade every session to "not busy". The parse tests guard this
			// so it never reaches a user's terminal.
			panic(fmt.Sprintf("detect: bad embedded manifest %q: %v", kind, err))
		}
		manifests[kind] = m
	}
}

// loadManifest parses one YAML manifest and compiles its rules. It is exported to the
// package (not init-only) so tests can exercise both the happy path and the error
// paths (bad YAML, bad regex, unknown region) directly.
func loadManifest(raw []byte) (compiledManifest, error) {
	var man Manifest
	if err := yaml.Unmarshal(raw, &man); err != nil {
		return compiledManifest{}, fmt.Errorf("parse yaml: %w", err)
	}
	out := compiledManifest{rules: make([]compiledRule, 0, len(man.Rules))}
	for _, r := range man.Rules {
		region, err := parseRegion(r.Region)
		if err != nil {
			return compiledManifest{}, fmt.Errorf("rule %q: %w", r.ID, err)
		}
		gate, err := compileGate(r.Gate)
		if err != nil {
			return compiledManifest{}, fmt.Errorf("rule %q: %w", r.ID, err)
		}
		out.rules = append(out.rules, compiledRule{
			state:    r.State,
			priority: r.Priority,
			region:   region,
			gate:     gate,
		})
	}
	return out, nil
}

// Classify returns the state of the highest-priority matching rule for kind's pane
// text (ties keep the first rule in file order), and whether kind has a manifest at
// all. Priority resolves overlaps deterministically (blocked outranks working in the
// shipped manifests). No matching rule => StateUnknown. This is the sole entrypoint
// kmux uses; status.ClassifyAttention maps its result to an AttentionState.
func Classify(kind, paneText string) (State, bool) {
	man, ok := manifests[kind]
	if !ok {
		return StateUnknown, false
	}
	best := 0
	result := StateUnknown
	matched := false
	for i := range man.rules {
		r := &man.rules[i]
		text, avail := extractRegion(paneText, r.region)
		if !avail || !r.gate.eval(text) {
			continue
		}
		if !matched || r.priority > best {
			best = r.priority
			result = r.state
			matched = true
		}
	}
	return result, true
}

// Known reports whether kind has an embedded manifest (i.e. is a recognized agent).
func Known(kind string) bool {
	_, ok := manifests[kind]
	return ok
}

// lower is the single case-folding used everywhere in the engine, so needles compiled
// at load time and haystacks matched at poll time fold identically.
func lower(s string) string { return strings.ToLower(s) }
