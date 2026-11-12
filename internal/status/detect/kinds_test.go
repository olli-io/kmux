// Package detect_test holds the cross-package guard tying the detect registry to the
// agent registry. It is an external test package so it can import internal/agent
// without dragging that dependency into detect itself.
package detect_test

import (
	"testing"

	"github.com/olli-io/kmux/internal/agent"
	"github.com/olli-io/kmux/internal/status/detect"
)

// TestEveryKindHasAManifest pins the shipped manifests to the agent registry. A kind
// added to agent.Kinds without a manifest still launches and still gets a pane, but
// Classify reports it unknown forever: no spinner, no attention marker, and the idle
// reaper's only signal gone. Catching that here beats noticing it in a live dashboard.
func TestEveryKindHasAManifest(t *testing.T) {
	for _, k := range agent.Kinds() {
		if !detect.Known(k.Name) {
			t.Errorf("kind %q has no detect manifest; add internal/status/detect/%s.yaml and embed it", k.Name, k.Name)
		}
	}
}
