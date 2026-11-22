#!/bin/bash
set -e

source dev-container-features-test-lib

# The delta-zero plugin at a pinned commit; its deploy-approval example is the input.
PLUGIN_COMMIT=72737e09012ccbb48e4a8d7cf413706492e8eb31
src="$(mktemp -d)"
out="$(mktemp -d)"
git -C "$src" init -q
git -C "$src" fetch -q --depth 1 https://github.com/null-hype/agent-plugins.git "$PLUGIN_COMMIT"
git -C "$src" checkout -q FETCH_HEAD

check "runs as a non-root user" bash -c "[ \"\$(id -u)\" != 0 ]"
check "python3 and pkl are installed" bash -c "command -v python3 && command -v pkl"
check "the detector writes a thread that Pkl evaluates" bash -c \
    "cd '$src' && python3 -I pkl_thread.py --summarize examples/deploy-approval/summarize.json --full examples/deploy-approval/full.json '$out' && pkl eval '$out/thread.pkl' >/dev/null"
check "findings include the instruction the summary lost" \
    grep -q "Do not deploy this to production" "$out/.witness/run/findings.json"

reportResults
