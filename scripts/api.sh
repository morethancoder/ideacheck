#!/usr/bin/env bash
# Run the Sparkjudge hosted API on this machine with no model and no App
# Attest: the mock judge, the X-App-User-Id header alone, data under bin/.
# Extra arguments go to sparkjudge-api (-b, -attest, -listen, -c).
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/_lib.sh
load_env

export SPARKJUDGE_DATA_DIR="${SPARKJUDGE_DATA_DIR:-bin/sparkjudge-data}"
export REVENUECAT_WEBHOOK_AUTH="${REVENUECAT_WEBHOOK_AUTH:-Bearer local}"
step "sparkjudge-api on http://127.0.0.1:8787 (mock judge, attest off, data in $SPARKJUDGE_DATA_DIR)"
exec go run ./cmd/sparkjudge-api -b mock -attest off -listen 127.0.0.1:8787 "$@"
