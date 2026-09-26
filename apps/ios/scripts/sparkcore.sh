#!/usr/bin/env bash
# Keeps build/Sparkcore.xcframework — the Go core, built by `make mobile` —
# in step with its sources, before Xcode compiles the app. Run by the
# Sparkjudge scheme's build pre-action, before Xcode plans the build, and
# again (`--phase`) by the Sparkcore target the app depends on (project.yml);
# by hand it is `apps/ios/scripts/sparkcore.sh`.
#
# With --phase a rebuild fails the build: Xcode has already copied the old
# framework into this build, so it would be linked once more. Build again.
#
# Up to date (nothing under the core's packages is newer than the
# xcframework) it only runs one `find` and exits. Otherwise it runs
# `make mobile` from the repository root (about 25 s) in a clean environment:
# Xcode's build variables (SDKROOT, ARCHS, the deployment target, …) would
# steer gomobile's own clang calls for the other platform's slice.
set -euo pipefail

root="$(cd "$(dirname "$0")/../../.." && pwd)"
out="$root/build/Sparkcore.xcframework"
stamp="$out/Info.plist"

# What the xcframework is built from: the core's packages and their configs
# (embedded with go:embed), and the module's requirements.
stale() {
  [ -f "$stamp" ] || return 0
  [ -n "$(cd "$root" && find ideacheck judge rubric prompt configs mobile go.mod go.sum -newer "$stamp" \
    \( -name '*.go' -o -name '*.yaml' -o -name '*.yml' -o -name '*.tmpl' -o -name '*.md' -o -name '*.json' -o -name 'go.mod' -o -name 'go.sum' \) \
    -print -quit 2>/dev/null)" ]
}

if ! stale; then
  exit 0
fi

# Xcode runs scripts with a bare PATH: look where Go is usually installed.
path="/usr/local/go/bin:/opt/homebrew/bin:/usr/local/bin:$HOME/go/bin:$HOME/.local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
if ! PATH="$path" command -v go >/dev/null 2>&1; then
  echo "error: Sparkcore.xcframework is missing or older than the Go core, and Go is not installed to rebuild it. Install Go (https://go.dev/dl or brew install go), then run make mobile in $root." >&2
  exit 1
fi

reason="missing"
[ -f "$stamp" ] && reason="older than the Go core"
echo "note: Sparkcore.xcframework is $reason: running make mobile"
env -i HOME="$HOME" USER="${USER:-}" TMPDIR="${TMPDIR:-/tmp}" PATH="$path" NO_COLOR=1 \
  make -C "$root" --no-print-directory mobile

if [ "${1:-}" = --phase ]; then
  echo "error: Sparkcore.xcframework was $reason and has been rebuilt during this build, after Xcode copied the old one in. Build again to link the new core. (The scheme's pre-action does this before a build starts; see build/sparkcore.log.)" >&2
  exit 1
fi
