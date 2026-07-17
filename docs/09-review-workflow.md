# Review workflow contract

## Review request

A review request is an append-only request to review one immutable target:

```text
review_request
  id
  issue_id
  target_issue_version
  target_event_id
  artifact_ids
  status: open | claimed | approved | changes_requested | blocked | cancelled | superseded
  supersedes_id nullable
  created_at
  resolved_at nullable
```

The request stores an exact issue version and latest event position, plus a
bounded ordered list of artifact IDs. It does not store a reviewer identity.
Reviewer attribution is supplied by the existing leased work attempt and its
temporary agent session.

Only one `open` or `claimed` request may exist for the same
`issue_id`/`target_issue_version` pair. A duplicate create is idempotent only
when its request content is identical; otherwise it fails with
`REVIEW_ALREADY_EXISTS`.

## State transitions

| Current | Action | Next | Effect |
| --- | --- | --- | --- |
| none | request review | open | captures target version/event and artifacts |
| open | claim | claimed | creates one `review` work attempt |
| claimed | approve | approved | reviewed issue becomes `done` |
| claimed | request changes | changes_requested | reviewed issue becomes `ready`; creates linked follow-up |
| claimed | block | blocked | reviewed issue becomes `blocked` with reason |
| open, claimed | cancel | cancelled | no issue status change |
| open, claimed | target becomes stale | superseded | request is no longer claimable |
| approved, changes_requested, blocked, cancelled, superseded | request re-review | open | creates a new request with a new exact target |

`claimed` is derived from its active review attempt and is not a persisted
general workflow status. Attempt expiry returns a claimed request to `open`
unless it has become stale. No table stores `in_progress`.

## Staleness and concurrency

Before review completion, the service compares the target issue version and
event position with current values. Any change that affects implementation
content, acceptance criteria, artifacts, status, or a new implementation
attempt makes the request stale. Priority-only changes do not. A stale request
cannot approve, request changes, or block; it transitions to `superseded` and
returns `STALE_REVIEW_TARGET`.

Creation, claiming, completion, cancellation, and supersession use optimistic
request version checks and short write transactions. The database enforces one
active review attempt per request and one active request per target. Events are
append-only: `review_requested`, `review_claimed`, `review_approved`,
`review_changes_requested`, `review_blocked`, `review_cancelled`, and
`review_superseded`.

## Follow-up and re-review

Changes requested creates an explicit implementation follow-up linked to the
request and preserving reviewer findings. Completion of that follow-up does
not mutate the old request; it creates a new review request against the new
target. This keeps review history auditable and makes re-review discoverable
in planning and work context.
