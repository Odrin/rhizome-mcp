#!/usr/bin/env bash
# Re-records every demo asset in site/assets/demo/. Run from the repository
# root: bash demo/record.sh
# Requires: vhs (brings ttyd + ffmpeg), gifsicle, jq, curl, go.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

MAX_BYTES=$((2 * 1024 * 1024))

for dep in vhs gifsicle jq curl go; do
  command -v "$dep" >/dev/null || { echo "missing dependency: $dep" >&2; exit 1; }
done

echo "building rhizome-mcp..."
CGO_ENABLED=0 go build -o rhizome-mcp .

mkdir -p site/assets/demo

for tape in demo/tapes/01-lease-expiry.tape demo/tapes/02-reservation-conflict.tape demo/tapes/03-board.tape; do
  echo "recording $tape..."
  vhs "$tape"
done

# The board tape leaves its server running (it must not end with Hide/Show
# after Screenshot); stop it, and drop the GIF byproduct.
bash demo/03-seed-board.sh cleanup
rm -f site/assets/demo/board-recording.gif

echo "writing board.html snapshot..."
bash demo/03-seed-board.sh setup >/dev/null
bash demo/03-seed-board.sh html site/assets/demo/board.html >/dev/null
bash demo/03-seed-board.sh cleanup

for gif in site/assets/demo/*.gif; do
  gifsicle -O3 --lossy=80 -o "$gif.opt" "$gif" && mv "$gif.opt" "$gif"
  size=$(wc -c <"$gif")
  echo "$gif: $size bytes"
  if [ "$size" -gt "$MAX_BYTES" ]; then
    echo "ERROR: $gif exceeds $MAX_BYTES bytes" >&2
    exit 1
  fi
done

echo "done. re-run 'bash demo/01-lease-expiry.sh all' and 'bash demo/02-reservation-conflict.sh all' as smoke tests if the server changed."
