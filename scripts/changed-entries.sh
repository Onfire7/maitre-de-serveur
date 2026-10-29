#!/usr/bin/env bash
# Prints the ids of the Catalog entries a change touches, one per line: those
# whose folder under catalog/entries differs between the superproject's BASE
# commit and HEAD. The Catalog is a submodule, so this compares the commits it
# pointed at. The boot test installs these (plan C8's third check).
# Usage: scripts/changed-entries.sh <base commit>
set -euo pipefail
BASE=${1:?base commit}
old=$(git ls-tree "$BASE" catalog | awk '{print $3}')
new=$(git -C catalog rev-parse HEAD)
if [ -z "$old" ]; then
  # The Catalog is new in this change: every entry is.
  ls catalog/entries
  exit 0
fi
[ "$old" = "$new" ] && exit 0
# A shallow submodule checkout may not have the old commit.
git -C catalog cat-file -e "$old^{commit}" 2>/dev/null || git -C catalog fetch --quiet --depth=1 origin "$old"
git -C catalog diff --name-only "$old" "$new" -- entries | awk -F/ 'NF > 2 {print $2}' | sort -u |
  while read -r id; do [ -d "catalog/entries/$id" ] && echo "$id"; done
