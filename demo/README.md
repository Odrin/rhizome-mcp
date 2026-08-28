# Demo assets

Scripted, reproducible recordings of the guarantees the README claims. The
GIFs and the board screenshot in `site/assets/demo/` are generated from the
tapes in `tapes/`; nothing is staged by hand.

## What each scenario proves

| Asset | Scenario | Guarantee on screen |
| --- | --- | --- |
| `lease-expiry.gif` | [01-lease-expiry.sh](01-lease-expiry.sh) | Claims are renewable expiring leases; a second claim fails with `ACTIVE_ATTEMPT_EXISTS`; `in_progress` is derived from the live lease; after the lease expires, another session claims the issue and resumes from the checkpoint. |
| `reservation-conflict.gif` | [02-reservation-conflict.sh](02-reservation-conflict.sh) | Resource reservations are leased with the claim, all-or-nothing; an overlapping reservation fails the whole claim atomically with `RESOURCE_RESERVATION_CONFLICT`, naming the holder and its lease expiry. |
| `board.png` | [03-seed-board.sh](03-seed-board.sh) | `rhizome-mcp board` on a busy project: live leases, a reservation, blocked issues with reasons — no server, no login. |
| `review-superseded.gif` | [04-review-superseded.sh](04-review-superseded.sh) | A review request pins an exact issue version and event position; after the issue changes, approval is refused with `REVIEW_REQUEST_REQUIRED` and the request can only be superseded into a successor pinned to the new target. |

Each driver talks to a real `rhizome-mcp serve --http-address 127.0.0.1:0`
process over the stateless HTTP transport (docs/08) with curl + jq, against a
fresh temporary project. The drivers double as smoke tests of that transport
contract.

## Re-recording

```bash
bash demo/record.sh
```

Runs from the repository root: builds the binary, records every tape with
VHS, optimizes with gifsicle, and fails if any GIF exceeds 2 MiB.

Smoke-test the scenarios without recording (scenario A takes ~75 s — it
really waits for the lease to expire):

```bash
bash demo/01-lease-expiry.sh all
bash demo/02-reservation-conflict.sh all
bash demo/03-seed-board.sh all
bash demo/04-review-superseded.sh all
```

The 60-second lease wait is hidden in the tape with `Hide`/`Show`; everything
else in the GIFs runs in real time.

## Tool versions at last recording (2026-08-28)

- vhs v0.11.0 (Homebrew), with bundled ttyd + ffmpeg
- gifsicle 1.96
- macOS bash 3.2 compatible (arguments are built with `jq -nc`; see the
  comment in each driver)

If VHS or the server output changes shape, re-run the smoke tests first, then
re-record.
