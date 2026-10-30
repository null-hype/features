#!/bin/sh
set -e

echo "Activating feature 'xeno'"

SHARE=/usr/local/share/xeno

if ! command -v curl >/dev/null 2>&1; then
    apt-get update -y && apt-get install -y --no-install-recommends curl ca-certificates
fi

if ! command -v herdr >/dev/null 2>&1; then
    curl -fsSL https://herdr.dev/install.sh | HERDR_INSTALL_DIR=/usr/local/bin sh
fi

# The plugin is this directory. `herdr plugin link` does not run the
# manifest's build commands, so build it here.
mkdir -p "$SHARE"
cp herdr-plugin.toml go.mod main.go "$SHARE/"
GO="$(command -v go || echo /usr/local/go/bin/go)"
(cd "$SHARE" && CGO_ENABLED=0 "$GO" build -o xeno .)

# install.sh runs as root; Herdr runs as the remote user and keeps its plugin
# registry in that user's config, so link as them.
TARGET_USER="${_REMOTE_USER:-root}"
if [ "$TARGET_USER" = root ]; then
    herdr plugin link "$SHARE"
else
    su "$TARGET_USER" -c "herdr plugin link '$SHARE'"
fi
