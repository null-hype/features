#!/bin/bash
set -e

source dev-container-features-test-lib

check "xeno is on PATH" bash -c "command -v xeno"
check "Herdr is on PATH" bash -c "command -v herdr"

reportResults
