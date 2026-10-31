#!/bin/bash
set -e

if ! command -v herdr >/dev/null; then
    case $(uname -m) in
        x86_64 | amd64) arch=x86_64 ;;
        aarch64 | arm64) arch=aarch64 ;;
        *) echo "xeno: no Herdr build for $(uname -m)" >&2; exit 1 ;;
    esac
    if ! command -v curl >/dev/null; then
        apt-get update -y && apt-get install -y --no-install-recommends curl ca-certificates
    fi
    curl -fsSL -o /usr/local/bin/herdr \
        "https://github.com/herdrdev/herdr/releases/latest/download/herdr-linux-$arch"
    chmod +x /usr/local/bin/herdr
fi

mkdir -p /usr/local/share/xeno
cp -r "$(dirname "$0")/plugin" "$(dirname "$0")/example" /usr/local/share/xeno/
chmod +x /usr/local/share/xeno/plugin/xeno
ln -sf /usr/local/share/xeno/plugin/xeno /usr/local/bin/xeno

user=${_REMOTE_USER:-root}
su "$user" -c "herdr plugin link /usr/local/share/xeno/plugin"
