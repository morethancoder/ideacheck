#!/usr/bin/env bash
# build.sh [install] — compile with version + git SHA injected via -ldflags.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/_lib.sh

version="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
commit="$(git rev-parse --short HEAD 2>/dev/null || echo none)"
ldflags="-s -w -X github.com/morethancoder/ideacheck/internal/cli.Version=${version} -X github.com/morethancoder/ideacheck/internal/cli.Commit=${commit}"

if [ "${1:-}" = "install" ]; then
  step "Installing ideacheck ${version} (${commit})"
  go install -ldflags "$ldflags" ./cmd/ideacheck
  ok "installed to $(go env GOBIN 2>/dev/null | grep . || echo "$(go env GOPATH)/bin")"
else
  step "Building ideacheck ${version} (${commit})"
  go build -ldflags "$ldflags" -o bin/ideacheck ./cmd/ideacheck
  ok "bin/ideacheck"
fi
