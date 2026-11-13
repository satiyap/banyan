#!/bin/sh
# Release the desktop app: sh scripts/release.sh 0.2.0  (or: pnpm release 0.2.0)
#
# Sets the version in package.json, commits it, tags v0.2.0 and pushes both.
# The tag is what starts .github/workflows/release.yml, which builds the disk
# image and publishes the GitHub release.
set -eu

version="${1:-}"
echo "$version" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$' || { echo "usage: sh scripts/release.sh 0.2.0" >&2; exit 1; }
cd "$(dirname "$0")/.."

[ "$(git rev-parse --abbrev-ref HEAD)" = main ] || { echo "Releases are cut from main." >&2; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo "Commit or stash your changes first." >&2; exit 1; }
git fetch --quiet origin main
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || { echo "main is not level with origin/main." >&2; exit 1; }
if git rev-parse -q --verify "refs/tags/v$version" >/dev/null; then echo "v$version already exists." >&2; exit 1; fi

node -e "
  const fs = require('fs'); const p = JSON.parse(fs.readFileSync('package.json', 'utf8'));
  p.version = '$version'; fs.writeFileSync('package.json', JSON.stringify(p, null, 2) + '\n');
"
git commit --quiet -am "Release $version"
git tag "v$version"
git push --quiet origin main "v$version"
echo "Pushed v$version. The release is being built: gh run watch"
