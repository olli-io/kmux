package tui

import (
	"testing"

	"github.com/olli-io/kmux/internal/status"
)

// TestSessionAttnCmds covers the per-session [!!] marker flip tracking: a rename is
// emitted only when a session's permission status changes, and sessAttn tracks exactly
// the marked sessions (pruning ones that leave the poll).
func TestSessionAttnCmds(t *testing.T) {
	const a, b = "[kmux][CC]~/a", "[kmux][CC]~/b"

	m := &model{sessAttn: map[string]bool{}}

	// First poll: a needs permission, b is busy. Only a flips to marked.
	cmds := m.sessionAttnCmds(map[string]status.AttentionState{
		a: status.AttnPermission,
		b: status.AttnBusy,
	})
	if len(cmds) != 1 {
		t.Fatalf("poll 1: got %d cmds, want 1 (a marked)", len(cmds))
	}
	if !m.sessAttn[a] || m.sessAttn[b] {
		t.Fatalf("poll 1: sessAttn = %v, want only a marked", m.sessAttn)
	}

	// Second poll: unchanged states -> no renames (steady state shells out nothing).
	cmds = m.sessionAttnCmds(map[string]status.AttentionState{
		a: status.AttnPermission,
		b: status.AttnBusy,
	})
	if len(cmds) != 0 {
		t.Fatalf("poll 2: got %d cmds, want 0 (no flip)", len(cmds))
	}

	// Third poll: a resolves to waiting (clear its marker), b now needs permission.
	cmds = m.sessionAttnCmds(map[string]status.AttentionState{
		a: status.AttnWaiting,
		b: status.AttnPermission,
	})
	if len(cmds) != 2 {
		t.Fatalf("poll 3: got %d cmds, want 2 (a clear, b set)", len(cmds))
	}
	if m.sessAttn[a] || !m.sessAttn[b] {
		t.Fatalf("poll 3: sessAttn = %v, want only b marked", m.sessAttn)
	}

	// Fourth poll: b vanishes from the poll while still marked -> pruned, no rename.
	cmds = m.sessionAttnCmds(map[string]status.AttentionState{
		a: status.AttnWaiting,
	})
	if len(cmds) != 0 {
		t.Fatalf("poll 4: got %d cmds, want 0 (vanished session pruned silently)", len(cmds))
	}
	if len(m.sessAttn) != 0 {
		t.Fatalf("poll 4: sessAttn = %v, want empty (b pruned)", m.sessAttn)
	}
}
