package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/olli-io/kmux/internal/status"
)

// attnAction is what an agent lifecycle event tells kmux to do with the session's
// attention marker.
type attnAction int

const (
	attnNone       attnAction = iota // event we don't act on
	attnSetPerm                      // session is blocked on a prompt awaiting an answer
	attnSetWaiting                   // session finished / went idle, awaiting input
	attnClear                        // the attention was resolved
)

// attnEvent is the subset of a Claude Code hook payload kmux reads from stdin. The
// OpenCode plugin passes the same information as flags instead (see RunAttnHook).
type attnEvent struct {
	HookEventName    string `json:"hook_event_name"`   // e.g. "Notification", "Stop", "UserPromptSubmit"
	NotificationType string `json:"notification_type"` // e.g. "permission_prompt", "idle_prompt"
	Cwd              string `json:"cwd"`
}

// RunAttnHook is the `kmux --attn` entry point: it's invoked by an agent's own
// lifecycle machinery (a Claude Code hook or an OpenCode plugin) when a session
// changes attention state, and it writes or clears that session's marker in the
// attention dir (see status.WriteAttnMarker) so the dashboard can surface it.
//
// The event arrives one of two ways. The OpenCode plugin passes it as flags
// (pa.AttnEvent / pa.AttnCwd / pa.AttnKind). The Claude Code hook passes nothing on
// the command line and pipes its event JSON on stdin, so when pa.AttnEvent is empty
// RunAttnHook decodes stdin. Either way the working directory is mapped to the exact
// [kmux][CC|OC]… session name via sessionPlan (worktree- and orphan-aware), matching
// the name the dashboard shows.
//
// It is best-effort by contract: a hook must never fail an agent turn. Callers
// (main.go) log any returned error to stderr and exit 0. An unrecognized event is a
// no-op, not an error.
func RunAttnHook(pa ParsedArgs, stdin io.Reader) error {
	event, ntype, cwd, kind := pa.AttnEvent, "", pa.AttnCwd, pa.AttnKind
	if event == "" {
		// Claude Code hook path: the payload is on stdin.
		var ev attnEvent
		data, err := io.ReadAll(stdin)
		if err != nil {
			return fmt.Errorf("read hook stdin: %w", err)
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			return fmt.Errorf("decode hook payload: %w", err)
		}
		event, ntype, cwd = ev.HookEventName, ev.NotificationType, ev.Cwd
	}
	if kind == "" {
		kind = "claude"
	}
	if cwd == "" {
		return fmt.Errorf("no cwd in attention event")
	}

	action := classifyAttnEvent(event, ntype)
	if action == attnNone {
		return nil
	}

	name, _, err := sessionPlan(cwd, kind)
	if err != nil {
		return fmt.Errorf("resolve session for %q: %w", cwd, err)
	}

	switch action {
	case attnSetPerm:
		return status.WriteAttnMarker(name, status.AttnMarkerPermission)
	case attnSetWaiting:
		return status.WriteAttnMarker(name, status.AttnMarkerWaiting)
	case attnClear:
		return status.RemoveAttnMarker(name)
	}
	return nil
}

// classifyAttnEvent maps an agent's event name (and Claude's notification_type
// sub-kind) to an attnAction. Matching is lowercase substring so it tolerates
// wording/version drift and the differing vocabularies of the two agents:
//
//   - Claude Code (Notification hook): notification_type "permission_prompt" →
//     permission, "idle_prompt" → waiting. Stop / UserPromptSubmit → clear.
//   - OpenCode (plugin event.type): "permission.updated"/"permission.asked" →
//     permission, "session.idle" → waiting, "permission.replied" → clear.
//
// Anything else is attnNone. permission.replied is checked before the bare
// "permission" test so it clears rather than re-arms.
func classifyAttnEvent(event, ntype string) attnAction {
	e := strings.ToLower(event)
	n := strings.ToLower(ntype)

	// Resolved-attention signals (checked first so "permission.replied" doesn't fall
	// into the permission branch below).
	switch {
	case strings.Contains(e, "userpromptsubmit"),
		strings.Contains(e, "permission.replied"):
		return attnClear
	case e == "stop":
		return attnClear
	}

	// Permission requests.
	if strings.Contains(n, "permission") ||
		strings.Contains(e, "permission.updated") ||
		strings.Contains(e, "permission.asked") {
		return attnSetPerm
	}

	// Idle / waiting-for-input.
	if strings.Contains(n, "idle") || strings.Contains(e, "session.idle") {
		return attnSetWaiting
	}

	return attnNone
}
