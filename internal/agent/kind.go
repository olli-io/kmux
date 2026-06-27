package agent

import "strings"

// AgentKind returns "claude", "opencode", or "" for a session name. The agent
// suffix is matched case-insensitively.
func AgentKind(name string) string {
	switch {
	case agentSuffix(name, "cl"):
		return "claude"
	case agentSuffix(name, "oc"):
		return "opencode"
	default:
		return ""
	}
}

// agentSuffix reports whether name ends in "~"+suffix, case-insensitively.
func agentSuffix(name, suffix string) bool {
	return strings.HasSuffix(strings.ToLower(name), "~"+suffix)
}
