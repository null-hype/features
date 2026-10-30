#!/bin/bash
set -e

source dev-container-features-test-lib

check "herdr is on PATH" bash -c "command -v herdr"
check "xeno is linked" bash -c "herdr plugin list --json | grep -q null-hype.xeno"
check "done is labelled by whether both controls ran" bash controls.sh

reportResults
