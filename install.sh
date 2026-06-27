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

have tmux  || warn "tmux not found — kmux needs it at runtime."
have kitty || warn "kitty not found — kmux must run inside a kitty window."

# ---------------------------------------------------------------------------
# Locate the source.
# If run from a checkout (go.mod present) build that; otherwise build via
# 'go install' against the public module.
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"

# ---------------------------------------------------------------------------
# Choose an install directory.
# Defaults to /usr/local/bin (via sudo if needed).
# Override with PREFIX or INSTALL_DIR env vars.
# ---------------------------------------------------------------------------
if [ -n "${INSTALL_DIR:-}" ]; then
  DEST="$INSTALL_DIR"
elif [ -n "${PREFIX:-}" ]; then
  DEST="$PREFIX/bin"
else
  DEST="/usr/local/bin"
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
if [ -f "$SCRIPT_DIR/go.mod" ]; then
  info "Building $BIN_NAME from source in $SCRIPT_DIR ..."
  TMP_BIN="$(mktemp -d)/$BIN_NAME"
  ( cd "$SCRIPT_DIR" && go build -trimpath -ldflags "-s -w" -o "$TMP_BIN" ./cmd/kmux )
  info "Installing to $DEST/$BIN_NAME ..."
  $SUDO install -m 0755 "$TMP_BIN" "$DEST/$BIN_NAME"
  rm -f "$TMP_BIN"
  CONFIG_SRC="$SCRIPT_DIR/scripts/config.yaml"
else
  info "No local checkout found; fetching via 'go install' from $REPO_URL ..."
  TMP_GOBIN="$(mktemp -d)"
  GOBIN="$TMP_GOBIN" GOFLAGS="-trimpath" go install "${REPO_URL#https://}/cmd/kmux@latest" \
    || die "go install failed. Clone the repo and re-run ./install.sh from inside it."
  info "Installing to $DEST/$BIN_NAME ..."
  $SUDO install -m 0755 "$TMP_GOBIN/$BIN_NAME" "$DEST/$BIN_NAME"
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
