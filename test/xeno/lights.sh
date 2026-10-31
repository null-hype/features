#!/bin/bash
set -e

source dev-container-features-test-lib

herdr server >/tmp/herdr-server.log 2>&1 &
for _ in $(seq 50); do herdr workspace list >/dev/null 2>&1 && break; sleep 0.2; done
export HERDR_PANE_ID=$(herdr workspace create --no-focus |
    grep -o '"pane_id": *"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/')

shows() { herdr pane get "$HERDR_PANE_ID" | grep -qE "\"$1\": *\"$2\""; }
export -f shows

check "xeno is a linked Herdr plugin" bash -c "herdr plugin list | grep -q null-hype.xeno"
check "one arm reported is uncontrolled" bash -c \
    "[ \"\$(xeno verifier confirmed)\" = uncontrolled ] && shows xeno_verifier ● && shows xeno_falsifier ○"
check "both arms without Jev are unverified" bash -c \
    "[ \"\$(xeno falsifier failed)\" = unverified ] && shows xeno_jev ○"
check "a doubtful Jev keeps the run unverified" bash -c \
    "[ \"\$(xeno jev 0.3)\" = unverified ] && shows xeno_jev ◐"
check "a genuine run that held is confirmed, all three lit" bash -c \
    "[ \"\$(xeno jev 0.9)\" = confirmed ] && shows xeno_status confirmed && shows xeno_jev ●"
check "a refutation is on target too" bash -c \
    "[ \"\$(xeno falsifier refuted)\" = refuted ] && shows xeno_status refuted"
check "clearing removes the lights" bash -c \
    "xeno clear && ! herdr pane get \"\$HERDR_PANE_ID\" | grep -q xeno_"

herdr server stop >/dev/null 2>&1 || true
reportResults
