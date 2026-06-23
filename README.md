# kmux

`kmux` is a barebones TUI for monitoring parallel AI coding agents (claude,
opencode) that each run in their own tmux session. It runs as a left sidebar
inside a kitty window and auto-attaches every matching tmux session into its own
pane. It is meant to be seamless with my neovim setup.

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

Pass a directory to scope kmux to a single git project: the **Sessions** and
**Projects** panels then show only that project (and its worktrees). The path may
be the main worktree, a linked worktree, or any subdirectory of one:

```sh
kmux ~/git/myproject   # or: cd into the repo and run `kmux .`
```

## Config

An optional `~/.config/kmux/config.yaml` lets you list extra git project folders
to show in the **Projects** panel (when launched without a directory argument),
alongside the repos found under `~/git`:

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

## Usage

- Polls tmux every 2s for sessions named `*~cl` (claude) or `*~oc` (opencode),
  attaching each into its own pane and closing it when the session ends.
- Reaps idle agents: a session whose pane sits completely unchanged for 2 hours
  (configurable via `idle_timeout`) is killed to free the memory its agent
  process holds. A generating agent (or one you're typing into) keeps changing
  its pane, so only truly dormant sessions are reaped — attached or detached
  alike.
- The sidebar has two panels: **[1] Sessions** (live agents, grouped by project
  and worktree) and **[2] Projects** (git repos under `~/git`, plus any folders
  from the config file, or just the scoped project when launched with a path).
- Each project/worktree row leads with a git-status mark: a green `✓` when clean,
  a red `M` when it has uncommitted (staged or unstaged) changes.
- **Navigate**: arrow or vim keys (`j`/`k` move, `h`/`l` collapse/expand,
  `1`/`2` switch panel).
- `enter`/`space`/`l` — focus a session's pane, or open the agent picker on a
  project/worktree to start `claude` or `opencode` there.
- `d` — kill an agent. `h` — hide a session without detaching. `g` — open
  lazygit for that directory. `e` — open (or focus) the editor for that
  directory. `t` — open the selected project's root in a new kitty tab running
  its own kmux. `q` / `ctrl-c` — quit.

Quitting closes the panes kmux spawned, which only **detaches** tmux — the agent
sessions keep running.
