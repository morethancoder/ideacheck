#!/usr/bin/env bash
# exec.sh [ARGS] — run bin/ideacheck with .env loaded, so `make run/dev/serve/bench`
# see the same API keys as your shell would.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/_lib.sh

load_env
exec ./bin/ideacheck "$@"
