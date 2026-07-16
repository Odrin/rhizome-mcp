# Issue lifecycle

## Types and structure

Issues are `epic`, `task`, or `bug`. Use parent relationships for decomposition. A `blocks` relation means the source blocks the target; `related_to` adds non-scheduling context and `duplicates` identifies equivalent work.

## Statuses

- `open`: known but not executable.
- `ready`: may be claimed when dependencies permit.
- `blocked`: explicitly paused and accompanied by a useful reason.
- `review`: completed work awaiting review.
- `done`: accepted terminal work.
- `cancelled`: intentionally abandoned terminal work.

Never write `in_progress`. It is an effective status derived from one active renewable lease. When the lease expires, the issue becomes available again if its stored state and blockers permit it.

Use `get_planning_graph` or claimability filters rather than inferring readiness from comments. Keep `blocks` graphs acyclic.

## Concurrency

Issue writes use optimistic concurrency:

1. Read the issue and retain `version`.
2. Send it as `expected_version`.
3. On conflict, refetch and reconcile all intervening changes.

Never retry a stale patch blindly. For bounded batches, validate the normalized plan and then apply it atomically.

## Knowledge and terminal state

Comments are append-only collaboration. Decisions are append-only durable choices and should supersede, not rewrite, prior decisions.

Implementation and review attempts must record verification and artifacts before changing terminal or review state. Archival hides obsolete records from ordinary reads without deleting history and also requires the current issue version.
