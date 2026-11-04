#!/bin/bash
set -euo pipefail

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir "$work/bin"

export PATH="$work/bin:$PATH"
export XENO_STATE_DIR="$work/state"
export XENO_TEST_LOG="$work/monitor.json"

cat >"$work/bin/pass-cli" <<'PASS'
#!/bin/sh
set -eu
case "$*" in
    login)
        test "$PROTON_PASS_PERSONAL_ACCESS_TOKEN" = test-token
        mkdir -p "$PROTON_PASS_SESSION_DIR"
        touch "$PROTON_PASS_SESSION_DIR/active"
        ;;
    'agent monitor --output json')
        test -f "$PROTON_PASS_SESSION_DIR/active"
        cat "$XENO_TEST_LOG"
        ;;
    *) exit 2 ;;
esac
PASS
chmod +x "$work/bin/pass-cli"

# Startup is a no-op without a configured PAT.
unset PROTON_PASS_PERSONAL_ACCESS_TOKEN
xeno init
test ! -e "$XENO_STATE_DIR"

# A PAT starts an isolated session and an empty audit log is valid evidence.
export PROTON_PASS_PERSONAL_ACCESS_TOKEN=test-token
printf '%s\n' '[]' >"$XENO_TEST_LOG"
xeno init
jq -e '. == []' "$XENO_STATE_DIR/reasons.json"

# A manual refresh preserves the monitor's evidence and replaces the snapshot.
printf '%s\n' '[{"record_id":"new","payload":{"reason":"check rerun"}}]' >"$XENO_TEST_LOG"
xeno refresh
cmp "$XENO_TEST_LOG" "$XENO_STATE_DIR/reasons.json"
cp "$XENO_STATE_DIR/reasons.json" "$work/last-good.json"

# A malformed response fails a manual refresh without destroying that snapshot.
printf '%s\n' 'invalid JSON' >"$XENO_TEST_LOG"
if xeno refresh; then
    echo "refresh accepted invalid monitor data" >&2
    exit 1
fi
cmp "$work/last-good.json" "$XENO_STATE_DIR/reasons.json"
