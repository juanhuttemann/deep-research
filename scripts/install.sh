#!/bin/sh
# deep-research installer — Linux, macOS, WSL2, Termux.
# Downloads the latest release binary from GitHub into ~/.local/bin
# (/usr/local/bin as root, $PREFIX/bin on Termux).
set -eu

repo=juanhuttemann/deep-research

os=$(uname -s)
case "$os" in
  Linux*) os=linux ;;
  Darwin*) os=darwin ;;
  *) echo "unsupported OS: $os (on Windows use install.ps1)" >&2; exit 1 ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac

bin_dir=${DEEP_RESEARCH_BIN_DIR:-$HOME/.local/bin}
if [ "$(id -u)" = 0 ]; then bin_dir=/usr/local/bin; fi
if [ -n "${TERMUX_VERSION:-}" ]; then bin_dir=${PREFIX:?}/bin; fi

asset="deep-research_${os}_${arch}.tar.gz"
url="https://github.com/$repo/releases/latest/download/$asset"
sum_url="https://github.com/$repo/releases/latest/download/deep-research_checksums.txt"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading $url"
# Keep the release filename: sha256sum -c resolves it relative to $tmp.
curl -fsSL "$url" -o "$tmp/$asset"

# A release without a matching checksum is not safe to install.
curl -fsSL "$sum_url" -o "$tmp/checksums.txt"
line=$(awk -v asset="$asset" '$2 == asset { print }' "$tmp/checksums.txt")
[ "$(printf '%s\n' "$line" | awk 'NF { count++ } END { print count + 0 }')" = 1 ] || {
  echo "expected exactly one checksum for $asset — aborting" >&2
  exit 1
}
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$tmp" && printf '%s\n' "$line" | sha256sum -c -)
elif command -v shasum >/dev/null 2>&1; then
  (cd "$tmp" && printf '%s\n' "$line" | shasum -a 256 -c -)
else
  echo "sha256sum or shasum is required — aborting" >&2
  exit 1
fi

tar -xzf "$tmp/$asset" -C "$tmp"

mkdir -p "$bin_dir"
mv "$tmp/deep-research" "$bin_dir/deep-research"
chmod +x "$bin_dir/deep-research"

case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *) echo "note: $bin_dir is not on your PATH" >&2 ;;
esac

echo "installed $bin_dir/deep-research"
echo "next: deep-research init, then put your API key in .env"
