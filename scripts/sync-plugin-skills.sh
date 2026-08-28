#!/usr/bin/env bash
# Refreshes the Claude Code plugin's skills/ copy from the canonical
# .github/skills/ tree. plugin_skills_sync_test.go fails CI when the two
# trees drift; run this from the repository root after editing a skill.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

SRC=".github/skills"
DST="plugins/rhizome-mcp/skills"

rm -rf "$DST"
mkdir -p "$DST"
(cd "$SRC" && find . -type f ! -name '.DS_Store' -print0) |
  while IFS= read -r -d '' f; do
    mkdir -p "$DST/$(dirname "$f")"
    cp "$SRC/$f" "$DST/$f"
  done

echo "synced $SRC -> $DST"
