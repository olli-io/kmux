package detect

import (
	"fmt"
	"strconv"
	"strings"
)

// regionName enumerates the pane regions the engine can extract. Only pane-text
// regions are implemented; herdr regions that need terminal escape data or richer
// layout parsing map to regionUnavailable and cause their rules never to match.
type regionName int

const (
	regionWholeRecent    regionName = iota // the full captured pane text
	regionBottomNonEmpty                   // the last N non-trailing-blank lines
	regionUnavailable                      // recognized herdr region kmux can't provide
)

// regionSpec is a parsed rule region: a name plus the argument for the forms that
// take one (bottom_non_empty_lines(N)).
type regionSpec struct {
	name regionName
	n    int
}

// unavailableRegions are herdr region names kmux acknowledges but cannot produce from
// tmux pane text (OSC escape data) or has not implemented. A rule using one parses
// cleanly but never matches, so herdr manifests can be mirrored verbatim.
var unavailableRegions = map[string]bool{
	"osc_title":                  true,
	"osc_progress":               true,
	"prompt_box_body":            true,
	"after_last_horizontal_rule": true,
	"after_last_prompt_marker":   true,
}

// parseRegion parses a rule's region string. Bare identifiers name a region;
// name(N) forms carry an integer argument. Unknown-but-recognized herdr regions parse
// to regionUnavailable. A truly unrecognized or malformed region is an error so a
// typo in a manifest fails at load, not silently at runtime.
func parseRegion(spec string) (regionSpec, error) {
	s := strings.TrimSpace(spec)
	switch s {
	case "", "whole_recent":
		// Default region matches herdr: whole_recent.
		return regionSpec{name: regionWholeRecent}, nil
	}
	if unavailableRegions[s] {
		return regionSpec{name: regionUnavailable}, nil
	}
	if arg, ok := parenArg(s, "bottom_non_empty_lines"); ok {
		n, err := strconv.Atoi(arg)
		if err != nil || n <= 0 {
			return regionSpec{}, fmt.Errorf("invalid bottom_non_empty_lines arg %q", arg)
		}
		return regionSpec{name: regionBottomNonEmpty, n: n}, nil
	}
	return regionSpec{}, fmt.Errorf("unknown region %q", spec)
}

// parenArg returns the inner text of a name(arg) form, or ("", false) if s is not that
// form for the given name.
func parenArg(s, name string) (string, bool) {
	prefix := name + "("
	if !strings.HasPrefix(s, prefix) || !strings.HasSuffix(s, ")") {
		return "", false
	}
	return s[len(prefix) : len(s)-1], true
}

// extractRegion returns the text of a rule's region and whether it is available. An
// unavailable region yields ("", false); the caller treats that as a non-match.
func extractRegion(paneText string, r regionSpec) (string, bool) {
	switch r.name {
	case regionWholeRecent:
		return paneText, true
	case regionBottomNonEmpty:
		return bottomNonEmptyLines(paneText, r.n), true
	default:
		return "", false
	}
}

// bottomNonEmptyLines returns the last n lines after dropping trailing blank lines, so
// the window anchors on the agent's real bottom content rather than empty padding.
// This is the tail-confinement primitive that stops scrollback higher in the pane
// from spoofing the live status; it is a direct port of the former status.paneTail.
func bottomNonEmptyLines(text string, n int) string {
	lines := strings.Split(text, "\n")
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	start := end - n
	if start < 0 {
		start = 0
	}
	return strings.Join(lines[start:end], "\n")
}
