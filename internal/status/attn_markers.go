package status

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/olli-io/kmux/internal/config"
)

// The attention marker dir is how an agent's own lifecycle hooks/plugins tell the
// dashboard that a session needs the user, replacing the fragile pane-text scraping
// of permission prompts. `kmux --attn` (invoked by a Claude Code hook or an
// OpenCode plugin) writes one small file per session that currently needs
// attention, named for the session and holding the state word; the dashboard poll
// reads them via LoadAttnMarkers and folds them into the per-session AttentionState
// (see tui.attentionCmd). Pane text still owns the busy state (see attentionMarkers).

// AttnMarkerPermission / AttnMarkerWaiting are the marker file contents for the two
// hook-reported states. The file body is exactly one of these words. They're
// exported so the writer (agent.RunAttnHook) and this reader agree on the vocabulary.
const (
	AttnMarkerPermission = "permission" // blocked on a prompt awaiting an answer
	AttnMarkerWaiting    = "waiting"     // finished / idle, awaiting user input
)

// attnMarkerTTL bounds how long a marker is trusted without being refreshed. The
// resolving event (Claude's UserPromptSubmit/Stop, OpenCode's permission.replied)
// removes the marker directly; the TTL is only a fallback so a crashed or
// never-fired resolve event can't pin a stale [!!] forever. LoadAttnMarkers sweeps
// markers older than this.
const attnMarkerTTL = 30 * time.Minute

// AttnDir returns kmux's attention marker directory (~/.config/kmux/attention),
// creating it as needed. It shares config.ConfigDir with the rest of kmux's state.
func AttnDir() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	attn := filepath.Join(dir, "attention")
	if err := os.MkdirAll(attn, 0o755); err != nil {
		return "", err
	}
	return attn, nil
}

// AttnMarkerName encodes a session name into a single safe filename. Session names
// contain '/', '[', ']', '@' and '~', so url.PathEscape maps them to one path
// segment; the writer (kmux --attn) and this reader share it so the round-trip is
// exact.
func AttnMarkerName(session string) string {
	return url.PathEscape(session)
}

// WriteAttnMarker records that session needs attention in state (AttnMarkerPermission or
// AttnMarkerWaiting), overwriting any prior marker. Its mtime is refreshed on every write,
// which the TTL sweep in LoadAttnMarkers relies on.
func WriteAttnMarker(session, state string) error {
	dir, err := AttnDir()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, AttnMarkerName(session)), []byte(state), 0o644)
}

// RemoveAttnMarker clears session's attention marker (its work is resolved). A
// missing marker is not an error.
func RemoveAttnMarker(session string) error {
	dir, err := AttnDir()
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, AttnMarkerName(session))); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// LoadAttnMarkers reads the attention dir and returns session -> AttentionState for
// every live marker: a "permission" body maps to AttnPermission, "waiting" to
// AttnWaiting. Markers whose mtime is older than attnMarkerTTL are ignored and
// removed (a lost resolve event mustn't strand a stale state). A missing dir yields
// an empty map, not an error; unreadable individual files are skipped.
func LoadAttnMarkers(now time.Time) (map[string]AttentionState, error) {
	dir, err := AttnDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]AttentionState{}, nil
		}
		return nil, err
	}
	out := make(map[string]AttentionState, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > attnMarkerTTL {
			_ = os.Remove(path) // stale: swept
			continue
		}
		session, err := url.PathUnescape(e.Name())
		if err != nil {
			continue // not one of ours
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		switch strings.TrimSpace(string(data)) {
		case AttnMarkerPermission:
			out[session] = AttnPermission
		case AttnMarkerWaiting:
			out[session] = AttnWaiting
		}
	}
	return out, nil
}
