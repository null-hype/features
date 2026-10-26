#!/bin/sh
set -e

echo "Activating feature 'witness'"

VERSION="${VERSION:-v0.1.0}"
NVIM_VERSION="v0.11.4"
SHARE=/usr/local/share/witness

if ! command -v git >/dev/null 2>&1 || ! command -v curl >/dev/null 2>&1; then
    apt-get update -y && apt-get install -y --no-install-recommends git curl ca-certificates
fi

# One checkout gives the server, its Neovim client and the example run, all at
# the same version. Fetching a single ref works for a tag, a branch or a commit.
src="$(mktemp -d)"
git -C "$src" init -q
git -C "$src" fetch -q --depth 1 https://github.com/null-hype/witness.git "$VERSION"
git -C "$src" checkout -q FETCH_HEAD

GO="$(command -v go || echo /usr/local/go/bin/go)"
(cd "$src" && CGO_ENABLED=0 "$GO" build -o /usr/local/bin/witness .)

mkdir -p "$SHARE"
cp -R "$src/nvim" "$src/example" "$SHARE/"
rm -rf "$src"

# The client uses vim.lsp.config, so it needs Neovim 0.11 or later.
if ! command -v nvim >/dev/null 2>&1; then
    case "$(uname -m)" in
        x86_64)        NVIM_ARCH=x86_64 ;;
        aarch64|arm64) NVIM_ARCH=arm64 ;;
        *) echo "unsupported architecture $(uname -m) for Neovim" >&2; exit 1 ;;
    esac
    curl -fsSL "https://github.com/neovim/neovim/releases/download/${NVIM_VERSION}/nvim-linux-${NVIM_ARCH}.tar.gz" \
        | tar xz -C /opt
    ln -sf "/opt/nvim-linux-${NVIM_ARCH}/bin/nvim" /usr/local/bin/nvim
fi

# install.sh runs as root; the editor runs as the remote user, so the client
# goes in that user's Neovim config.
TARGET_HOME="${_REMOTE_USER_HOME:-$HOME}"
mkdir -p "$TARGET_HOME/.config/nvim/plugin"
echo "dofile('$SHARE/nvim/witness.lua')" > "$TARGET_HOME/.config/nvim/plugin/witness.lua"
if [ -n "$_REMOTE_USER" ] && [ "$_REMOTE_USER" != root ]; then
    chown -R "$_REMOTE_USER" "$TARGET_HOME/.config"
fi
