package main

import (
	"fmt"
	"os"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	if os.Getenv("KITTY_LISTEN_ON") == "" {
		fmt.Fprintln(os.Stderr, "kmux: KITTY_LISTEN_ON is not set.")
		fmt.Fprintln(os.Stderr, "Run kmux inside kitty with remote control enabled:")
		fmt.Fprintln(os.Stderr, "  allow_remote_control yes")
		fmt.Fprintln(os.Stderr, "  listen_on unix:@kitty")
		os.Exit(1)
	}

	sidebarID, err := strconv.Atoi(os.Getenv("KITTY_WINDOW_ID"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "kmux: KITTY_WINDOW_ID is not set; run kmux inside a kitty window.")
		os.Exit(1)
	}

	if err := GotoLayoutSplits(); err != nil {
		fmt.Fprintf(os.Stderr, "kmux: could not switch to splits layout: %v\n", err)
		os.Exit(1)
	}

	mgr := NewManager(sidebarID)
	// AltScreen gives a clean, full-pane dashboard (clears on launch, restores on exit).
	p := tea.NewProgram(newModel(mgr), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		mgr.CloseAll()
		fmt.Fprintf(os.Stderr, "kmux: %v\n", err)
		os.Exit(1)
	}
}
