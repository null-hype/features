#!/bin/bash
set -e

source dev-container-features-test-lib

check "runs as a non-root user" bash -c "[ \"\$(id -u)\" != 0 ]"
check "xeno is linked for the remote user" bash -c "herdr plugin list --json | grep -q null-hype.xeno"
check "done is labelled by whether both controls ran" bash controls.sh

reportResults
