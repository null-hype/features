#!/bin/bash
# Drives a real headless Herdr server: a pane's agent finishes, xeno reads a
# recorded agent monitor, and the pane's done label must say whether both
# controls ran. The negative control comes first, so the positive control's
# plain "done" has to replace a label xeno set, not a default.
set -e

work="$(mktemp -d)"
mkdir -m 700 "$work/run"
export XDG_RUNTIME_DIR="$work/run" HERDR_SOCKET_PATH="$work/run/herdr.sock"
export SHELL=/bin/sh HERDR_DISABLE_SOUND=1 XENO_MONITOR="cat $work/monitor.json"

herdr server >"$work/server.log" 2>&1 &
server=$!
trap 'kill $server 2>/dev/null' EXIT
for _ in $(seq 100); do [ -S "$HERDR_SOCKET_PATH" ] && break; sleep 0.1; done

herdr workspace create --cwd "$work" >/dev/null
pane="$(herdr pane list | grep -o '"pane_id":"[^"]*"' | head -1 | cut -d'"' -f4)"

finish() {
    herdr pane report-agent "$pane" --source test --agent test-agent --state working >/dev/null
    herdr pane report-agent "$pane" --source test --agent test-agent --state done >/dev/null
}

expect() {
    for _ in $(seq 50); do
        herdr pane get "$pane" | grep -q "\"done\":\"$1\"" && return 0
        sleep 0.2
    done
    echo "expected done label: $1"
    herdr pane get "$pane"
    herdr plugin log list --plugin null-hype.xeno || true
    cat "$work/server.log"
    return 1
}

# Negative control: only the positive control ran.
echo '[{"reason":"positive control: same task"}]' >"$work/monitor.json"
finish
expect "done · uncontrolled"

# Positive control: both ran.
echo '[{"reason":"positive control: same task"},{"reason":"negative control: same task"}]' >"$work/monitor.json"
finish
expect "done"

# No monitor: missing evidence is not a clean result.
rm "$work/monitor.json"
finish
expect "done · unverified"
