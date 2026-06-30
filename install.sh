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
