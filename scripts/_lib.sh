#!/usr/bin/env bash
# Shared helpers: colors, logging, env + CLI checks, confirmations.
# Sourced by every script in scripts/; do not execute directly.

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  BOLD=$'\033[1m'; RED=$'\033[31m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; CYAN=$'\033[36m'; RESET=$'\033[0m'
else
  BOLD=""; RED=""; GREEN=""; YELLOW=""; CYAN=""; RESET=""
fi

say()  { printf '%s\n' "$*"; }
step() { printf '%s==>%s %s%s%s\n' "$CYAN" "$RESET" "$BOLD" "$*" "$RESET"; }
ok()   { printf '%s ok %s %s\n' "$GREEN" "$RESET" "$*"; }
warn() { printf '%swarn%s %s\n' "$YELLOW" "$RESET" "$*" >&2; }
err()  { printf '%s err%s %s\n' "$RED" "$RESET" "$*" >&2; }

# load_env sources .env when present. API keys belong here or in your shell, never in config files.
load_env() {
  if [ -f .env ]; then set -a; . ./.env; set +a; fi
}

# has_cli NAME — quiet presence test.
has_cli() { command -v "$1" >/dev/null 2>&1; }

# require_cli NAME "install hint" — exit when missing.
require_cli() {
  if has_cli "$1"; then ok "$1 found"; return 0; fi
  err "$1 not found — $2"; exit 1
}

# want_cli NAME "install hint" — warn when missing, never fail.
want_cli() {
  if has_cli "$1"; then ok "$1 found"; else warn "$1 not found (optional) — $2"; fi
}

# want_env NAME "where to get it" — warn when unset, never fail, never print the value.
want_env() {
  if [ -n "${!1:-}" ]; then ok "$1 is set"; else warn "$1 is not set — $2"; fi
}

# confirm "question" — auto-yes when CI=1.
confirm() {
  [ "${CI:-}" = "1" ] && return 0
  read -r -p "$1 [y/N] " reply
  [[ "$reply" =~ ^[Yy]$ ]]
}
