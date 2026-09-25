#!/usr/bin/env bash
# Launches the built app in a booted simulator with demo launch arguments and
# saves one screenshot per screen.
#   scripts/screenshots.sh <simulator-udid> [out-dir]
# Build first: xcodebuild -scheme Sparkjudge -destination 'platform=iOS Simulator,id=<udid>' -derivedDataPath build/DerivedData build
set -euo pipefail
cd "$(dirname "$0")/.."

device="${1:?usage: scripts/screenshots.sh <simulator-udid> [out-dir]}"
out="${2:-build/shots}"
app="build/DerivedData/Build/Products/Debug-iphonesimulator/Sparkjudge.app"
bundle="com.morethancoder.sparkjudge"
mkdir -p "$out"

xcrun simctl boot "$device" 2>/dev/null || true
xcrun simctl install "$device" "$app"
xcrun simctl status_bar "$device" override --time 9:41 --batteryState charged --batteryLevel 100 --cellularBars 4 --wifiBars 3

shoot() {
  local name="$1" wait="$2"
  shift 2
  xcrun simctl terminate "$device" "$bundle" 2>/dev/null || true
  xcrun simctl launch "$device" "$bundle" -sjScheme dark -checker preview "$@" >/dev/null
  sleep "$wait"
  xcrun simctl io "$device" screenshot "$out/$name.png" >/dev/null 2>&1
  echo "  ✓ $name"
}

shoot home 3
shoot dictating 6 -sjDemoDictation YES -sjAutoDictate YES
shoot review 4 -sjSeed YES -sjOpen review
shoot list 4 -sjSeed YES -sjTab ideas -ideasLayout list
shoot piles 4 -sjSeed YES -sjTab ideas -ideasLayout pile
shoot pile-open 4 -sjSeed YES -sjTab ideas -sjOpen pile:creative
shoot detail 4 -sjSeed YES -sjTab ideas -sjOpen detail
shoot settings 5 -sjTab settings
shoot list-light 4 -sjSeed YES -sjTab ideas -ideasLayout list -sjScheme light
xcrun simctl terminate "$device" "$bundle" 2>/dev/null || true
