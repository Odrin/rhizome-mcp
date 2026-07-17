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

## Operational guide: request, discover, claim, complete, follow-up, and re-request

Use the review workflow in this order when you need a durable review handoff:

1. Request: create a review request with the exact target issue version, latest event position, and artifact IDs you want to freeze. The request captures that immutable snapshot and remains open until it is claimed or superseded.
2. Discover: list or get review requests to find the request for the target you want to review. Review requests are discoverable from planning and work context, and a request that is still claimable is reported as `claimable`.
3. Claim: start a review attempt with `claim_issue` against the review issue, then attach that active review attempt to the request. A claimed request is derived from the active review attempt; if the lease expires before completion, the request returns to `open` and can be claimed again.
4. Complete: finish the active review attempt with `finish_attempt` and an explicit review outcome of `approved`, `changes_requested`, or `blocked`. `approved` finishes the request and marks the issue `done`; `changes_requested` leaves the issue `ready` and records that follow-up is required; `blocked` marks the issue `blocked`.
5. Follow-up and re-request: `changes_requested` should create an explicit implementation follow-up linked to the request and preserve reviewer findings. When the follow-up is complete, create a fresh review request for the new target version/event and repeat the discover/claim/complete cycle.

Recovery examples:

- If a session disappears after claim, the review request returns to `open` when the lease expires. Re-discover the request and repeat the claim step with a fresh review attempt.
- If the implementation changed while the request was claimed, `finish_attempt` returns `STALE_REVIEW_TARGET` and the request becomes `superseded`. Create a new review request against the new target instead of reusing the stale one.
- If two agents race to claim the same request, one wins and the other gets `VERSION_CONFLICT` or `ACTIVE_ATTEMPT_EXISTS`. Re-discover the request and retry the claim with the new state.

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
