#!/usr/bin/env bash
# Full verification of a change. Go/Node are NOT required locally – everything runs in Docker.
# Usage: scripts/check.sh            go vet + gofmt, JS syntax, image build, smoke test
#        scripts/check.sh tidy       go mod tidy (after changing imports/dependencies)
set -euo pipefail
cd "$(dirname "$0")/.."

IMAGE="${IMAGE:-pod-manager:dev}"
GO_IMAGE="${GO_IMAGE:-golang:1.26-alpine}"   # keep in sync with the Dockerfile
UIDGID="$(id -u):$(id -g)"
go_run() { docker run --rm -v "$PWD":/src -w /src -v pod-manager-gomod:/go/pkg/mod "$GO_IMAGE" sh -c "$1"; }

if [[ "${1:-}" == "tidy" ]]; then
  go_run "go mod tidy && chown $UIDGID go.mod go.sum"
  exit 0
fi

echo "== go vet / gofmt"
if ! out="$(go_run 'go vet ./... && f="$(gofmt -l .)" && { [ -z "$f" ] || { echo "gofmt needed: $f"; exit 1; }; }' 2>&1)"; then
  grep -v '^go: downloading' <<<"$out"
  exit 1
fi

echo "== JS syntax"
docker run --rm -v "$PWD/web":/w:ro node:22-alpine node --check /w/app.js

echo "== image build ($IMAGE)"
docker build -q -t "$IMAGE" . >/dev/null

echo "== smoke test"
scripts/smoke-test.sh "$IMAGE"
echo "ALL OK"
