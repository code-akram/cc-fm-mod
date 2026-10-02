#!/bin/sh
# Pushes the release's formula to code-akram/homebrew-tap. Run by the release
# workflow after GoReleaser, with write access to the tap from either
# $TAP_TOKEN (a token allowed to push to the tap) or $TAP_DEPLOY_KEY (a
# deploy key with write access).
#
#   scripts/update-tap.sh <version> <checksums.txt>
set -eu

version=${1:?usage: update-tap.sh <version> <checksums.txt>}
checksums=${2:?usage: update-tap.sh <version> <checksums.txt>}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

if [ -n "${TAP_TOKEN:-}" ]; then
  remote="https://x-access-token:$TAP_TOKEN@github.com/code-akram/homebrew-tap.git"
elif [ -n "${TAP_DEPLOY_KEY:-}" ]; then
  printf '%s\n' "$TAP_DEPLOY_KEY" > "$work/key"
  chmod 600 "$work/key"
  export GIT_SSH_COMMAND="ssh -i $work/key -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new"
  remote="git@github.com:code-akram/homebrew-tap.git"
else
  echo "update-tap.sh: set TAP_TOKEN or TAP_DEPLOY_KEY" >&2
  exit 1
fi

git clone --depth 1 "$remote" "$work/tap"
mkdir -p "$work/tap/Formula"
"$(dirname "$0")/formula.sh" "$version" "$checksums" > "$work/tap/Formula/cc-fm.rb"

cd "$work/tap"
git add Formula/cc-fm.rb
if git diff --cached --quiet; then
  echo "update-tap.sh: formula already at $version"
  exit 0
fi
git -c user.name="cc-fm release" -c user.email="noreply@github.com" \
  commit -m "cc-fm $version"
git push origin HEAD
