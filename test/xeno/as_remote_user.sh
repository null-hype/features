#!/bin/bash
set -e

source dev-container-features-test-lib

check "runs as a non-root user" bash -c "[ \"\$(id -u)\" != 0 ]"
check "xeno is linked for the remote user" bash -c "herdr plugin list --json | grep -q null-hype.xeno"
check "Pass CLI is on PATH" bash -c "command -v pass-cli"
check "Xeno refreshes audit evidence safely" bash evidence.sh
check "Xeno reports judgments and missing evidence safely" bash assessment.sh

reportResults
