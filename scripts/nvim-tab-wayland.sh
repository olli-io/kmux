#!/bin/bash
# nvim-tab-wayland.sh — open / focus nvim in a tab of the existing kitty nvim
# window. Hyprland / Wayland counterpart of nvim-tab.sh (which targets macOS +
# aerospace).
#
# nvim sets its terminal title to 'nvim:<cwd-basename>' (see nvim/init.lua), so
# the kitty window hosting nvim always has a window titled 'nvim:...'. Wire this
# into the leader-key 'n' binding.
#
# Usage:
#   nvim-tab-wayland.sh [--className <name>] [--focus] [--exclude-window <id>] [<dir>]
#     <dir>                 if an nvim session for <dir> exists, focus that tab;
#                           otherwise open a new nvim in <dir>. With no <dir>,
#                           open a fresh nvim ($HOME) in a new tab.
#     --className <name>    wm class (app_id) for a freshly launched kitty window
#                           so Hyprland window rules can target it (default: nvim).
#     --focus              only focus an existing session for <dir>; never launch.
#                           Exits 0 if a tab was focused, 1 if no session exists.
#                           Lets a caller (e.g. nvim's dashboard) fall back to
#                           opening the project in place when it isn't open yet.
#     --exclude-window <id> skip the kitty window with this id when matching, so a
#                           caller can avoid focusing its own window (the nvim that
#                           launched this script).
#
# In all cases it raises the kitty window hosting nvim, or launches a new kitty
# running nvim if none exists yet.
#
# Sessions are matched by the visible title ('nvim:<basename>') rather than the
# window's reported cwd: nvim session-restore uses :lcd/:tcd, which updates the
# displayed dir (getcwd) but not the process cwd kitty reports. Caveat: two dirs
# sharing a basename are indistinguishable, and the first match wins.
#
# Platform notes vs the macOS script:
#   - kitty listens on the abstract socket '@kitty-<pid>' (kitty.conf:
#     'listen_on unix:@kitty'), enumerated from /proc/net/unix rather than a
#     /tmp glob. The trailing pid is the kitty process pid.
#   - On Wayland 'platform_window_id' is null, so raising the OS window goes
#     through Hyprland: 'hyprctl dispatch focuswindow pid:<pid>'. Hyprland
#     reports the kitty process pid as the client pid, which lets us map a
#     kitty instance to its window without the platform id.

set -euo pipefail

KITTEN="$(command -v kitten)"
NVIM="$(command -v nvim)"
KITTY="$(command -v kitty)"
HYPRCTL="$(command -v hyprctl || true)"

# Parse options, then the optional target directory.
CLASS_NAME="nvim"
FOCUS_ONLY=""
EXCLUDE_WINDOW=""
while [ $# -gt 0 ]; do
  case "$1" in
    --className)
      [ -n "${2:-}" ] || { echo "nvim-tab: --className needs a value" >&2; exit 1; }
      CLASS_NAME="$2"; shift 2 ;;
    --className=*) CLASS_NAME="${1#*=}"; shift ;;
    --focus) FOCUS_ONLY=1; shift ;;
    --exclude-window)
      [ -n "${2:-}" ] || { echo "nvim-tab: --exclude-window needs a value" >&2; exit 1; }
      EXCLUDE_WINDOW="$2"; shift 2 ;;
    --exclude-window=*) EXCLUDE_WINDOW="${1#*=}"; shift ;;
    --) shift; break ;;
    -*) echo "nvim-tab: unknown option: $1" >&2; exit 1 ;;
    *) break ;;
  esac
done

TARGET_CWD=""
if [ -n "${1:-}" ]; then
  TARGET_CWD="$(cd "$1" 2>/dev/null && pwd -P)" \
    || { echo "nvim-tab: no such directory: $1" >&2; exit 1; }
fi

[ -n "$FOCUS_ONLY" ] && [ -z "$TARGET_CWD" ] \
  && { echo "nvim-tab: --focus requires a directory" >&2; exit 1; }

