#!/bin/sh
# install.sh — put ideacheck on this machine.
#
#   curl -fsSL https://raw.githubusercontent.com/morethancoder/ideacheck/main/install.sh | sh
#
# It downloads the release build for this OS and CPU, checks it against the
# release's published checksums, and installs it into ~/.local/bin. Nothing is
# compiled, nothing needs Go, and nothing is written outside the install
# directory. After this, `ideacheck upgrade` keeps it current.
#
# Environment:
#   IDEACHECK_VERSION      version to install (default: the latest release)
#   IDEACHECK_INSTALL_DIR  where to put the binary (default: ~/.local/bin)
set -eu

REPO="morethancoder/ideacheck"
BIN="ideacheck"

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  BOLD=$(printf '\033[1m'); GREEN=$(printf '\033[32m'); YELLOW=$(printf '\033[33m')
  RED=$(printf '\033[31m'); CYAN=$(printf '\033[36m'); RESET=$(printf '\033[0m')
else
  BOLD=; GREEN=; YELLOW=; RED=; CYAN=; RESET=
fi

step() { printf '%s==>%s %s%s%s\n' "$CYAN" "$RESET" "$BOLD" "$*" "$RESET"; }
ok()   { printf '%s ok %s %s\n' "$GREEN" "$RESET" "$*"; }
warn() { printf '%swarn%s %s\n' "$YELLOW" "$RESET" "$*" >&2; }
err()  { printf '%s err%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }

have() { command -v "$1" >/dev/null 2>&1; }

# get URL [FILE] — download to FILE, or to stdout.
get() {
  if have curl; then
    if [ $# -eq 2 ]; then curl -fsSL "$1" -o "$2"; else curl -fsSL "$1"; fi
  elif have wget; then
    if [ $# -eq 2 ]; then wget -qO "$2" "$1"; else wget -qO- "$1"; fi
  else
    err "neither curl nor wget is installed, so nothing can be downloaded"
  fi
}

# sha256 FILE — print the hex digest, whichever tool this machine has.
sha256() {
  if have sha256sum; then sha256sum "$1" | cut -d' ' -f1
  elif have shasum; then shasum -a 256 "$1" | cut -d' ' -f1
  else err "no sha256sum or shasum here, so the download cannot be verified"
  fi
}

step "Looking at this machine"
os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  darwin|linux) ;;
  *) err "there is no ideacheck build for $os yet — build from source with: go install github.com/$REPO/cmd/$BIN@latest" ;;
esac
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) err "there is no ideacheck build for $arch yet — build from source with: go install github.com/$REPO/cmd/$BIN@latest" ;;
esac
ok "$os/$arch"

step "Finding the version to install"
version="${IDEACHECK_VERSION:-}"
if [ -z "$version" ]; then
  version="$(get "https://api.github.com/repos/$REPO/releases/latest" |
    sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)"
fi
[ -n "$version" ] || err "could not read the latest version from GitHub. Set IDEACHECK_VERSION=0.1.0 to pick one, or check https://github.com/$REPO/releases"
version="${version#v}"
ok "$version"

archive="${BIN}_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/v$version"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

step "Downloading $archive"
get "$base/$archive" "$tmp/$archive" ||
  err "could not download $archive. Is $version a released version? See https://github.com/$REPO/releases"
get "$base/checksums.txt" "$tmp/checksums.txt" ||
  err "could not download checksums.txt, so the download cannot be verified"

want="$(grep " $archive\$" "$tmp/checksums.txt" | cut -d' ' -f1 | head -n1)"
[ -n "$want" ] || err "checksums.txt has no entry for $archive"
got="$(sha256 "$tmp/$archive")"
[ "$want" = "$got" ] || err "$archive does not match its checksum — the download was corrupted or tampered with. Nothing was installed."
ok "checksum verified"

tar -xzf "$tmp/$archive" -C "$tmp" || err "could not unpack $archive"
[ -f "$tmp/$BIN" ] || err "$archive does not contain a $BIN binary"

dir="${IDEACHECK_INSTALL_DIR:-$HOME/.local/bin}"
step "Installing into $dir"
mkdir -p "$dir" || err "could not create $dir. Pick another with: IDEACHECK_INSTALL_DIR=/usr/local/bin"
[ -w "$dir" ] || err "$dir is not writable by you. Pick a directory you own with IDEACHECK_INSTALL_DIR=..., or re-run this with sudo."
# Install through a temp name in the target directory, so the last step is an
# atomic rename: a running ideacheck is never half-replaced.
mv "$tmp/$BIN" "$dir/.$BIN.new" || err "could not write to $dir"
chmod 755 "$dir/.$BIN.new"
mv "$dir/.$BIN.new" "$dir/$BIN"
ok "$dir/$BIN"

case ":$PATH:" in
  *":$dir:"*) ;;
  *)
    warn "$dir is not on your PATH. Add this to ~/.zshrc (or ~/.bashrc):"
    printf '\n    export PATH="%s:$PATH"\n\n' "$dir"
    ;;
esac

printf '\n%sideacheck %s is installed.%s\n' "$BOLD" "$version" "$RESET"
printf '  %s                 check an idea, or open the app\n' "$BIN"
printf '  %s setup           pick a model provider (the default is free and local)\n' "$BIN"
printf '  %s upgrade         update to the next release\n' "$BIN"
