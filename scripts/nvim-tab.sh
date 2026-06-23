#!/bin/bash
# nvim-tab.sh — open / focus nvim in a tab of the existing kitty nvim window.
#
# nvim sets its terminal title to 'nvim:<cwd-basename>' (see nvim/init.lua), so
# the kitty window hosting nvim always has a window titled 'nvim:...'. Wire this
# into the leader-key 'n' binding.
#
# Usage:
#   nvim-tab.sh [--focus] [--exclude-window <id>] [<dir>]
#     (no <dir>)            open a fresh nvim ($HOME) in a new tab
#     <dir>                 if an nvim session for <dir> exists, focus that tab;
#                           otherwise open a new nvim in <dir>
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

set -euo pipefail

KITTEN=/Applications/kitty.app/Contents/MacOS/kitten
NVIM=/opt/homebrew/bin/nvim
AEROSPACE=/opt/homebrew/bin/aerospace

# Parse options, then the optional target directory.
FOCUS_ONLY=""
EXCLUDE_WINDOW=""
while [ $# -gt 0 ]; do
  case "$1" in
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
# set, prints "<platform-window-id> <kitty-window-id>" for the window with that
# exact title; otherwise prints "<platform-window-id>" for the first nvim window.
# The platform window id doubles as the aerospace window id (CGWindowID).
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
                    print(osw.get("platform_window_id"), w.get("id"))
                    sys.exit(0)
            else:
                print(osw.get("platform_window_id"))
                sys.exit(0)
PY

# scan_nvim [match-title]
#   Each `open -na kitty` is a separate instance with its own remote-control
#   socket (listen_on unix:/tmp/mykitty-{pid}). Scans them all and, on the first
#   instance with a matching nvim window, prints "<socket> <ids...>".
scan_nvim() {
  local match="${1:-}" s out res
  for s in /tmp/mykitty-*; do
    [ -S "$s" ] || continue
    out="$("$KITTEN" @ --to "unix:$s" ls 2>/dev/null)" || continue
    res="$(KJSON="$out" MATCH_TITLE="$match" EXCLUDE_WINDOW="$EXCLUDE_WINDOW" python3 -c "$FIND_NVIM" 2>/dev/null)" || continue
    if [ -n "$res" ]; then printf '%s %s\n' "unix:$s" "$res"; return 0; fi
  done
  return 0
}

# With a target dir: if that session already exists, focus its exact tab.
if [ -n "$TARGET_CWD" ]; then
  line="$(scan_nvim "nvim:$(basename "$TARGET_CWD")")"
  if [ -n "$line" ]; then
    read -r sock winid kid <<<"$line"
    "$AEROSPACE" focus --window-id "$winid" >/dev/null 2>&1 || true
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
# shows — adding $HOME as a path would open the home dir instead. The
# ${arr[@]+...} guard keeps the empty-array expansion safe under `set -u` on
# bash 3.2 (macOS).
NVIM_ARGS=()
if [ -n "$TARGET_CWD" ]; then
  NVIM_ARGS=("$TARGET_CWD")
fi

# Add it as a tab to the existing nvim window if there is one.
line="$(scan_nvim)"
if [ -n "$line" ]; then
  read -r sock winid <<<"$line"
  "$AEROSPACE" focus --window-id "$winid" >/dev/null 2>&1 || true
  exec "$KITTEN" @ --to "$sock" launch --type=tab --cwd "$launch_cwd" "$NVIM" "${NVIM_ARGS[@]+"${NVIM_ARGS[@]}"}"
fi

# No nvim window yet — open a fresh kitty running nvim in launch_cwd.
exec /usr/bin/open -na kitty --args -d "$launch_cwd" -e "$NVIM" "${NVIM_ARGS[@]+"${NVIM_ARGS[@]}"}"
