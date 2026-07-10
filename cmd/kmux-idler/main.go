// Command kmux-idler is the one-shot launcher picker kmux uses for its idle slots.
// The slot itself is held by a cheap shell loop, not this program: kmux-idler is
// spawned only while the user is choosing what to launch, then exits. On selection
// it creates the agent's tmux session detached and exits, and the dashboard gives
// it a managed pane on its next poll.
package main

import (
	"fmt"
	"os"

	"github.com/olli-io/kmux/internal/idler"
)

func main() {
	// `--idle-loop` execs the interactive hold loop that placeholder panes run, so a
	// blank pane the user spawned outside the dashboard becomes a kmux idle slot.
	if len(os.Args) > 1 && os.Args[1] == "--idle-loop" {
		if err := idler.RunIdleLoop(); err != nil {
			fmt.Fprintf(os.Stderr, "kmux-idler: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// `--quit` closes this idle pane, but only when the layout has a spare beyond the
	// core sidebar + maxColumns panes, which can never be quit away. Bound to `q`.
	if len(os.Args) > 1 && os.Args[1] == "--quit" {
		if err := idler.QuitIfSpare(); err != nil {
			fmt.Fprintf(os.Stderr, "kmux-idler: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// `--can-quit` probes whether `--quit` would close this pane, so the idle loop
	// shows the `q` hint only on a zero exit: 0 = can quit, 1 = cannot, 2 = error.
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

	// The optional first argument is the agent kind; absent, the picker asks for it
	// after the project is chosen.
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
	// managed pane.
	if err := idler.Start(launch); err != nil {
		fmt.Fprintf(os.Stderr, "kmux-idler: %v\n", err)
		os.Exit(1)
	}
}