# Scans a `kitten @ ls` JSON blob ($KJSON) for nvim windows. If $MATCH_TITLE is
# set, prints the kitty window id of the window with that exact title; otherwise
# prints the kitty window id of the first nvim window found.
read -r -d '' FIND_NVIM <<'PY' || true
import json, os, sys
data = json.loads(os.environ["KJSON"])
want = os.environ.get("MATCH_TITLE", "")
exclude = os.environ.get("EXCLUDE_WINDOW", "")
for osw in data:
    for tab in osw.get("tabs", []):
        for w in tab.get("windows", []):
            if exclude and str(w.get("id")) == exclude:
                continue
            title = w.get("title") or ""
            if not title.startswith("nvim:"):
                continue
            if want:
                if title == want:
                    print(w.get("id"))
                    sys.exit(0)
            else:
                print(w.get("id"))
                sys.exit(0)
PY

# Abstract sockets can't be globbed on the filesystem, so enumerate the kitty
# remote-control sockets ('@kitty-<pid>') from /proc/net/unix.
list_kitty_sockets() {
  awk '{print $NF}' /proc/net/unix 2>/dev/null \
    | grep -E '^@kitty(-[0-9]+)?$' | sort -u || true
}

# Raise the kitty OS window owned by $1 (a pid) via Hyprland. Best-effort.
focus_hypr() {
  [ -n "${1:-}" ] && [ -n "$HYPRCTL" ] || return 0
  "$HYPRCTL" dispatch focuswindow "pid:$1" >/dev/null 2>&1 || true
}

# scan_nvim [match-title]
#   Each kitty instance has its own remote-control socket (@kitty-<pid>). Scans
#   them all and, on the first instance with a matching nvim window, prints
#   "<socket> <pid> <kitty-window-id>".
scan_nvim() {
  local match="${1:-}" name pid out res
  for name in $(list_kitty_sockets); do
    pid="${name#@kitty-}"
    out="$("$KITTEN" @ --to "unix:$name" ls 2>/dev/null)" || continue
    res="$(KJSON="$out" MATCH_TITLE="$match" EXCLUDE_WINDOW="$EXCLUDE_WINDOW" python3 -c "$FIND_NVIM" 2>/dev/null)" || continue
    if [ -n "$res" ]; then printf '%s %s %s\n' "unix:$name" "$pid" "$res"; return 0; fi
  done
  return 0
}

# With a target dir: if that session already exists, focus its exact tab.
if [ -n "$TARGET_CWD" ]; then
  line="$(scan_nvim "nvim:$(basename "$TARGET_CWD")")"
  if [ -n "$line" ]; then
    read -r sock pid kid <<<"$line"
    focus_hypr "$pid"
    exec "$KITTEN" @ --to "$sock" focus-window --match "id:$kid"
  fi
  # --focus only ever focuses an existing tab; signal "not open" so the caller
  # can fall back to opening the project itself.
  [ -n "$FOCUS_ONLY" ] && exit 1
fi

# Otherwise open a fresh nvim (in $TARGET_CWD if given, else $HOME).
launch_cwd="${TARGET_CWD:-$HOME}"

# Pass the directory to nvim as a path argument only when one was given: with a
# target dir (kmux's editor binding) `nvim <dir>` opens it directly, bypassing
# the start screen. With no arg (leader-n) run a plain `nvim` so the dashboard
# shows — adding $HOME as a path would open the home dir instead.
NVIM_ARGS=()
if [ -n "$TARGET_CWD" ]; then
  NVIM_ARGS=("$TARGET_CWD")
fi

# Add it as a tab to the existing nvim window if there is one.
line="$(scan_nvim)"
if [ -n "$line" ]; then
  read -r sock pid kid <<<"$line"
  focus_hypr "$pid"
  exec "$KITTEN" @ --to "$sock" launch --type=tab --cwd "$launch_cwd" "$NVIM" "${NVIM_ARGS[@]+"${NVIM_ARGS[@]}"}"
fi

# No nvim window yet — open a fresh kitty running nvim in launch_cwd. Detach with
# setsid so this script (run synchronously by kmux's OpenEditor) returns at once
# instead of blocking until nvim exits.
exec setsid -f "$KITTY" --class "$CLASS_NAME" --directory "$launch_cwd" -e "$NVIM" "${NVIM_ARGS[@]+"${NVIM_ARGS[@]}"}"
