package agent

import (
	"path/filepath"
	"testing"
)

// TestSessionPlanOrphan verifies that a directory outside any git repository
// resolves to an orphaned session anchored at the directory itself, rather than
// erroring. t.TempDir() is created under the OS temp root (not a git repo).
func TestSessionPlanOrphan(t *testing.T) {
	dir := t.TempDir()
	// EvalSymlinks mirrors orphanPlan's own resolution (macOS /var -> /private/var).
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}

	name, planDir, err := sessionPlan(dir, "claude")
	if err != nil {
		t.Fatalf("sessionPlan(%q) err = %v, want nil", dir, err)
	}
	if planDir != resolved {
		t.Errorf("dir = %q, want %q", planDir, resolved)
	}
	if want := ExpectedSession(resolved, ""); name != want {
		t.Errorf("name = %q, want %q", name, want)
	}
	// An orphaned session belongs to no project and no worktree.
	if _, _, ok := MatchProject(name, []string{"/some/other/proj"}); ok {
		t.Errorf("MatchProject(%q) ok = true, want false", name)
	}
	if wt := WorktreeName(name); wt != "" {
		t.Errorf("WorktreeName(%q) = %q, want \"\"", name, wt)
	}
}

// TestSessionPlanOrphanMissing verifies a non-existent path errors rather than
// silently planning a session in a directory that isn't there.
func TestSessionPlanOrphanMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if _, _, err := sessionPlan(missing, "claude"); err == nil {
		t.Fatalf("sessionPlan(%q) err = nil, want error", missing)
	}
}

func TestParseArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    ParsedArgs
		wantErr bool
	}{
		{"empty", nil, ParsedArgs{}, false},
		{"path only", []string{"/g/x"}, ParsedArgs{Path: "/g/x"}, false},
		{"agent space", []string{"--agent", "claude"}, ParsedArgs{Agent: "claude"}, false},
		{"agent equals", []string{"--agent=opencode"}, ParsedArgs{Agent: "opencode"}, false},
		{"agent then path", []string{"--agent", "claude", "/g/x"}, ParsedArgs{Path: "/g/x", Agent: "claude"}, false},
		{"path then agent", []string{"/g/x", "--agent", "claude"}, ParsedArgs{Path: "/g/x", Agent: "claude"}, false},
		{"session space", []string{"--session", "claude"}, ParsedArgs{Agent: "claude", PrintSession: true}, false},
		{"session equals", []string{"--session=opencode"}, ParsedArgs{Agent: "opencode", PrintSession: true}, false},
		{"session with path", []string{"--session", "opencode", "/g/x"}, ParsedArgs{Path: "/g/x", Agent: "opencode", PrintSession: true}, false},
		{"agent missing value", []string{"--agent"}, ParsedArgs{}, true},
		{"session missing value", []string{"--session"}, ParsedArgs{}, true},
		{"bad kind", []string{"--agent", "vim"}, ParsedArgs{}, true},
		{"bad session kind", []string{"--session=vim"}, ParsedArgs{}, true},
		{"unknown flag", []string{"--nope"}, ParsedArgs{}, true},
		{"two paths", []string{"/a", "/b"}, ParsedArgs{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseArgs(c.args)
			if (err != nil) != c.wantErr {
				t.Fatalf("ParseArgs(%v) err = %v, wantErr %v", c.args, err, c.wantErr)
			}
			if c.wantErr {
				return
			}
			if got != c.want {
				t.Errorf("ParseArgs(%v) = %+v, want %+v", c.args, got, c.want)
			}
		})
	}
}
