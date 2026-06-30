// Command kmux-idler is the one-shot launcher picker kmux uses for its idle slots.
// An idle slot is held by a cheap shell loop (see internal/layout.placeholderCmd),
// not by this program — kmux-idler is spawned only for the moment the user is
// choosing what to launch, then exits. It takes an agent kind as its argument:
//
//	kmux-idler            pick a project, then pick the kind (the ↵ path)
//	kmux-idler claude     pick a project, launch Claude in it
//	kmux-idler opencode   pick a project, launch OpenCode in it
//
// On selection it execs the agent's tmux client in place, so the idle pane it ran
// in becomes the agent pane instantly; the running kmux dashboard then adopts that
// window. On cancel it just exits and the shell loop redraws the idle hint.
package main

import (
	"fmt"
	"os"

	"github.com/olli-io/kmux/internal/idler"
)

func main() {
	// `--idle-loop` turns the current pane into a kmux idle slot: it execs the
	// interactive hold loop (the same one layout's placeholder panes run) so the
	// pane shows the idle hint and launches the picker on a keypress. kmux sends
	// this into a blank pane the user spawned outside the dashboard.
	if len(os.Args) > 1 && os.Args[1] == "--idle-loop" {
		if err := idler.RunIdleLoop(); err != nil {
			fmt.Fprintf(os.Stderr, "kmux-idler: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// The optional first argument is the agent kind (claude/opencode); absent, the
	// picker asks for the kind after the project is chosen.
	kind := ""
	if len(os.Args) > 1 {
		kind = os.Args[1]
	}
	launch, err := idler.Run(kind)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kmux-idler: %v\n", err)
		os.Exit(1)
	}
	if launch == nil {
		return // cancelled — back to the idle hint
	}
	// Replace this process with the agent's tmux client: the idle pane becomes the
	// agent in place. Exec only returns on failure.
	if err := idler.Exec(launch); err != nil {
		fmt.Fprintf(os.Stderr, "kmux-idler: %v\n", err)
		os.Exit(1)
	}
}
