package status

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withTempConfig points ConfigDir at a temp dir for the duration of a test, so
// marker reads/writes never touch the real ~/.config/kmux.
func withTempConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func TestAttnMarkerRoundTrip(t *testing.T) {
	withTempConfig(t)
	const sess = "[kmux][CC]~/git/kmux@feat.branch"

	if err := WriteAttnMarker(sess, AttnMarkerPermission); err != nil {
		t.Fatalf("WriteAttnMarker: %v", err)
	}
	markers, err := LoadAttnMarkers(time.Now())
	if err != nil {
		t.Fatalf("LoadAttnMarkers: %v", err)
	}
	if got := markers[sess]; got != AttnPermission {
		t.Fatalf("marker[%q] = %d, want AttnPermission (%d)", sess, got, AttnPermission)
	}

	// Overwrite to waiting.
	if err := WriteAttnMarker(sess, AttnMarkerWaiting); err != nil {
		t.Fatalf("WriteAttnMarker(waiting): %v", err)
	}
	markers, _ = LoadAttnMarkers(time.Now())
	if got := markers[sess]; got != AttnWaiting {
		t.Fatalf("after overwrite marker[%q] = %d, want AttnWaiting (%d)", sess, got, AttnWaiting)
	}

	// Remove clears it (and removing again is not an error).
	if err := RemoveAttnMarker(sess); err != nil {
		t.Fatalf("RemoveAttnMarker: %v", err)
	}
	if err := RemoveAttnMarker(sess); err != nil {
		t.Fatalf("RemoveAttnMarker (second): %v", err)
	}
	markers, _ = LoadAttnMarkers(time.Now())
	if _, ok := markers[sess]; ok {
		t.Fatalf("marker still present after remove")
	}
}

func TestLoadAttnMarkersMissingDir(t *testing.T) {
	// Point at a temp base but never create the attention dir via a write; LoadAttnMarkers
	// creates it on demand (via AttnDir) and returns empty.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "nope"))
	markers, err := LoadAttnMarkers(time.Now())
	if err != nil {
		t.Fatalf("LoadAttnMarkers on missing dir: %v", err)
	}
	if len(markers) != 0 {
		t.Fatalf("expected empty markers, got %v", markers)
	}
}

func TestLoadAttnMarkersTTLSweep(t *testing.T) {
	withTempConfig(t)
	const sess = "[kmux][CC]~/git/old"
	if err := WriteAttnMarker(sess, AttnMarkerPermission); err != nil {
		t.Fatalf("WriteAttnMarker: %v", err)
	}
	// Backdate the marker's mtime beyond the TTL.
	dir, _ := AttnDir()
	path := filepath.Join(dir, AttnMarkerName(sess))
	old := time.Now().Add(-attnMarkerTTL - time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	markers, err := LoadAttnMarkers(time.Now())
	if err != nil {
		t.Fatalf("LoadAttnMarkers: %v", err)
	}
	if _, ok := markers[sess]; ok {
		t.Fatalf("stale marker was not swept")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale marker file still on disk (err=%v)", err)
	}
}
