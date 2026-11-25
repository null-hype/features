#!/bin/sh
set -e

echo "Activating feature 'delta-zero'"

PKL_VERSION="${PKLVERSION:-0.28.2}"

# SHA-256 of the pkl-linux-* release binaries, from the apple/pkl release.
case "$PKL_VERSION" in
    0.28.2)
        PKL_SHA256_AMD64="ba8722ba6971f7cb2b8b90fdca76c947a9510a57119baf05263bf4fcf9f9b0ba"
        PKL_SHA256_AARCH64="ed04d0175fd3ceaddcb5c35af657de6a96cd8018cfca9f7d14e738c98b4782bf"
        ;;
    *) echo "no pinned checksum for Pkl $PKL_VERSION" >&2; exit 1 ;;
esac

case "$(uname -m)" in
    x86_64)        PKL_ARCH=amd64;   PKL_SHA256="$PKL_SHA256_AMD64" ;;
    aarch64|arm64) PKL_ARCH=aarch64; PKL_SHA256="$PKL_SHA256_AARCH64" ;;
    *) echo "unsupported architecture $(uname -m) for Pkl" >&2; exit 1 ;;
esac

if ! command -v curl >/dev/null 2>&1 || ! command -v python3 >/dev/null 2>&1 || ! command -v sha256sum >/dev/null 2>&1; then
    apt-get update -y
    apt-get install -y --no-install-recommends curl ca-certificates python3 coreutils
    rm -rf /var/lib/apt/lists/*
fi

tmp="$(mktemp -d)"
curl -fsSL -o "$tmp/pkl" "https://github.com/apple/pkl/releases/download/${PKL_VERSION}/pkl-linux-${PKL_ARCH}"
echo "${PKL_SHA256}  $tmp/pkl" | sha256sum -c -
install -m 0755 "$tmp/pkl" /usr/local/bin/pkl
rm -rf "$tmp"
