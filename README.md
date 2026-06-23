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
