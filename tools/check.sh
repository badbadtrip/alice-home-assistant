#!/usr/bin/env bash
# All project checks in one run, the same ones CI runs: Python tests, the Home Functions
# server (lint, types, tests), the Go bridge (format, vet, tests) and the release manifest.
# Usage: tools/check.sh   (from any directory; needs python3, node with npx, go)
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
python=${PYTHON:-python3}
step() { printf '\n== %s\n' "$1"; }

step "Python tests"
(cd "$root" && "$python" -m unittest discover -s tests)

step "Home Functions server"
(cd "$root/alice/server" && npx -y pnpm@10.23.0 install --frozen-lockfile >/dev/null &&
  npx -y pnpm@10.23.0 lint && npx -y pnpm@10.23.0 typecheck && npx -y pnpm@10.23.0 test)
# The release check scans alice/ and must not see the installed dependencies.
rm -rf "$root/alice/server/node_modules"

step "Bridge"
(cd "$root/alice/bridge" && unformatted=$(gofmt -l .) && { [ -z "$unformatted" ] || { echo "gofmt: $unformatted"; exit 1; }; } &&
  go vet ./... && go test ./... github.com/grandcat/zeroconf github.com/skrashevich/station-airplay/audio)

step "Release manifest"
(cd "$root" && "$python" tools/sync_release.py && "$python" tools/check_release.py)
echo; echo "All checks passed."
