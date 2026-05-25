#!/usr/bin/env bash
# Verify the development toolchain needed by this repo. Invoked from
# `make doctor`. Exits non-zero if any required tool is missing or
# below its minimum version so it doubles as a CI pre-flight check.
#
# Required tools:
#   go         — backend compiler (≥1.24.2 per go.mod)
#   node       — frontend runtime (Vite + Vitest)
#   npm        — frontend package manager
#   wails      — desktop app build CLI
#   staticcheck — Go static analyser (lint)

set -uo pipefail

# Minimum Go version is pulled from go.mod; keep in sync if go.mod
# bumps. The doctor target is checking developer setup, not the
# go.mod itself, so duplicating here is intentional.
GO_MIN="1.24.2"

OK=0
FAIL=0

GREEN=$'\033[32m'
RED=$'\033[31m'
YELLOW=$'\033[33m'
DIM=$'\033[2m'
RESET=$'\033[0m'

# present <name> <command> <version-cmd> <install-hint>
# Prints ✓ + the version line on success, ✗ + install hint on
# failure. version-cmd is evaluated in a subshell so it can pipe.
present() {
  local name="$1" cmd="$2" vcmd="$3" hint="$4"
  if command -v "$cmd" >/dev/null 2>&1; then
    local v
    v="$(eval "$vcmd" 2>&1 | head -1)"
    printf "${GREEN}✓${RESET} %-12s ${DIM}%s${RESET}\n" "$name" "$v"
    OK=$((OK + 1))
    return 0
  fi
  printf "${RED}✗${RESET} %-12s missing\n" "$name"
  printf "  ${DIM}install: %s${RESET}\n" "$hint"
  FAIL=$((FAIL + 1))
  return 1
}

# version_ge <have> <want> — true if `have` ≥ `want` via sort -V.
version_ge() {
  [[ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -1)" == "$2" ]]
}

echo "Checking development tools…"
echo

present go         go         'go version'         'https://go.dev/dl/  (need ≥'"$GO_MIN"')'
present node       node       'node --version'     'https://nodejs.org/  (LTS)'
present npm        npm        'npm --version'      'bundled with Node.js'
present wails      wails      'wails version'      'go install github.com/wailsapp/wails/v2/cmd/wails@latest'
present staticcheck staticcheck 'staticcheck -version' 'go install honnef.co/go/tools/cmd/staticcheck@latest'

# Version constraint check — Go must be ≥ GO_MIN.
if command -v go >/dev/null 2>&1; then
  GO_VER="$(go version | awk '{print $3}' | sed 's/^go//')"
  if ! version_ge "$GO_VER" "$GO_MIN"; then
    echo
    printf "${YELLOW}!${RESET} Go %s is older than required %s — update before building.\n" "$GO_VER" "$GO_MIN"
    FAIL=$((FAIL + 1))
  fi
fi

echo
if [[ $FAIL -gt 0 ]]; then
  printf "${RED}%d tool(s) missing or out of date.${RESET} See install hints above.\n" "$FAIL"
  exit 1
fi
printf "${GREEN}All %d required tools present.${RESET}\n" "$OK"
