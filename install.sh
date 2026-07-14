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
# Wire the agent attention hooks.
# kmux shows which sessions need you (the Sessions-panel glyph and the kitty tab
# title's [!!] marker) from each agent's own lifecycle events, not by scraping the
# pane. Claude Code delivers these via JSON hooks in ~/.claude/settings.json;
# OpenCode via a plugin in ~/.config/opencode/plugin/. Both invoke `kmux --attn`,
# which records the state under ~/.config/kmux/attention/. Installed idempotently,
# using the binary's absolute path (hooks/plugins get no login-shell PATH).
# ---------------------------------------------------------------------------
# Absolute path to the installed binary for hook/plugin commands.
case "$DEST" in
  /*) KMUX_BIN="$DEST/$BIN_NAME" ;;
  *)  KMUX_BIN="$(CDPATH= cd -- "$DEST" && pwd)/$BIN_NAME" ;;
esac

# --- Claude Code: merge hooks into ~/.claude/settings.json (needs jq) -----------
CLAUDE_SETTINGS="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/settings.json"
install_claude_hooks() {
  if ! have jq; then
    warn "jq not found — skipping automatic Claude Code hook install."
    printf '  To enable the kmux attention marker in Claude Code, add these hooks to\n  %s (command: "%s --attn"):\n' "$CLAUDE_SETTINGS" "$KMUX_BIN"
    printf '    Notification (matcher "permission_prompt|idle_prompt"), UserPromptSubmit, Stop\n'
    return
  fi
  mkdir -p "$(dirname "$CLAUDE_SETTINGS")" 2>/dev/null || true
  [ -f "$CLAUDE_SETTINGS" ] || printf '{}\n' > "$CLAUDE_SETTINGS"

  # Skip if a kmux --attn hook is already wired (idempotent re-runs): true when any
  # hook command string contains "--attn".
  if jq -e '[.hooks // {} | .. | .command? // empty] | any(contains("--attn"))' \
        "$CLAUDE_SETTINGS" >/dev/null 2>&1; then
    info "Claude Code attention hooks already present in $CLAUDE_SETTINGS."
    return
  fi

  cp "$CLAUDE_SETTINGS" "$CLAUDE_SETTINGS.kmux-bak" 2>/dev/null || true
  TMP_JSON="$(mktemp)"
  if jq --arg cmd "$KMUX_BIN --attn" '
        .hooks = (.hooks // {})
        | .hooks.Notification = ((.hooks.Notification // []) + [{"matcher":"permission_prompt|idle_prompt","hooks":[{"type":"command","command":$cmd}]}])
        | .hooks.UserPromptSubmit = ((.hooks.UserPromptSubmit // []) + [{"hooks":[{"type":"command","command":$cmd}]}])
        | .hooks.Stop = ((.hooks.Stop // []) + [{"hooks":[{"type":"command","command":$cmd}]}])
      ' "$CLAUDE_SETTINGS" > "$TMP_JSON" 2>/dev/null && [ -s "$TMP_JSON" ]; then
    mv "$TMP_JSON" "$CLAUDE_SETTINGS"
    info "Installed Claude Code attention hooks into $CLAUDE_SETTINGS (backup: $CLAUDE_SETTINGS.kmux-bak)."
  else
    rm -f "$TMP_JSON"
    warn "could not update $CLAUDE_SETTINGS with jq; left it unchanged."
  fi
}
install_claude_hooks

# --- OpenCode: drop a plugin into ~/.config/opencode/plugin/ ---------------------
OPENCODE_PLUGIN_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/opencode/plugin"
OPENCODE_PLUGIN="$OPENCODE_PLUGIN_DIR/kmux-attn.js"
install_opencode_plugin() {
  mkdir -p "$OPENCODE_PLUGIN_DIR" 2>/dev/null || true
  # The plugin forwards permission/idle events to `kmux --attn`. directory is the
  # plugin's project cwd; kmux maps it to the [kmux][OC] session name.
  cat > "$OPENCODE_PLUGIN.tmp" <<EOF
// Installed by kmux (install.sh). Forwards OpenCode lifecycle events to kmux so the
// dashboard can show which sessions need attention. Safe to delete to opt out.
export const KmuxAttn = async ({ \$, directory }) => ({
  event: async ({ event }) => {
    const t = event.type
    if (t === "permission.updated" || t === "permission.replied" || t === "session.idle") {
      await \$\`$KMUX_BIN --attn --kind opencode --event \${t} --cwd \${directory}\`.quiet().nothrow()
    }
  },
})
EOF
  if [ -f "$OPENCODE_PLUGIN" ] && cmp -s "$OPENCODE_PLUGIN.tmp" "$OPENCODE_PLUGIN"; then
    rm -f "$OPENCODE_PLUGIN.tmp"
    info "OpenCode attention plugin already up to date at $OPENCODE_PLUGIN."
  else
    mv "$OPENCODE_PLUGIN.tmp" "$OPENCODE_PLUGIN"
    info "Installed OpenCode attention plugin to $OPENCODE_PLUGIN."
  fi
}
install_opencode_plugin

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
