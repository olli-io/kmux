# kmux

Agent dashboard for kitty.

`kmux` is a barebones TUI for monitoring multiple parallel AI coding agents
(claude, opencode) that each run in their own tmux session. It runs as a left
sidebar inside a kitty window and uses kitty remote control to automatically
attach every matching tmux session into its own pane.

## What it does

- Polls tmux every 2s for sessions named `*_cl` (claude) or `*_oc` (opencode).
- Auto-attaches each one into a kitty pane (`tmux attach -t <session>`) and
  closes the pane when the session ends.
- Layout: sidebar + up to **3 vertical agent columns**; further agents **stack**
  horizontally under the least-populated column. After every change the panes are
  sized to fixed fractions of the tab width (**~16%** sidebar, **~28%** per agent
  column) and the agent columns are evened out.
- While fewer than 3 agent columns are active, the layout is padded with inert
  **placeholder panes** up to 3 columns, so every agent pane always renders at
  the same fixed width regardless of how many agents are running. The
  placeholders disappear automatically as real agents claim their slots.
- The sidebar is a lazygit-style dashboard split into two bordered panels:
  - **[1] Sessions** — live agent sessions grouped by project, then worktree
    (`<project>_<worktree>_<cl|oc>`), each with a `Claude`/`OpenCode` badge and
    attach mark. Sessions whose prefix matches no `~/git` project fall under
    `(ungrouped)`.
  - **[2] Projects** — every git repo under `~/git`. A repo with linked
    worktrees (from `git worktree list`) is shown as a collapsible folder
    (with a folder icon) whose expanded list begins with the main worktree,
    then each linked worktree; a repo with none is a single row.
- Navigate with the arrow keys or vim keys (`j`/`k` move, `h`/`l` collapse/expand
  a node, `1`/`2` jump between panels); the focused panel's border is
  highlighted. Press `enter`, `space`, or `l` on a session to give keyboard focus
  to its agent pane. Press `enter`, `space`, or `l` on a project or worktree to
  focus its `claude` session, or start one (`<project>_<worktree>_cl`, running
  `claude` in that directory) if it isn't running yet. Press `d` to **kill** an
  agent (ends the tmux session and closes its pane) — on a session row, or on a
  project/worktree whose session is running. On a session, press `h` to **hide**
  it from the panel without detaching (its pane and tmux session keep running; it
  reappears if it is restarted). Press `g` on any project, worktree, or session
  to open **lazygit** for that directory in kitty's quick-access terminal (a
  session resolves to its project/worktree directory). `q` / `ctrl-c` quits.
  - **Keys** — a bottom panel listing the keybinds for the currently focused
    panel; its contents switch as you move between **[1] Sessions** and
    **[2] Projects**.

Quitting closes the panes kmux spawned, which only **detaches** tmux — the
agent sessions keep running.

## Prerequisites

- **kitty** with remote control enabled. In `~/.config/kitty/kitty.conf`:
  ```
  allow_remote_control yes
  listen_on unix:@kitty
  ```
- **tmux**
- **Go** 1.21+ (to build)

## Build

```sh
go build -o kmux .
```

## Run

Run it **inside a kitty window** (it needs `KITTY_LISTEN_ON` and
`KITTY_WINDOW_ID`, which kitty sets automatically):

```sh
./kmux
```

That window becomes the sidebar; agent panes open to its right. The tab is
switched to kitty's `splits` layout on startup.

## Notes / limitations

- Attach is interactive, so you can type into any agent from its pane. tmux
  clamps a session's size to its smallest attached client — if a session is also
  attached full-size elsewhere, the dashboard pane will shrink it. Mitigate with
  `tmux set -g window-size latest` / `aggressive-resize on`.
- The sidebar is sized to ~16% of the tab width and columns are evened out on
  each layout change. If you manually resize panes, the next attach/detach will
  re-pin them.
