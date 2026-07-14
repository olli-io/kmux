package tui

import (
	"testing"

	"github.com/olli-io/kmux/internal/layout"
	"github.com/olli-io/kmux/internal/status"
)

// anyBusy is true iff some session is generating (AttnBusy), and false for an
// empty map or one holding only non-busy states.
func TestAnyBusy(t *testing.T) {
	if anyBusy(nil) {
		t.Errorf("anyBusy(nil) = true, want false")
	}
	idle := map[string]status.AttentionState{"a": status.AttnUnknown, "b": status.AttnWaiting}
	if anyBusy(idle) {
		t.Errorf("anyBusy(all idle) = true, want false")
	}
	busy := map[string]status.AttentionState{"a": status.AttnUnknown, "b": status.AttnBusy}
	if !anyBusy(busy) {
		t.Errorf("anyBusy(one busy) = false, want true")
	}
}

// The spinner ticker is armed on the idle->busy transition and stops once
// nothing is busy: an attentionMsg with a busy session sets spinning; a later
// all-idle attentionMsg leaves it running until the next spinnerMsg observes no
// busy session and clears it.
func TestSpinnerGating(t *testing.T) {
	m := newTestModel()

	// Idle attention: spinner stays off.
	updated, _ := m.Update(attentionMsg{states: map[string]status.AttentionState{"s": status.AttnWaiting}})
	m = updated.(model)
	if m.spinning {
		t.Fatalf("spinning after idle attentionMsg = true, want false")
	}

	// A busy session starts the spinner.
	updated, _ = m.Update(attentionMsg{states: map[string]status.AttentionState{"s": status.AttnBusy}})
	m = updated.(model)
	if !m.spinning {
		t.Fatalf("spinning after busy attentionMsg = false, want true")
	}

	// A spinnerMsg while still busy keeps it armed and advances the frame.
	before := m.spinnerFrame
	updated, cmd := m.Update(spinnerMsg{})
	m = updated.(model)
	if !m.spinning || cmd == nil {
		t.Fatalf("spinnerMsg while busy: spinning=%v cmd=%v, want armed", m.spinning, cmd != nil)
	}
	if m.spinnerFrame != before+1 {
		t.Fatalf("spinnerFrame = %d, want %d", m.spinnerFrame, before+1)
	}

	// Once nothing is busy, the next spinnerMsg stops the ticker.
	updated, _ = m.Update(attentionMsg{states: map[string]status.AttentionState{"s": status.AttnWaiting}})
	m = updated.(model)
	updated, cmd = m.Update(spinnerMsg{})
	m = updated.(model)
	if m.spinning || cmd != nil {
		t.Fatalf("spinnerMsg while idle: spinning=%v cmd=%v, want stopped", m.spinning, cmd != nil)
	}
}

// newTestModel builds a model with the maps the attentionMsg handler touches
// initialized, so Update can run without persisted state. It carries a bare
// layout.Manager so the handler's tab-title re-title (which reads the sidebar id)
// doesn't dereference a nil manager.
func newTestModel() model {
	return model{
		mgr:        layout.NewManager(0),
		attention:  map[string]status.AttentionState{},
		detached:   map[string]bool{},
		idle:       status.NewIdleTrackerFrom(0, nil), // timeout 0 disables reaping
		idledPanes: map[int]bool{},
		collapsed:  map[string]bool{},
	}
}
