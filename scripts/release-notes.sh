#!/usr/bin/env bash
# Prints the CHANGELOG.md entry for a version.
# Usage: scripts/release-notes.sh vX.Y.Z
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

notes="$(awk -v heading="[${1#v}]" '
	/^## / { found = ($2 == heading); next }
	found && (started || NF) { started = 1; print }
' CHANGELOG.md)"

if [[ -z "${notes//[[:space:]]/}" ]]; then
	echo "release-notes: no CHANGELOG.md entry for $1" >&2
	exit 1
fi
printf '%s\n' "$notes"
