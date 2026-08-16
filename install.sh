#!/usr/bin/env sh
# kmux installer — builds from source and installs the binary.
# Supports macOS and Linux. POSIX sh compatible.
set -eu

BIN_NAME="kmux"
REPO_URL="https://github.com/olli-io/kmux"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
info()  { printf '\033[1;34m==>\033[0m %s\n' "$1"; }
warn()  { printf '\033[1;33mwarning:\033[0m %s\n' "$1" >&2; }
die()   { printf '\033[1;31merror:\033[0m %s\n' "$1" >&2; exit 1; }
have()  { command -v "$1" >/dev/null 2>&1; }

# ---------------------------------------------------------------------------
# Detect platform (informational; Go handles the actual cross details)
# ---------------------------------------------------------------------------
OS="$(uname -s)"
case "$OS" in
  Darwin) PLATFORM="macOS" ;;
  Linux)  PLATFORM="Linux" ;;
  *)      die "unsupported OS: $OS (only macOS and Linux are supported)" ;;
esac
info "Detected platform: $PLATFORM ($(uname -m))"

# ---------------------------------------------------------------------------
# Check prerequisites
# ---------------------------------------------------------------------------
have go || die "Go is not installed. Install it from https://go.dev/dl/ (1.21+ required)."
info "Using $(go version)"

have tmux   || warn "tmux not found — kmux needs it at runtime."
have kitty  || warn "kitty not found — kmux must run inside a kitty window."
have kitten || warn "kitten not found — kmux drives kitty via 'kitten @'; ensure kitty's bin dir is on PATH."

# ---------------------------------------------------------------------------
# Locate the source.
# If run from a checkout (go.mod present) build that; otherwise build via
# 'go install' against the public module.
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"

# ---------------------------------------------------------------------------
# Choose an install directory.
# Defaults to ~/.local/bin so no sudo is needed.
# Override with PREFIX or INSTALL_DIR env vars.
# ---------------------------------------------------------------------------
if [ -n "${INSTALL_DIR:-}" ]; then
  DEST="$INSTALL_DIR"
elif [ -n "${PREFIX:-}" ]; then
  DEST="$PREFIX/bin"
else
  DEST="${XDG_BIN_HOME:-$HOME/.local/bin}"
fi
mkdir -p "$DEST" 2>/dev/null || true

# Detect whether we need sudo to write to DEST.
SUDO=""
if [ ! -w "$DEST" ]; then
  if have sudo; then
    SUDO="sudo"
  else
    die "cannot write to $DEST and sudo is unavailable. Set INSTALL_DIR to a writable path."
  fi
fi

# ---------------------------------------------------------------------------
# Build & install
# ---------------------------------------------------------------------------
# kmux-idler is the lightweight launcher kmux runs in its idle panes. It installs
# beside kmux ($DEST/kmux-idler) where kmux discovers it relative to its own path;
# if it's absent kmux just falls back to inert idle slots.
IDLER_NAME="kmux-idler"

if [ -f "$SCRIPT_DIR/go.mod" ]; then
  info "Building $BIN_NAME from source in $SCRIPT_DIR ..."
  TMP_DIR="$(mktemp -d)"
  ( cd "$SCRIPT_DIR" && go build -trimpath -ldflags "-s -w" -o "$TMP_DIR/$BIN_NAME" ./cmd/kmux )
  info "Building $IDLER_NAME ..."
  ( cd "$SCRIPT_DIR" && go build -trimpath -ldflags "-s -w" -o "$TMP_DIR/$IDLER_NAME" ./cmd/kmux-idler )
  info "Installing to $DEST/$BIN_NAME and $DEST/$IDLER_NAME ..."
  $SUDO install -m 0755 "$TMP_DIR/$BIN_NAME" "$DEST/$BIN_NAME"
  $SUDO install -m 0755 "$TMP_DIR/$IDLER_NAME" "$DEST/$IDLER_NAME"
  rm -rf "$TMP_DIR"
  CONFIG_SRC="$SCRIPT_DIR/scripts/config.yaml"
else
  info "No local checkout found; fetching via 'go install' from $REPO_URL ..."
  TMP_GOBIN="$(mktemp -d)"
  GOBIN="$TMP_GOBIN" GOFLAGS="-trimpath" go install "${REPO_URL#https://}/cmd/kmux@latest" \
    || die "go install failed. Clone the repo and re-run ./install.sh from inside it."
  GOBIN="$TMP_GOBIN" GOFLAGS="-trimpath" go install "${REPO_URL#https://}/cmd/kmux-idler@latest" \
    || warn "could not build $IDLER_NAME; idle panes will fall back to inert slots."
  info "Installing to $DEST/$BIN_NAME ..."
  $SUDO install -m 0755 "$TMP_GOBIN/$BIN_NAME" "$DEST/$BIN_NAME"
  if [ -f "$TMP_GOBIN/$IDLER_NAME" ]; then
    info "Installing to $DEST/$IDLER_NAME ..."
    $SUDO install -m 0755 "$TMP_GOBIN/$IDLER_NAME" "$DEST/$IDLER_NAME"
  fi
  rm -rf "$TMP_GOBIN"
  CONFIG_SRC=""
