# kmux

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

It builds from source and installs `kmux` to `~/.local/bin` (override with
`INSTALL_DIR` or `PREFIX`). Make sure the install dir is on your `PATH`.

## Run

Run it **inside a kitty window** — that window becomes the sidebar and agent
panes open to its right:

```sh
kmux
```

Pass a directory to scope kmux to a single git project. The path may be the main
worktree, a linked worktree, or any subdirectory of one:

```sh
kmux ~/git/myproject   # or: cd into the repo and run `kmux .`
```

## Launch an agent directly

With `--agent`, kmux skips the dashboard and creates (or attaches to) the tmux
session for one agent in the project containing the given path, attaching it to
the current terminal. This needs only tmux — no kitty:

```sh
kmux ~/git/myproject --agent claude     # path then flag
kmux --agent opencode ~/git/myproject   # flag then path, equivalent
kmux --agent claude                     # omit the path to use the current dir
```

The session name follows the same convention the dashboard uses (`<project>~cl`
for claude, `~oc` for opencode; worktrees become `<project>/<worktree>~…`), so an
agent launched this way is the very same session the dashboard manages — launch
it here, then open `kmux`, and it focuses that running agent.

## Config

An optional `~/.config/kmux/config.yaml` lets you list extra git project folders
to show in the **Projects** panel, alongside the repos found under `~/git`:

```yaml
# Extra project folders for the Projects panel.
projects:
  - ~/work/some-repo
  - /opt/code/another-repo

# Kill an agent whose pane sits unchanged this long, to free memory.
# A Go duration (e.g. 2h, 90m); 0, off, or never disables it. Default: 2h.
idle_timeout: 2h
```

Paths may use `~` and `$ENV` references and point at a main worktree, a linked
worktree, or a subdirectory of one. Entries that already live under `~/git` are
deduplicated.
