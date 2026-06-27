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
  killAgent: x         # remap an action's key (defaults live in the binary)
```

## How layers combine

| Key              | Merge                                                              |
| ---------------- | ----------------------------------------------------------------- |
| `projects`       | concatenated (entries already under `~/git` are deduplicated)      |
| `idle_timeout`   | your value wins                                                    |
| `customCommands` | merged by `key` — override, add, or remove with empty `cmd:`       |
| `keybindings`    | override individual actions                                        |

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

`target:` — `tab` (terminal apps), `window` (separate kitty instance),
`detach` (background, no kitty surface).

`cmd:` placeholders (shell-escaped, empty when not applicable):

| Placeholder      | Expands to                                            |
| ---------------- | ----------------------------------------------------- |
| `{dir}`          | working directory of the row (also the command's cwd) |
| `{project}`      | project name (e.g. `kmux`)                            |
| `{worktree}`     | linked-worktree name, empty on the main worktree      |
| `{project_root}` | the project's main-worktree path                      |
| `{tmux_session}` | agent session name (`<project>[/<worktree>]~cl\|~oc`) |

```yaml
cmd: tmux attach -t {tmux_session}
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
  detachAgent:            d       # detach the agent's pane (tmux keeps running)
  killAgent:              D       # kill the agent's tmux session
  fullscreenAgent:        f       # open the agent in its own kitty tab
  createOrFocusClaude:    c       # launch/focus Claude for the project
  createOrFocusOpencode:  o       # launch/focus OpenCode for the project
  launchKmuxInProject:    t       # open the project in a new kmux tab
  quit:                   q       # quit kmux
```

`1` (Projects), `2` (Sessions), and `ctrl+c` (quit) are fixed and can't be
rebound. `h`/`l` and the arrows **cycle panel focus**; toggle a folder with
`enter` on its header.

If a key binds to more than one action (two keybindings, or a `customCommands`
key colliding with a navigation key), the Keys panel lists the conflict; the
first action in the table above wins.