fi

# ---------------------------------------------------------------------------
# Install the default config next to the binary as $DEST/config.yaml. kmux reads
# it as the base layer for command keybindings (editor, lazygit) and overlays
# the user's ~/.config/kmux/config.yaml on top. Only available from a checkout.
# ---------------------------------------------------------------------------
if [ -n "$CONFIG_SRC" ] && [ -f "$CONFIG_SRC" ]; then
  info "Installing default config to $DEST/config.yaml ..."
  $SUDO install -m 0644 "$CONFIG_SRC" "$DEST/config.yaml"
else
  warn "default config.yaml not installed (no local scripts/); editor/lazygit bindings need $DEST/config.yaml."
fi

# ---------------------------------------------------------------------------
# Deprecate the agent attention hooks.
# kmux now detects which sessions need you (the Sessions-panel glyph and the kitty
# tab / tmux window "[!!]" marker) purely from each session's pane text — no agent-
# side hooks or plugins are required. Earlier versions installed a Claude Code hook
# (in ~/.claude/settings.json) and an OpenCode plugin that invoked `kmux --attn`;
# that flag no longer exists. This section REMOVES any previously-installed hook /
# plugin idempotently so upgrades clean themselves up.
# ---------------------------------------------------------------------------

# --- Claude Code: strip any `kmux --attn` hook from ~/.claude/settings.json ------
CLAUDE_SETTINGS="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/settings.json"
remove_claude_hooks() {
  [ -f "$CLAUDE_SETTINGS" ] || return 0
  if ! have jq; then
    if grep -q -- '--attn' "$CLAUDE_SETTINGS" 2>/dev/null; then
      warn "jq not found — please remove the kmux '--attn' hooks from $CLAUDE_SETTINGS manually (Notification/UserPromptSubmit/Stop)."
    fi
    return 0
  fi
  # Nothing to do if no --attn hook is present (idempotent re-runs).
  if ! jq -e '[.hooks // {} | .. | .command? // empty] | any(contains("--attn"))' \
        "$CLAUDE_SETTINGS" >/dev/null 2>&1; then
    return 0
  fi
  cp "$CLAUDE_SETTINGS" "$CLAUDE_SETTINGS.kmux-bak" 2>/dev/null || true
  TMP_JSON="$(mktemp)"
  # Drop hook entries whose command contains "--attn" from the three event arrays,
  # then delete any arrays/keys left empty, and remove .hooks if it ends up empty.
  if jq '
        def strip: map(select((.hooks // [] | map(.command // "" | contains("--attn")) | any) | not));
        if .hooks then
          .hooks.Notification    = ((.hooks.Notification    // []) | strip)
          | .hooks.UserPromptSubmit = ((.hooks.UserPromptSubmit // []) | strip)
          | .hooks.Stop          = ((.hooks.Stop          // []) | strip)
          | .hooks |= with_entries(select(.value | length > 0))
          | if (.hooks | length) == 0 then del(.hooks) else . end
        else . end
      ' "$CLAUDE_SETTINGS" > "$TMP_JSON" 2>/dev/null && [ -s "$TMP_JSON" ]; then
    mv "$TMP_JSON" "$CLAUDE_SETTINGS"
    info "Removed kmux attention hooks from $CLAUDE_SETTINGS (backup: $CLAUDE_SETTINGS.kmux-bak)."
  else
    rm -f "$TMP_JSON"
    warn "could not update $CLAUDE_SETTINGS with jq; left it unchanged."
  fi
}
remove_claude_hooks

# --- OpenCode: remove the kmux plugin --------------------------------------------
OPENCODE_PLUGIN="${XDG_CONFIG_HOME:-$HOME/.config}/opencode/plugin/kmux-attn.js"
remove_opencode_plugin() {
  # Only remove our own file (identified by its install header), never a user file
  # that happens to share the name.
  if [ -f "$OPENCODE_PLUGIN" ] && grep -q "Installed by kmux" "$OPENCODE_PLUGIN" 2>/dev/null; then
    rm -f "$OPENCODE_PLUGIN"
    info "Removed OpenCode attention plugin $OPENCODE_PLUGIN."
  fi
}
remove_opencode_plugin

# --- Drop the now-unused attention marker directory ------------------------------
ATTN_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/kmux/attention"
[ -d "$ATTN_DIR" ] && rm -rf "$ATTN_DIR" && info "Removed stale attention markers ($ATTN_DIR)."

info "Attention is now detected from pane text — no agent hooks or plugins needed."

# ---------------------------------------------------------------------------
# PATH check
# ---------------------------------------------------------------------------
case ":$PATH:" in
  *":$DEST:"*) : ;;
  *)
    warn "$DEST is not on your PATH."
    printf '  Add it with:\n    export PATH="%s:$PATH"\n' "$DEST"
    ;;
esac

info "Done. Installed $BIN_NAME to $DEST/$BIN_NAME"
info "Run it inside a kitty window with remote control enabled (see README)."
