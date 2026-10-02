#!/bin/sh
# Writes the Homebrew formula for a release to stdout.
#
#   scripts/formula.sh <version> <checksums.txt>
#
# The formula installs the prebuilt binary, pulls in ffmpeg and yt-dlp, and
# lets `brew services start cc-fm` keep the player running at login.
set -eu

version=${1:?usage: formula.sh <version> <checksums.txt>}
checksums=${2:?usage: formula.sh <version> <checksums.txt>}
base="https://github.com/code-akram/cc-fm-mod/releases/download/v$version"

sha() {
  sum=$(awk -v f="cc-fm_$1.tar.gz" '$2 == f { print $1 }' "$checksums")
  [ -n "$sum" ] || { echo "formula.sh: no checksum for cc-fm_$1.tar.gz" >&2; exit 1; }
  echo "$sum"
}

cat <<EOF
# Written by scripts/formula.sh in code-akram/cc-fm-mod on each release.
class CcFm < Formula
  desc "Player for cc-fm: claude.fm with a live spectrum in Claude Code"
  homepage "https://github.com/code-akram/cc-fm-mod"
  version "$version"
  license "MIT"

  depends_on "ffmpeg"
  depends_on "yt-dlp"

  on_macos do
    on_arm do
      url "$base/cc-fm_darwin_arm64.tar.gz"
      sha256 "$(sha darwin_arm64)"
    end
    on_intel do
      url "$base/cc-fm_darwin_amd64.tar.gz"
      sha256 "$(sha darwin_amd64)"
    end
  end

  on_linux do
    on_arm do
      url "$base/cc-fm_linux_arm64.tar.gz"
      sha256 "$(sha linux_arm64)"
    end
    on_intel do
      url "$base/cc-fm_linux_amd64.tar.gz"
      sha256 "$(sha linux_amd64)"
    end
  end

  def install
    bin.install "cc-fm"
  end

  # Idle until /fm or \`cc-fm play\` starts something: nothing plays at login.
  service do
    run [opt_bin/"cc-fm", "serve"]
    environment_variables PATH: std_service_path_env
    keep_alive true
    log_path var/"log/cc-fm.log"
    error_log_path var/"log/cc-fm.log"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/cc-fm version")
  end
end
EOF
