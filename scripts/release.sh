#!/usr/bin/env bash
# release.sh [version] — tag a release. GitHub Actions does the rest: it builds
# macOS and Linux binaries, publishes them with checksums, and every installed
# ideacheck finds the new version within a day (or on `ideacheck upgrade`).
#
#   make release             # asks for the version, suggesting the next patch
#   make release ARGS=0.2.0  # or say it outright
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/_lib.sh

require_cli git "install from https://git-scm.com"
require_cli gh  "brew install gh — the release is published through GitHub"

step "Checks before tagging"
[ -z "$(git status --porcelain)" ] || { err "the working tree has uncommitted changes; commit or stash them first"; exit 1; }
branch="$(git rev-parse --abbrev-ref HEAD)"
[ "$branch" = "main" ] || { err "releases are cut from main, and you are on $branch"; exit 1; }
git fetch --tags --quiet
ok "clean tree on main"

latest="$(git tag --list 'v*' --sort=-v:refname | head -n1)"
say "  latest tag: ${latest:-none yet}"

version="${1:-}"
if [ -z "$version" ]; then
  suggest="0.1.0"
  if [ -n "$latest" ]; then
    IFS='.' read -r major minor patch <<<"${latest#v}"
    suggest="$major.$minor.$((patch + 1))"
  fi
  read -r -p "version to release [$suggest]: " version
  version="${version:-$suggest}"
fi
version="${version#v}"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { err "version must be X.Y.Z, got '$version'"; exit 1; }
[ "v$version" != "$latest" ] || { err "v$version is already the latest tag"; exit 1; }
if git rev-parse -q --verify "refs/tags/v$version" >/dev/null; then
  err "tag v$version already exists"; exit 1
fi

step "Running the tests the release will ship"
go test ./...
ok "tests pass"

confirm "tag v$version and publish it?" || { warn "nothing was tagged"; exit 0; }

step "Tagging v$version"
git tag -a "v$version" -m "ideacheck v$version"
git push origin "v$version"
ok "pushed v$version"

step "Building the release"
say "  GitHub Actions is building it now:"
gh run list --workflow=release.yml --limit 1 || true
if confirm "watch the build?"; then
  sleep 5 # give the push event time to create the run
  gh run watch --exit-status || { err "the release build failed; see: gh run view --log-failed"; exit 1; }
fi
ok "https://github.com/morethancoder/ideacheck/releases/tag/v$version"
