#!/bin/bash
set -e

source dev-container-features-test-lib

check "runs as a non-root user" bash -c "[ \"\$(id -u)\" != 0 ]"
check "Neovim shows the example's finding and navigates to its evidence" bash -c \
    "cd /usr/local/share/witness/example && nvim --headless -c 'edit example.ts' -c 'luafile /usr/local/share/witness/nvim/check.lua'"

reportResults
