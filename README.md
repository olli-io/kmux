# kmux

Agent dashboard for kitty.

`kmux` is a barebones TUI for monitoring parallel AI coding agents (claude,
opencode) that each run in their own tmux session. It runs as a left sidebar
inside a kitty window and auto-attaches every matching tmux session into its own
pane.

## Prerequisites

- **kitty** with remote control enabled. In `~/.config/kitty/kitty.conf`:
  ```
  allow_remote_control yes
  listen_on unix:@kitty
  ```
- **tmux**
- **Go** 1.21+ (to build)

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/olli-io/kmux/main/install.sh | sh
```

Or, from a checkout:

```sh
./install.sh
```

Either way it builds from source and installs `kmux` to `~/.local/bin`
(override with `INSTALL_DIR` or `PREFIX`). Make sure the install dir is on your
`PATH`.

## Run

Run it **inside a kitty window** — that window becomes the sidebar and agent
panes open to its right:

```sh
kmux
```

## Usage

- Polls tmux every 2s for sessions named `*_cl` (claude) or `*_oc` (opencode),
  attaching each into its own pane and closing it when the session ends.
- The sidebar has two panels: **[1] Sessions** (live agents, grouped by project
  and worktree) and **[2] Projects** (git repos under `~/git`).
- **Navigate**: arrow or vim keys (`j`/`k` move, `h`/`l` collapse/expand,
  `1`/`2` switch panel).
- `enter`/`space`/`l` — focus a session's pane, or open the agent picker on a
  project/worktree to start `claude` or `opencode` there.
- `d` — kill an agent. `h` — hide a session without detaching. `g` — open
  lazygit for that directory. `q` / `ctrl-c` — quit.

Quitting closes the panes kmux spawned, which only **detaches** tmux — the agent
sessions keep running.
