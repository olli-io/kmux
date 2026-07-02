package tui

import (
	"testing"

	"github.com/olli-io/kmux/internal/layout"
)

// dismissLauncher is one-shot: with no splash (launcherID 0) it does nothing;
// with a splash it returns a teardown cmd exactly once and then no-ops, so the
// two triggers (first reconcile and the cap timer) never close the tab twice.
func TestDismissLauncherOneShot(t *testing.T) {
	// No splash: nothing to dismiss, and launched stays false.
	none := &model{mgr: layout.NewManager(1)}
	if cmd := none.dismissLauncher(); cmd != nil {
		t.Errorf("dismissLauncher with launcherID 0 = non-nil cmd, want nil")
	}
	if none.launched {
		t.Errorf("launched set with no splash to dismiss")
	}

	// With a splash: first call tears down and latches; second call no-ops.
	m := &model{mgr: layout.NewManager(1), launcherID: 7}
	if cmd := m.dismissLauncher(); cmd == nil {
		t.Fatalf("first dismissLauncher = nil cmd, want the teardown cmd")
	}
	if !m.launched {
		t.Errorf("launched not set after dismissing the splash")
	}
	if cmd := m.dismissLauncher(); cmd != nil {
		t.Errorf("second dismissLauncher = non-nil cmd, want nil (already dismissed)")
	}
}

// Normal dismissal is gated on both a first reconcile AND the minimum hold: neither
// alone tears the splash down, but the second to arrive (in either order) does. The
// launcherCapMsg fallback dismisses on its own, ignoring the min-hold gate.
func TestLauncherRevealTriggers(t *testing.T) {
	// A lone reconcile marks the layout ready but holds the splash (min not elapsed).
	m := model{mgr: layout.NewManager(1), launcherID: 7}
	updated, cmd := m.Update(reconciledMsg{})
	m = updated.(model)
	if cmd != nil || m.launched {
		t.Errorf("reconcile alone dismissed the splash; want held for launcherMin")
	}
	if !m.layoutReady {
		t.Errorf("reconcile did not mark layoutReady")
	}
	// The min timer then completes the dismissal.
	updated, cmd = m.Update(launcherMinMsg{})
	if cmd == nil || !updated.(model).launched {
		t.Errorf("reconcile+min did not tear the splash down")
	}

	// Reverse order: min first (held, layout not ready), then reconcile dismisses.
	m = model{mgr: layout.NewManager(1), launcherID: 7}
	updated, cmd = m.Update(launcherMinMsg{})
	m = updated.(model)
	if cmd != nil || m.launched {
		t.Errorf("min alone dismissed the splash; want held until layoutReady")
	}
	if !m.minHeld {
		t.Errorf("min timer did not mark minHeld")
	}
	updated, cmd = m.Update(reconciledMsg{})
	if cmd == nil || !updated.(model).launched {
		t.Errorf("min+reconcile did not tear the splash down")
	}

	// The cap fallback dismisses on its own, no reconcile or min needed.
	m = model{mgr: layout.NewManager(1), launcherID: 7}
	updated, cmd = m.Update(launcherCapMsg{})
	if cmd == nil || !updated.(model).launched {
		t.Errorf("cap fallback did not tear the splash down")
	}
}
