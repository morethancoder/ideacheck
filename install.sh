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

# Live redraws (the progress bar, pending rows) only happen on a terminal; a
# pipe or a CI log gets one plain line per step instead.
TTY=; [ -t 1 ] && TTY=1
if [ -n "$TTY" ] && [ -z "${NO_COLOR:-}" ]; then
  BOLD=$(printf '\033[1m'); DIM=$(printf '\033[2m'); GREEN=$(printf '\033[32m')
  YELLOW=$(printf '\033[33m'); RED=$(printf '\033[31m'); RESET=$(printf '\033[0m')
else
  BOLD=; DIM=; GREEN=; YELLOW=; RED=; RESET=
fi
case "${LC_ALL:-${LC_CTYPE:-${LANG:-}}}" in
  *[Uu][Tt][Ff]-8*|*[Uu][Tt][Ff]8*) G_OK='✓'; G_ERR='✗'; G_BUSY='↓'; G_WAIT='·'; G_FILL='━'; G_REST='─' ;;
  *) G_OK='+'; G_ERR='x'; G_BUSY='>'; G_WAIT='.'; G_FILL='#'; G_REST='-' ;;
esac

tmp=
pid=
cleanup() {
  [ -z "$pid" ] || kill "$pid" 2>/dev/null || true
  [ -z "$TTY" ] || printf '\033[?25h'
  [ -z "$tmp" ] || rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

clear_line() { [ -z "$TTY" ] || printf '\r\033[K'; }

# row LABEL VALUE — a finished step. It replaces whatever pending line is showing.
row() {
  clear_line
  printf '  %s%s%s %s%-10s%s %s\n' "$GREEN" "$G_OK" "$RESET" "$DIM" "$1" "$RESET" "$2"
}

# pending LABEL TEXT — a step in flight; the next row, or an error, overwrites it.
pending() {
  [ -n "$TTY" ] || return 0
  clear_line
  printf '  %s%s %-10s %s%s' "$DIM" "$G_WAIT" "$1" "$2" "$RESET"
}

warn() { clear_line; printf '  %s!%s %s\n' "$YELLOW" "$RESET" "$*" >&2; }
err()  { clear_line; printf '  %s%s%s %s\n\n' "$RED" "$G_ERR" "$RESET" "$*" >&2; exit 1; }

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

# size BYTES — print it the way a person reads it.
size() {
  if [ "$1" -lt 1048576 ]; then printf '%d KB' $(($1 / 1024))
  else printf '%d.%d MB' $(($1 / 1048576)) $(($1 % 1048576 * 10 / 1048576))
  fi
}

# progress GOT TOTAL — redraw the download row. TOTAL is 0 until the server says.
progress() {
  if [ "$2" -le 0 ]; then
    printf '\r\033[K  %s%s %-10s%s %s' "$DIM" "$G_BUSY" download "$RESET" "$(size "$1")"
    return
  fi
  pct=$(($1 * 100 / $2)); [ "$pct" -le 100 ] || pct=100
  fill=$((pct * BAR / 100)); done_=; rest=; i=0
  while [ "$i" -lt "$BAR" ]; do
    if [ "$i" -lt "$fill" ]; then done_="$done_$G_FILL"; else rest="$rest$G_REST"; fi
    i=$((i + 1))
  done
  printf '\r\033[K  %s%s %-10s%s %s%s%s%s%s %3d%%  %s%s / %s%s' \
    "$DIM" "$G_BUSY" download "$RESET" "$GREEN" "$done_" "$RESET$DIM" "$rest" "$RESET" \
    "$pct" "$DIM" "$(size "$1")" "$(size "$2")" "$RESET"
}

# fetch URL FILE — download in the background and draw its progress while it
# runs. The total comes from the response headers of the same request, so there
# is no second round trip and nothing to get out of step.
fetch() {
  hdr="$tmp/headers"; : > "$hdr"
  if have curl; then curl -fsSL -D "$hdr" -o "$2" "$1" 2>/dev/null &
  elif have wget; then wget -qS -O "$2" "$1" 2>"$hdr" &
  else err "neither curl nor wget is installed, so nothing can be downloaded"
  fi
  pid=$!
  if [ -n "$TTY" ]; then
    BAR=24; [ "$(tput cols 2>/dev/null || echo 80)" -ge 66 ] || BAR=10
    total=0
    printf '\033[?25l'
    while kill -0 "$pid" 2>/dev/null; do
      got=0; [ ! -f "$2" ] || got=$(($(wc -c < "$2")))
      # Redirects carry their own Content-Length; only the last one, once the
      # body has started arriving, is the size of the archive.
      if [ "$total" -le 0 ] && [ "$got" -gt 0 ]; then
        total=$(awk 'tolower($1) == "content-length:" { v = $2 } END { print v + 0 }' "$hdr")
      fi
      progress "$got" "$total"
      sleep 0.1 2>/dev/null || sleep 1
    done
    printf '\033[?25h'
  fi
  rc=0; wait "$pid" || rc=$?
  pid=
  return "$rc"
}

# sha256 FILE — print the hex digest, whichever tool this machine has.
sha256() {
  if have sha256sum; then sha256sum "$1" | cut -d' ' -f1
  elif have shasum; then shasum -a 256 "$1" | cut -d' ' -f1
  else err "no sha256sum or shasum here, so the download cannot be verified"
  fi
}

printf '\n  %s%s%s %sinstaller%s\n\n' "$BOLD" "$BIN" "$RESET" "$DIM" "$RESET"

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
row system "$os/$arch"

version="${IDEACHECK_VERSION:-}"
if [ -z "$version" ]; then
  pending version "asking GitHub for the latest release"
  version="$(get "https://api.github.com/repos/$REPO/releases/latest" |
    sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)"
