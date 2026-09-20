#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/_lib.sh

bash scripts/doctor.sh
step "Downloading Go modules"
go mod download
ok "ready — try: make run ARGS='\"my idea\" -b mock -o plain'"
