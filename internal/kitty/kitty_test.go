package kitty

import "testing"

func TestIsBareShell(t *testing.T) {
	tests := []struct {
		name string
		cmd  []string
		want bool
	}{
		{"plain bash", []string{"bash"}, true},
		{"login shell", []string{"-zsh"}, true},
		{"absolute path", []string{"/usr/bin/fish"}, true},
		{"login flag", []string{"/bin/bash", "-l"}, true},
		{"idle-loop placeholder", []string{"sh", "-c", "idler=...; while :; do :; done"}, false},
		{"command tab", []string{"sh", "-c", "lazygit"}, false},
		{"tmux client", []string{"tmux", "attach", "-t", "x‧CC"}, false},
		{"kmux sidebar", []string{"kmux"}, false},
		{"running editor", []string{"nvim", "main.go"}, false},
		{"empty", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBareShell(tc.cmd); got != tc.want {
				t.Errorf("isBareShell(%v) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestWindowIsBareShell(t *testing.T) {
	bare := lsWindow{ForegroundProcesses: []lsProcess{{Cmdline: []string{"bash"}}}}
	if !windowIsBareShell(bare) {
		t.Error("a window whose only foreground process is a bare shell should match")
	}
	none := lsWindow{}
	if windowIsBareShell(none) {
		t.Error("a window with no foreground processes should not match")
	}
	busy := lsWindow{ForegroundProcesses: []lsProcess{{Cmdline: []string{"bash"}}, {Cmdline: []string{"vim"}}}}
	if windowIsBareShell(busy) {
		t.Error("a window also running a non-shell process should not match")
	}
}
