#!/bin/sh
# Installs the cc-fm player from the latest GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/code-akram/cc-fm-mod/main/install.sh | sh
#
# CC_FM_VERSION       a release to install instead of the latest (e.g. 0.0.1)
# CC_FM_INSTALL_DIR   where the binary goes (default: ~/.local/bin)
set -eu

repo=code-akram/cc-fm-mod
dir=${CC_FM_INSTALL_DIR:-$HOME/.local/bin}

die() { echo "cc-fm install: $*" >&2; exit 1; }

case $(uname -s) in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) die "unsupported system: $(uname -s) (macOS and Linux only)" ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "unsupported CPU: $(uname -m)" ;;
esac

if [ -n "${CC_FM_VERSION:-}" ]; then
  base="https://github.com/$repo/releases/download/v${CC_FM_VERSION#v}"
else
  base="https://github.com/$repo/releases/latest/download"
fi
archive="cc-fm_${os}_${arch}.tar.gz"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading $archive"
curl -fsSL "$base/$archive" -o "$tmp/$archive" || die "download failed: $base/$archive"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || die "download failed: $base/checksums.txt"

expected=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$expected" ] || die "no checksum for $archive"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
else
  actual=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
fi
[ "$expected" = "$actual" ] || die "checksum mismatch for $archive"

tar -xzf "$tmp/$archive" -C "$tmp" cc-fm
mkdir -p "$dir"
install -m 0755 "$tmp/cc-fm" "$dir/cc-fm"
echo "Installed $("$dir/cc-fm" version) to $dir/cc-fm"

case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "Note: $dir is not on your PATH; add it, or run $dir/cc-fm" ;;
esac

missing=""
for tool in ffmpeg yt-dlp; do
  command -v "$tool" >/dev/null 2>&1 || missing="$missing $tool"
done
if [ -n "$missing" ]; then
  echo "cc-fm also needs:$missing"
  if [ "$os" = darwin ]; then
    echo "  brew install$missing"
  else
    echo "  install them with your package manager (yt-dlp goes stale fast; prefer a recent one)"
  fi
fi

echo "Next: cc-fm doctor, then cc-fm serve --autoplay default"
echo "To keep it running at login: cc-fm service install"
