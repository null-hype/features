#!/bin/bash
set -e

source dev-container-features-test-lib

# The false positive in null-hype/worlds, replayed from recorded container-use
# diffs and recorded Jev scores: green as a check, a warning then an error to
# xeno, until the verifier installs witness the way people do.
herdr server >/tmp/herdr-server.log 2>&1 &
for _ in $(seq 50); do herdr workspace list >/dev/null 2>&1 && break; sleep 0.2; done
export HERDR_PANE_ID=$(herdr workspace create --no-focus |
    grep -o '"pane_id": *"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/')

cp -r /usr/local/share/xeno/example /tmp/claim && cd /tmp/claim
line=$(grep -n '^func' check.go | cut -d: -f1)
xeno claim "check.go:$line" >/dev/null

finding() { grep -q "\"$1\": \"$2\"" .witness/run/findings.json; }
export -f finding

check "today's green check is one arm: a warning on the claim" bash -c \
    "[ \"\$(xeno verifier confirmed today.diff)\" = uncontrolled ] && finding severity warning && grep -q \"\\\"line\\\": $line,\" .witness/run/findings.json"
check "with the falsifier, Jev doubts the verifier installed it as people do: an error" bash -c \
    "xeno falsifier failed falsifier.diff >/dev/null && [ \"\$(xeno jev 0.3)\" = unverified ] && finding severity error"
check "both arms' diffs are the finding's evidence" bash -c \
    "diff -q today.diff .witness/run/evidence/verifier.diff && diff -q falsifier.diff .witness/run/evidence/falsifier.diff"
check "installed the way people do, the claim is confirmed: information" bash -c \
    "xeno verifier confirmed verifier.diff >/dev/null && [ \"\$(xeno jev 0.9)\" = confirmed ] && finding severity information"

herdr server stop >/dev/null 2>&1 || true
reportResults
