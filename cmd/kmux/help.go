package main

// helpText is printed by `kmux --help`. It documents every mode and the
// dashboard keybindings, so a user can discover the full surface without
// leaving the terminal. Keep the keybindings in sync with the Go defaults in
// internal/config (DefaultKeybindings) and the footer hints in internal/tui.
const helpText = `kmux — a TUI for monitoring parallel AI coding agents (claude, opencode).

Each agent runs in its own tmux session. kmux runs as a left sidebar inside a
kitty window and auto-attaches every matching session into its own pane.

USAGE
  kmux [path]                     Launch the dashboard (needs kitty + tmux).
  kmux --agent <kind> [path]      Launch/attach one agent in the terminal (tmux only).
  kmux --session <kind> [path]    Print the agent's tmux session name and exit.
  kmux --project                  Print the project dir of the current agent session.
  kmux --help                     Show this help.

  <kind> is 'claude' or 'opencode'. [path] defaults to the current directory and
  may be a repo, a linked worktree, or any subdirectory of one; it scopes kmux to
  that single git project.

MODES
  Dashboard (default)
    Run inside a kitty window (with remote control enabled); that window becomes
    the sidebar and agent panes open to its right. Pass a path to scope kmux to a
    single project instead of scanning ~/git plus your configured folders.

      kmux                        Scan ~/git and configured folders.
      kmux ~/git/myproject        Scope to one project.

  Launch an agent directly (--agent)
    Skip the dashboard and create (or attach to) the tmux session for one agent,
    attached to the current terminal. Needs only tmux — no kitty. The session name
    matches the dashboard's convention, so opening kmux later focuses this agent.

      kmux ~/git/myproject --agent claude
      kmux --agent opencode ~/git/myproject
      kmux --agent claude         Use the current directory.

  Print a session name (--session)
    Resolve the same session name without launching anything, for scripting.
    Needs neither tmux nor kitty.

      kmux --session claude                     # e.g. [kmux][CC]~/git/myproject
      tmux send-keys -t "$(kmux --session claude)" 'hi' Enter

  Print the current project dir (--project)
    From inside an agent's tmux session, print its git project directory — for
    scripts bound to tmux keybindings.

DASHBOARD KEYBINDINGS
  Navigation
    j / k, ↓ / ↑                  Move within a panel
    h / l, ← / →                  Switch panel
    1 / 2                         Focus Projects / Sessions panel

  Projects panel
    enter                         Launch an agent (pick project, then kind)
    c / o                         Launch claude / opencode
    t                             Open kmux for the project in a new kitty tab
    r                             Refresh git status
    f                             Fullscreen the focused agent
    D                             Kill the session

  Sessions panel
    enter                         Focus the agent's pane
    f                             Fullscreen the agent
    d                             Detach the agent
    D                             Kill the session

  Idle slots (empty launcher columns)
    c / o                         Pick a project, then launch claude / opencode
    enter                         Pick a project, then pick the agent kind
    q                             Close a spare idle slot

  General
    q                             Quit (ctrl+c always quits)

  Editor, lazygit, and other custom commands are configured via customCommands and
  appear in the footer. Navigation keys are rebindable via the keybindings config.

CONFIG
  kmux reads a default config shipped beside the binary, then overlays your
  optional ~/.config/kmux/config.yaml on top. See docs/configuration.md for the
  full reference (projects, idle_timeout, customCommands, keybindings).

More: https://github.com/olli-io/kmux
`
