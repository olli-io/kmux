package main

// helpText is printed by `kmux --help`. It documents every mode and the
// dashboard keybindings, so a user can discover the full surface without
// leaving the terminal. Keep the keybindings in sync with the Go defaults in
// internal/config (DefaultKeybindings) and the footer hints in internal/tui.
const helpText = `kmux — a TUI for monitoring parallel AI coding agents (claude, opencode, pi).

Each agent runs in a tmux pane. kmux runs as the left pane of one tmux window
and shows the agent panes to its right, in three fixed columns.

USAGE
  kmux [path]                     Launch the dashboard (needs tmux).
  kmux --agent <kind> [path]      Launch/attach one agent in the terminal (tmux only).
  kmux --config [--agent <kind>]  Launch/attach a config session in ~/.config.
  kmux --session <kind> [path]    Print the agent's tmux session name and exit.
  kmux --pane <kind> [path]       Print the tmux pane id of the running agent.
  kmux --project                  Print the project dir of the current agent session.
  kmux --help                     Show this help.

  <kind> is 'claude', 'opencode' or 'pi'. [path] defaults to the current
  directory and may be a repo, a linked worktree, or any subdirectory of one; it
  scopes kmux to that single git project.

MODES
  Dashboard (default)
    Run in any terminal. Outside tmux, kmux starts a tmux session and attaches to
    it. In tmux, the current window becomes the grid. Pass a path to scope kmux to
    a single project instead of scanning ~/git plus your configured folders.

      kmux                        Scan ~/git and configured folders.
      kmux ~/git/myproject        Scope to one project.

  Launch an agent directly (--agent)
    Skip the dashboard and create (or attach to) the tmux session for one agent,
    attached to the current terminal. The session name matches the dashboard's
    convention, so opening kmux later moves this agent into the grid.

      kmux ~/git/myproject --agent claude
      kmux --agent opencode ~/git/myproject
      kmux --agent claude         Use the current directory.

  Print a session name (--session)
    Resolve the same session name without launching anything, for scripting.
    Does not need tmux. A session with this name exists only while the agent is
    not in a grid.

      kmux --session claude                     # e.g. [kmux][CC]~/git/myproject

  Print a pane id (--pane)
    Print the tmux pane id of the running agent, in a grid or not.

      tmux send-keys -t "$(kmux --pane claude)" 'hi' Enter

  Config session (--config)
    Launch an agent (claude by default) in ~/.config. With the sandbox enabled
    it runs under the config settings file, which lets it edit ~/.config.

  Sandbox
    With "sandbox: {command: srt}" in the config, every agent runs inside srt and
    skips its own permission prompts. See docs/configuration.md.

  Print the current project dir (--project)
    From inside an agent's tmux pane, print its git project directory — for
    scripts bound to tmux keybindings.

DASHBOARD KEYBINDINGS
  Navigation
    j / k, ↓ / ↑                  Move within a panel
    h / l, ← / →                  Switch panel
    1 / 2                         Focus Projects / Sessions panel

  Projects panel
    enter                         Launch an agent (pick project, then kind)
    c / o / p                     Launch claude / opencode / pi
    t                             Open kmux for the project in a new tmux window
    r                             Refresh git status
    f                             Zoom the agent to full window size
    D                             Kill the agent

  Sessions panel
    enter                         Select the agent's pane (move it into the grid if necessary)
    f                             Zoom the agent to full window size (again: end the zoom)
    d                             Move the agent out of the grid (it continues to run)
    D                             Kill the agent

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
