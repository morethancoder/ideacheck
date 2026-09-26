#!/usr/bin/env bash
# The public packages are the core other hosts import: the HTTP server, a
# hosted API, the gomobile binding (mobile/). They must not start processes or reach the
# terminal, the CLI's config or anything under internal/, and they must build
# for iOS and Android.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/_lib.sh

module="$(go list -m)"
public=(./configs/... ./ideacheck/... ./judge/... ./rubric/... ./prompt/... ./search/... ./store/... ./server/... ./mobile/...)
# modernc.org/libc, under the pure-Go SQLite, links os/exec for C's system();
# the store never calls it.
exec_ok="modernc.org/libc"

step "public packages stay host-agnostic"
bad="$(go list -deps -f '{{.ImportPath}}{{range .Imports}} {{.}}{{end}}' "${public[@]}" |
  awk -v mod="$module/internal/" -v exec_ok="$exec_ok" '{
    for (i = 2; i <= NF; i++) {
      d = $i
      if ((d == "os/exec" && $1 != exec_ok) || index(d, mod) == 1 ||
          d ~ /^github\.com\/(spf13\/cobra|charmbracelet\/|rs\/zerolog|knadh\/koanf)/)
        print "  " $1 " imports " d
    }
  }' | sort -u)"
if [ -n "$bad" ]; then err "a public package reaches something desktop-only:"; say "$bad"; exit 1; fi
ok "no os/exec, internal/, cobra, bubbletea, zerolog or koanf"

for goos in ios android; do
  step "build for $goos"
  CGO_ENABLED=0 GOOS="$goos" GOARCH=arm64 go build "${public[@]}"
  ok "$goos/arm64 builds"
done
