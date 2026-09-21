#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/_lib.sh
load_env

step "Required tools"
require_cli go  "install from https://go.dev/dl/"
require_cli git "needed to stamp the build with a commit SHA"

step "Optional tools"
want_cli golangci-lint "brew install golangci-lint — make lint falls back to go vet + gofmt"
want_cli jq            "brew install jq — handy for -o json output"
want_cli docker        "runs the free local search engine for research: ideacheck search up"
want_cli gh            "brew install gh — needed by make release, not by the tool itself"

step "Providers (you need ONE; pick it with: ideacheck setup)"
want_cli claude "Claude CLI login — no API key needed"
want_cli codex  "Codex CLI login — no API key needed"
want_cli ollama "local models — no API key needed"
say "  API keys (Anthropic, OpenAI, OpenRouter) are optional: enter one in \`ideacheck setup\` or export it."
