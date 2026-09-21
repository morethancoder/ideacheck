#!/usr/bin/env bash
# searxng.sh up|down — a local SearXNG for research (`research.search: auto`
# finds it at research.endpoints.searxng). It listens on 127.0.0.1 only and has
# JSON output on, which ideacheck needs: see docker/searxng/settings.yml.
#   SEARXNG_PORT   host port (8080, the endpoint configs/config.yaml ships)
#   SEARXNG_IMAGE  image to run (searxng/searxng:latest)
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/_lib.sh
load_env

NAME=ideacheck-searxng
PORT="${SEARXNG_PORT:-8080}"
IMAGE="${SEARXNG_IMAGE:-searxng/searxng:latest}"
URL="http://localhost:$PORT"

has_cli docker || { err "docker not found — install Docker Desktop, or point research.endpoints.searxng at a SearXNG you already run"; exit 1; }
docker info >/dev/null 2>&1 || { err "docker is installed but not running — start it and try again"; exit 1; }

down() {
  # `docker rm -f` succeeds on a name that is not there, so ask first.
  if ! docker inspect "$NAME" >/dev/null 2>&1; then say "  $NAME is not running"; return 0; fi
  docker rm -f "$NAME" >/dev/null
  ok "stopped $NAME"
}

up() {
  if [ "$(docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null)" = "true" ]; then
    ok "$NAME is already running at $URL"
    return 0
  fi
  docker rm -f "$NAME" >/dev/null 2>&1 || true   # a stopped one: start fresh, with a fresh secret

  step "Starting SearXNG ($IMAGE)"
  docker run -d --name "$NAME" --restart unless-stopped \
    -p "127.0.0.1:$PORT:8080" \
    -v "$PWD/docker/searxng/settings.yml:/etc/searxng/settings.yml:ro" \
    -e "SEARXNG_SECRET=$(openssl rand -hex 32)" \
    "$IMAGE" >/dev/null

  for _ in $(seq 1 30); do
    if curl -fsS -o /dev/null "$URL/healthz" 2>/dev/null; then break; fi
    sleep 1
  done
  if ! curl -fsS -o /dev/null "$URL/search?q=ideacheck&format=json"; then
    err "SearXNG did not answer JSON at $URL — docker logs $NAME"
    exit 1
  fi
  ok "SearXNG answers JSON at $URL"
  if [ "$PORT" != 8080 ]; then
    say "  tell ideacheck: export IDEACHECK_RESEARCH__ENDPOINTS__SEARXNG=$URL"
  fi
  say "  stop it with: make down"
}

case "${1:-up}" in
  up) up ;;
  down) down ;;
  *) err "usage: searxng.sh up|down"; exit 1 ;;
esac
