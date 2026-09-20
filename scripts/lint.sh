#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/_lib.sh

step "go vet"
go vet ./...
ok "vet clean"

step "gofmt"
unformatted="$(gofmt -l cmd internal configs)"
if [ -n "$unformatted" ]; then err "needs gofmt (run make fmt):"; say "$unformatted"; exit 1; fi
ok "formatted"

step "golangci-lint"
if has_cli golangci-lint; then
  golangci-lint run ./...
  ok "golangci-lint clean"
else
  warn "golangci-lint not installed — skipped (brew install golangci-lint)"
fi
