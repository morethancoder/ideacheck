#!/usr/bin/env bash
# Deploy the Sparkjudge hosted API (cmd/sparkjudge-api) to Fly.io with fly.toml.
# The first deploy needs an app, a volume and secrets that only you can create:
# docs/sparkjudge-api.md → "First deploy". Extra arguments go to `fly deploy`.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/_lib.sh

app="${FLY_APP:-$(awk -F'"' '/^app =/ {print $2}' fly.toml)}"

step "preflight"
require_cli fly "https://fly.io/docs/flyctl/install/"
if ! fly auth whoami >/dev/null 2>&1; then err "not logged in to Fly — run: fly auth login"; exit 1; fi
ok "logged in to Fly"
if ! fly status --app "$app" >/dev/null 2>&1; then
  err "there is no Fly app named $app — create it first (docs/sparkjudge-api.md → First deploy)"
  exit 1
fi
ok "app $app exists"

step "secrets"
listed="$(fly secrets list --app "$app" --json 2>/dev/null || echo '[]')"
missing=()
for name in TYPESAFE_API_KEY OPENROUTER_API_KEY REVENUECAT_WEBHOOK_AUTH SPARKJUDGE_TEAM_ID; do
  if grep -q "\"$name\"" <<<"$listed"; then ok "$name is set"; else missing+=("$name"); fi
done
if ! grep -qE '"(TAVILY_API_KEY|BRAVE_API_KEY)"' <<<"$listed"; then
  warn "neither TAVILY_API_KEY nor BRAVE_API_KEY is set: research falls back to OpenRouter's web plugin (billed per search)"
fi
if [ "${#missing[@]}" -gt 0 ]; then
  err "missing secrets: ${missing[*]}"
  say "  set them (values stay out of your shell history if you paste them at the prompt):"
  say "  fly secrets set --app $app $(printf '%s=... ' "${missing[@]}")"
  exit 1
fi

step "tests"
go test ./internal/sparkjudge/... ./server/... ./store/...
ok "tests pass"

step "fly deploy"
fly deploy --app "$app" "$@"
ok "deployed — https://$app.fly.dev/v1/healthz"
