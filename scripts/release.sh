#!/usr/bin/env bash
# Cuts a release: adds the new version to CHANGELOG.md, commits it and tags it.
# Usage: scripts/release.sh [vX.Y.Z]
set -euo pipefail

die() {
	echo "release: $*" >&2
	exit 1
}

cd "$(git rev-parse --show-toplevel)"

[[ "$(git branch --show-current)" == master ]] || die "not on master"
[[ -z "$(git status --porcelain)" ]] || die "working tree is not clean"
git fetch --quiet --tags origin master
[[ "$(git rev-parse HEAD)" == "$(git rev-parse origin/master)" ]] || die "master is out of sync with origin"

version="${1:-$(git cliff --bumped-version)}"
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || die "$version is not a vX.Y.Z version"
if git rev-parse --quiet --verify "refs/tags/$version" >/dev/null; then
	die "$version is already tagged; nothing new to release, or pass a version"
fi

undo() {
	git checkout --quiet -- CHANGELOG.md 2>/dev/null || rm -f CHANGELOG.md
}
trap 'undo; exit 130' INT

if [[ -f CHANGELOG.md ]]; then
	git cliff --unreleased --tag "$version" --prepend CHANGELOG.md
else
	git cliff --unreleased --tag "$version" --output CHANGELOG.md
fi

echo
scripts/release-notes.sh "$version"
echo
read -r -p "Edit CHANGELOG.md if you like, then press enter to commit and tag $version (ctrl-c to abort) "

# The release workflow reads this entry back, so it has to survive the edit.
scripts/release-notes.sh "$version" >/dev/null
git add CHANGELOG.md
git commit --quiet -m "chore: release $version"
git tag -a "$version" -m "$version"

echo "Tagged $version. Publish it with:"
echo "  git push --atomic origin master $version"