fi
[ -n "$version" ] || err "could not read the latest version from GitHub. Set IDEACHECK_VERSION=0.1.0 to pick one, or check https://github.com/$REPO/releases"
version="${version#v}"
row version "$version"

archive="${BIN}_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/v$version"

tmp="$(mktemp -d)"

pending download "$archive"
fetch "$base/$archive" "$tmp/$archive" ||
  err "could not download $archive. Is $version a released version? See https://github.com/$REPO/releases"
row download "$(size $(($(wc -c < "$tmp/$archive"))))"

pending verify "checking the sha256 checksum"
get "$base/checksums.txt" "$tmp/checksums.txt" ||
  err "could not download checksums.txt, so the download cannot be verified"
want="$(grep " $archive\$" "$tmp/checksums.txt" | cut -d' ' -f1 | head -n1)"
[ -n "$want" ] || err "checksums.txt has no entry for $archive"
got="$(sha256 "$tmp/$archive")"
[ "$want" = "$got" ] || err "$archive does not match its checksum — the download was corrupted or tampered with. Nothing was installed."
row verify "sha256 matches the release"

tar -xzf "$tmp/$archive" -C "$tmp" || err "could not unpack $archive"
[ -f "$tmp/$BIN" ] || err "$archive does not contain a $BIN binary"

dir="${IDEACHECK_INSTALL_DIR:-$HOME/.local/bin}"
mkdir -p "$dir" || err "could not create $dir. Pick another with: IDEACHECK_INSTALL_DIR=/usr/local/bin"
[ -w "$dir" ] || err "$dir is not writable by you. Pick a directory you own with IDEACHECK_INSTALL_DIR=..., or re-run this with sudo."
# Install through a temp name in the target directory, so the last step is an
# atomic rename: a running ideacheck is never half-replaced.
mv "$tmp/$BIN" "$dir/.$BIN.new" || err "could not write to $dir"
chmod 755 "$dir/.$BIN.new"
mv "$dir/.$BIN.new" "$dir/$BIN"
case "$dir" in
  "$HOME"/*) shown="~${dir#"$HOME"}" ;;
  *) shown="$dir" ;;
esac
row install "$shown/$BIN"

printf '\n  %s%s %s is ready%s\n\n' "$BOLD" "$BIN" "$version" "$RESET"
printf '  %-18s %s%s%s\n' "$BIN" "$DIM" "check an idea, or open the app" "$RESET"
printf '  %-18s %s%s%s\n' "$BIN setup" "$DIM" "pick a model provider (the default is free and local)" "$RESET"
printf '  %-18s %s%s%s\n\n' "$BIN upgrade" "$DIM" "update to the next release" "$RESET"

case ":$PATH:" in
  *":$dir:"*) ;;
  *)
    warn "$shown is not on your PATH yet. Add this to ~/.zshrc (or ~/.bashrc):"
    printf '\n    export PATH="%s:$PATH"\n\n' "$dir"
    ;;
esac
