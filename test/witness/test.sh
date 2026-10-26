#!/bin/bash
set -e

source dev-container-features-test-lib

check "witness is on PATH" bash -c "command -v witness"
check "Neovim shows the example's finding and navigates to its evidence" bash -c \
    "cd /usr/local/share/witness/example && nvim --headless -c 'edit example.ts' -c 'luafile /usr/local/share/witness/nvim/check.lua'"

reportResults
