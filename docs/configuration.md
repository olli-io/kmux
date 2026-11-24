# Configuration

kmux reads the default `config.yaml` shipped beside the binary, then overlays
`~/.config/kmux/config.yaml` on top. Create your own with only the keys you want
to change.

```yaml
# Extra project folders for the Projects panel (added to ~/git discovery).
# Paths may use ~ and $ENV; main worktree, linked worktree, or a subdir of one.
projects:
  - ~/git
  - ~/work

# Kill an agent whose pane sits unchanged this long. Go duration; off disables.
idle_timeout: 2h

customCommands:
  - key: e
    title: Editor      # shown in the Keys footer / tab title
    cmd: $EDITOR {dir}
  - key: g
    title: Lazygit
    cmd: lazygit

keybindings:
  killAgent: D         # remap an action's key (defaults live in the binary)

# Run every agent inside srt (anthropic-ai/sandbox-runtime). Off by default.
sandbox:
  command: srt                                  # off / none / empty disables
  settings: ~/.srt/settings.json                # empty = srt's own default
  config_settings: ~/.srt/settings-config.json  # for config sessions (default)
```

## How layers combine

| Key              | Merge                                                              |
| ---------------- | ----------------------------------------------------------------- |
| `projects`       | concatenated (entries already under `~/git` are deduplicated)      |
| `idle_timeout`   | your value wins                                                    |
| `customCommands` | merged by `key` — override, add, or remove with empty `cmd:`       |
| `keybindings`    | override individual actions                                        |
| `sandbox`        | each non-empty field of yours wins                                 |

## sandbox

With `sandbox.command` set, kmux launches each agent as
`srt [-s <settings>] -c '<agent> --continue || <agent>'`. The tmux server stays
outside the sandbox; only the agent runs inside it.

- Agents skip their own permission prompts (claude `--dangerously-skip-permissions`,
  opencode `--auto`), because srt is the security boundary. pi has no prompts.
- pi runs as bare `pi` instead of `pi-sandbox`: its podman container cannot run
  inside srt.
- A **config session** is an agent launched in `~/.config` (or below it), for
  example with `kmux --config --agent claude`. It uses `config_settings`, which
  should allow writes to `~/.config`. If that file is missing, kmux uses the
  normal `settings` instead.
- If srt is not installed, the agent session exits at once. kmux never falls back
  to an unsandboxed agent.

## customCommands

Bind a key to a shell command (run via `sh -c`, cwd = the selected row's folder).

```yaml
customCommands:
  - key: e
    panel: both        # sessions | projects | both   (default: both)
    target: tab        # tab | window | detach        (default: tab)
    title: Editor
    cmd: $EDITOR {dir}
  - key: z
    target: detach     # GUI editors that fork and return
    cmd: zed {dir}
  - key: g
    cmd: ""            # remove an inherited binding
```

`target:` — `tab` or `window` (terminal apps; both open a new tmux window),
`detach` (background, no tmux window).

`cmd:` placeholders (shell-escaped, empty when not applicable):

| Placeholder      | Expands to                                            |
| ---------------- | ----------------------------------------------------- |
| `{dir}`          | working directory of the row (also the command's cwd) |
| `{project}`      | project name (e.g. `kmux`)                            |
| `{worktree}`     | linked-worktree name, empty on the main worktree      |
| `{project_root}` | the project's main-worktree path                      |
| `{tmux_session}` | agent name (`[kmux][CC]<path>[@<worktree>]`)          |

```yaml
cmd: gh repo view --web -R {project}
```

## keybindings

Remap any action; list only what you change. The block below shows every action
with its default — uncomment a line to override it. Special key names: `enter`,
`up`, `down`, `left`, `right`, `space`, `tab`, `esc`.

```yaml
keybindings:
  prevItem:               k       # move cursor up
  nextItem:               j       # move cursor down
  prevItemAlt:            up      # move cursor up (alias)
  nextItemAlt:            down    # move cursor down (alias)
  prevPanel:              h       # cycle panel focus
  nextPanel:              l       # cycle panel focus
  prevPanelAlt:           left    # cycle panel focus (alias)
  nextPanelAlt:           right   # cycle panel focus (alias)
  createOrAttachAgent:    enter   # open/attach selection; toggle a folder header
  attachAgent:            a       # move the agent's pane into the grid
  detachAgent:            d       # detach the agent's pane (tmux keeps running)
  killAgent:              x       # kill the agent's tmux session
  fullscreenAgent:        f       # zoom the agent to full window size
  createOrFocusClaude:    c       # launch/focus Claude for the project
  createOrFocusOpencode:  o       # launch/focus OpenCode for the project
  createOrFocusPi:        p       # launch/focus Pi for the project
  launchKmuxInProject:    t       # open the project in a new kmux tab
  refreshProjects:        r       # force a full git-status rescan of all projects
  quit:                   q       # quit kmux
```

`1` (Projects), `2` (Sessions), and `ctrl+c` (quit) are fixed and can't be
rebound. `h`/`l` and the arrows **cycle panel focus**; toggle a folder with
`enter` on its header.

If a key binds to more than one action (two keybindings, or a `customCommands`
key colliding with a navigation key), the Keys panel lists the conflict; the
first action in the table above wins.
