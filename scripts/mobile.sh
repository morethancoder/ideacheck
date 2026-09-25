#!/usr/bin/env bash
# Builds build/Sparkcore.xcframework from ./mobile with gomobile: the whole
# check, for the Sparkjudge iOS app, on the device and in the simulator.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/_lib.sh

# gomobile, gobind and the bind package they generate code against must be
# the same version.
x_mobile="golang.org/x/mobile"
version="v0.0.0-20260908204917-8b95e45f8d3e"
out="build/Sparkcore.xcframework"
root="$PWD"
work="$root/build/bind"

require_cli xcrun "install Xcode (the command line tools alone lack the iOS SDKs)"

step "gomobile and gobind $version"
mkdir -p build/bin
GOBIN="$root/build/bin" go install "$x_mobile/cmd/gomobile@$version" "$x_mobile/cmd/gobind@$version"
export PATH="$root/build/bin:$PATH"
ok "build/bin"

# The generated code imports golang.org/x/mobile/bind, so it has to be in the
# build — but not in go.mod, where it would drag x/net and x/tools forward for
# the CLI too. So the bind runs from a throwaway module beside the output that
# requires both, with this repository replaced by its working tree. (Not a
# go.work: gomobile builds its generated module under the same GOWORK, which
# then refuses a module the workspace does not list.)
module="$(go list -m)"
rm -rf "$work"
mkdir -p "$work"
printf 'package bind\n\nimport (\n\t_ "%s/mobile"\n\t_ "%s/bind"\n)\n' "$module" "$x_mobile" > "$work/bind.go"
(
  cd "$work"
  export GOWORK=off
  go mod init sparkcore.bind 2>/dev/null
  go mod edit -require="$module@v0.0.0" -replace="$module=$root" -require="$x_mobile@$version"
  go mod tidy 2>/dev/null
)

step "gomobile bind -target=ios,iossimulator"
start=$(date +%s)
rm -rf "$out"
(cd "$work" && GOWORK=off gomobile bind -target=ios,iossimulator -ldflags='-s -w' -o "$root/$out" "$module/mobile")
ok "$out in $(($(date +%s) - start))s"
for slice in "$out"/*/; do
  bin="$(find "$slice" -type f -name Sparkcore | head -1)"
  say "   $(basename "$slice"): $(du -sh "$slice" | cut -f1) on disk, binary $(du -h "$bin" | cut -f1)"
done

# `make mobile ARGS=test` then runs the Swift proof (mobile/swiftcheck) in the
# simulator: a check from Swift with a stub judge, and on the on-device model
# when the simulator's host has Apple Intelligence (skipped otherwise).
if [ "${1:-}" = test ]; then
  require_cli xcodegen "brew install xcodegen"
  step "swiftcheck on ${SIMULATOR:=iPhone 16 Pro}"
  (cd mobile/swiftcheck && xcodegen generate --quiet &&
    xcodebuild test -quiet -project SwiftCheck.xcodeproj -scheme SwiftCheck \
      -destination "platform=iOS Simulator,name=$SIMULATOR,OS=latest")
  ok "swiftcheck passed"
fi
