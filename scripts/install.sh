#!/bin/sh
# deep-research installer — Linux, macOS, WSL2, Termux.
# Downloads the latest release binary from GitHub into ~/.local/bin
# (/usr/local/bin as root, $PREFIX/bin on Termux).
set -eu

repo=juanhuttemann/deep-research

# Colour only on a terminal: piped into a log, escapes are noise. NO_COLOR
# (https://no-color.org) turns it off. curl | sh leaves stdout the terminal.
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-}" != dumb ]; then
  bold=$(printf '\033[1m') dim=$(printf '\033[2m') red=$(printf '\033[31m')
  green=$(printf '\033[32m') yellow=$(printf '\033[33m') cyan=$(printf '\033[36m')
  reset=$(printf '\033[0m')
else
  bold='' dim='' red='' green='' yellow='' cyan='' reset=''
fi

step() { printf '  %s✓%s %s\n' "$green" "$reset" "$*"; }
note() { printf '  %s!%s %s\n' "$yellow" "$reset" "$*" >&2; }
fail() {
  printf '  %s✗ %s%s\n' "$red" "$*" "$reset" >&2
  exit 1
}
# tilde shows a path under $HOME as ~/…, the way it is typed.
tilde() { case "$1" in "$HOME"/*) printf '~/%s' "${1#"$HOME"/}" ;; *) printf '%s' "$1" ;; esac; }

printf '\n  %sdeep-research%s installer\n\n' "$bold" "$reset"

os=$(uname -s)
case "$os" in
  Linux*) os=linux ;;
  Darwin*) os=darwin ;;
  *) fail "unsupported OS: $os (on Windows use install.ps1)" ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "unsupported architecture: $arch" ;;
esac

bin_dir=${DEEP_RESEARCH_BIN_DIR:-$HOME/.local/bin}
if [ "$(id -u)" = 0 ]; then bin_dir=/usr/local/bin; fi
if [ -n "${TERMUX_VERSION:-}" ]; then bin_dir=${PREFIX:?}/bin; fi

asset="deep-research_${os}_${arch}.tar.gz"
url="https://github.com/$repo/releases/latest/download/$asset"
sum_url="https://github.com/$repo/releases/latest/download/deep-research_checksums.txt"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Keep the release filename: sha256sum -c resolves it relative to $tmp.
curl -fsSL "$url" -o "$tmp/$asset" || fail "could not download $url"
step "downloaded $asset"

# A release without a matching checksum is not safe to install.
curl -fsSL "$sum_url" -o "$tmp/checksums.txt" || fail "could not download the checksums"
line=$(awk -v asset="$asset" '$2 == asset { print }' "$tmp/checksums.txt")
[ "$(printf '%s\n' "$line" | awk 'NF { count++ } END { print count + 0 }')" = 1 ] ||
  fail "expected exactly one checksum for $asset — aborting"
# The tools' own "OK" line is dropped for ours; a mismatch still fails.
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$tmp" && printf '%s\n' "$line" | sha256sum -c - >/dev/null) || fail "checksum mismatch for $asset — aborting"
elif command -v shasum >/dev/null 2>&1; then
  (cd "$tmp" && printf '%s\n' "$line" | shasum -a 256 -c - >/dev/null) || fail "checksum mismatch for $asset — aborting"
else
  fail "sha256sum or shasum is required — aborting"
fi
step "checksum verified"

tar -xzf "$tmp/$asset" -C "$tmp"

# The version it replaces, so an upgrade says so.
old=''
if [ -x "$bin_dir/drsh" ]; then
  old=$("$bin_dir/drsh" --version 2>/dev/null | awk '{ print $NF }') || old=''
fi

mkdir -p "$bin_dir"
mv "$tmp/drsh" "$bin_dir/drsh"
chmod +x "$bin_dir/drsh"
# The command was deep-research before it was renamed drsh. Left in place,
# that binary would stay on the old release, and its update cannot install
# the new one.
if [ -f "$bin_dir/deep-research" ]; then
  rm -f "$bin_dir/deep-research"
  step "removed $(tilde "$bin_dir")/deep-research ${dim}(the command is drsh now)${reset}"
fi

new=$("$bin_dir/drsh" --version 2>/dev/null | awk '{ print $NF }') || new=''
if [ -n "$old" ] && [ -n "$new" ] && [ "$old" != "$new" ]; then
  step "updated drsh ${dim}$old →${reset} ${bold}$new${reset} in $(tilde "$bin_dir")"
else
  step "installed drsh ${bold}${new}${reset} to $(tilde "$bin_dir")"
fi

case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *)
    case "${SHELL:-}" in
      */zsh) rc='~/.zshrc' ;;
      */fish) rc='' ;;
      *) rc='~/.bashrc' ;;
    esac
    echo >&2
    note "$(tilde "$bin_dir") is not on your PATH. Add it with:"
    if [ -z "$rc" ]; then
      printf '      %sfish_add_path %s%s\n' "$cyan" "$bin_dir" "$reset" >&2
    else
      # $HOME left unexpanded: the line is copied into a shell's rc file.
      dir=$bin_dir
      case "$dir" in "$HOME"/*) dir="\$HOME/${dir#"$HOME"/}" ;; esac
      printf '      %secho '"'"'export PATH="%s:$PATH"'"'"' >> %s%s\n' "$cyan" "$dir" "$rc" "$reset" >&2
    fi
    ;;
esac

if [ -n "$old" ]; then
  printf '\n  %sAll set.%s Your settings are kept.\n\n' "$bold" "$reset"
else
  printf '\n  %sGet started%s\n' "$bold" "$reset"
  printf '    %sdrsh setup%s                pick a model and a web search\n' "$cyan" "$reset"
  printf '    %sdrsh -p "your question"%s   research it\n' "$cyan" "$reset"
  printf '    %sdrsh doctor%s               check the setup if something is off\n\n' "$cyan" "$reset"
fi
