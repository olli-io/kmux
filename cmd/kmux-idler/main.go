// Command kmux-idler is the one-shot launcher picker kmux uses for its idle slots.
// An idle slot is held by a cheap shell loop (see internal/layout.placeholderCmd),
// not by this program — kmux-idler is spawned only for the moment the user is
// choosing what to launch, then exits. It takes an agent kind as its argument:
//
//	kmux-idler            pick a project, then pick the kind (the ↵ path)
//	kmux-idler claude     pick a project, launch Claude in it
//	kmux-idler opencode   pick a project, launch OpenCode in it
//	kmux-idler --quit     close this idle pane if the layout has a spare one
//	kmux-idler --idle-loop turn the current pane into a kmux idle slot
//
// On selection it creates the agent's tmux session detached and exits; the running
// kmux dashboard then gives the new session a managed pane on its next poll. On
// cancel it just exits and the shell loop redraws the idle hint.
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

	// `--quit` closes this idle pane — but only when the layout has more than the
	// dashboard plus its maxColumns panes, so the core sidebar + 3 panes can never be
	// quit away (a closed core placeholder would just respawn anyway). The idle
	// loop's `q` key calls this.
	if len(os.Args) > 1 && os.Args[1] == "--quit" {
		if err := idler.QuitIfSpare(); err != nil {
			fmt.Fprintf(os.Stderr, "kmux-idler: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// `--can-quit` probes whether `--quit` would close this pane (there is a spare
	// pane beyond the core layout). It is a predicate for the idle loop, which shows
	// the `q` hint only on a zero exit: 0 = can quit, 1 = cannot, 2 = error.
	if len(os.Args) > 1 && os.Args[1] == "--can-quit" {
		ok, err := idler.CanQuit()
		if err != nil {
			fmt.Fprintf(os.Stderr, "kmux-idler: %v\n", err)
			os.Exit(2)
		}
		if !ok {
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
	// Create the agent's tmux session detached; the dashboard's poll then gives it a
	// managed pane. Start returns once the session exists (or on failure).
	if err := idler.Start(launch); err != nil {
		fmt.Fprintf(os.Stderr, "kmux-idler: %v\n", err)
		os.Exit(1)
	}
}
