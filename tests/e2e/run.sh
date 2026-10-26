#!/bin/sh
# Runs the browser tests against a throwaway instance of the current code.
# Python, Playwright and Chromium come from nixpkgs. Screenshots go to
# tests/e2e/out. Set FEED_URL to also test a subscription.
set -eu
cd "$(dirname "$0")/../.."
mkdir -p tests/e2e/out
tmp=$(mktemp -d)
trap 'kill $pid 2>/dev/null || true; rm -rf "$tmp"' EXIT

go build -o "$tmp/calendar" ./cmd/server
"$tmp/calendar" -port 18080 -data-dir "$tmp/data" >"$tmp/server.log" 2>&1 &
pid=$!
sleep 1

python=$(nix build --no-link --print-out-paths --impure --expr \
  'let p = import (builtins.getFlake "nixpkgs") {}; in p.python3.withPackages (ps: [ ps.playwright ])')
browsers=$(nix build --no-link --print-out-paths --impure --expr \
  'let p = import (builtins.getFlake "nixpkgs") {}; in p.playwright-driver.browsers')

PLAYWRIGHT_BROWSERS_PATH="$browsers" BASE_URL=http://localhost:18080 OUT=tests/e2e/out \
  "$python/bin/python3" -I tests/e2e/smoke.py
