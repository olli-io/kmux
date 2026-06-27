package tui

import (
	"os"
	"os/exec"
	"path/filepath"
)

// nvimTabScriptPath resolves the nvim launcher installed alongside the kmux
// binary (install.sh drops scripts/nvim-tab.sh next to it). Resolving relative
// to the executable keeps kmux and the script versioned together and lets the
// aerospace leader-key binding invoke the very same installed file.
func nvimTabScriptPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	// Resolve symlinks so a linked binary still finds the script beside the real
	// install location.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Join(filepath.Dir(exe), "nvim-tab.sh"), nil
}

// OpenEditor focuses an existing nvim tab for dir, or opens a new one there, via
// the installed nvim-tab.sh launcher. Like OpenLazygit it is fire-and-forget:
// the script raises/launches the kitty window hosting nvim itself, so no managed
// pane is involved. The script returns promptly once it has handed off to kitty.
func OpenEditor(dir string) error {
	script, err := nvimTabScriptPath()
	if err != nil {
		return err
	}
	return exec.Command(script, dir).Run()
}
