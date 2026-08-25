# Review workflow contract

## Relationship to workflow gates

A workflow policy's `review_approval` requirement (docs/02 §17) names a
purpose (e.g. `"security"`) that must have a matching immutable approval
record before `approve_review` -- or a work attempt completing straight to
`done` -- can succeed. That approval record is granted by the review request
described below, not a separate entity: a request declares the `purposes` it
covers (ISSUE-173), one issue can have at most one open-or-claimed request at
a time (see below), and a single request can cover several purposes at once
(for example `"implementation"` and `"security"`) rather than needing one
request per purpose. Creating or replacing a request resolves the target's
currently-active `review_approval` requirements and rejects a request whose
purposes do not cover all of them (`REVIEW_PURPOSE_REQUIRED`); approving the
request then grants one immutable, purpose-scoped approval row per purpose it
covers, bound to the request's target (issue version and event position).
Full contract: docs/02 §17.5, docs/03 §7.6.

## Review request

A review request is an append-only request to review one immutable target:

```text
review_request
  id
  issue_id
  target_issue_version
  target_event_id
  artifact_ids
  purposes
  status: open | claimed | approved | changes_requested | blocked | cancelled | superseded
  supersedes_id nullable
  created_at
  resolved_at nullable
```

The request stores an exact issue version and latest event position, plus a
bounded ordered list of artifact IDs and the purposes it covers (a unique
sorted list of 1-10 normalized keys, `[implementation]` by default). It does
not store a reviewer identity. Reviewer attribution is supplied by the
existing leased work attempt and its temporary agent session.

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
| open | claim with a stale target | superseded | claim fails with `STALE_REVIEW_TARGET`; no review attempt is bound |
| approved, changes_requested, blocked, cancelled, superseded | request re-review | open | creates a new request with a new exact target |

`claimed` is derived from its active review attempt and is not a persisted
general workflow status. Any path that ends the claiming review attempt
without it completing — lease expiry, `finish_attempt` with outcome `failed`
or `interrupted`, or an administrative force-release — returns the request
to `open`; if the target went stale while the attempt held it, the request is
resolved as `superseded` instead, so the next reviewer is never handed work
that can only end in `STALE_REVIEW_TARGET`. Only a completed attempt
(`approved`, `changes_requested`, or `blocked`) resolves the request
otherwise. No table stores `in_progress`.

## Operational guide: request, discover, claim, complete, follow-up, and re-request

Use the review workflow in this order when you need a durable review handoff:

1. Request: create a review request with the exact target issue version, latest event position, artifact IDs, and purposes you want to freeze (purposes default to `[implementation]`, and any purpose an active `review_approval` policy currently requires for this target must be included or the call fails with `REVIEW_PURPOSE_REQUIRED`). The request captures that immutable snapshot and remains open until it is claimed or superseded.
2. Discover: list or get review requests to find the request for the target you want to review. Review requests are discoverable from planning and work context, and a request that is still claimable is reported as `claimable`.
3. Claim: start a review attempt with `claim_issue` against the review issue. The attempt automatically binds the issue's open review request to the new attempt in the same transaction (no separate operation needed); if no open request exists, the attempt simply proceeds unbound. A claimed request is derived from the active review attempt; if the lease expires before completion, the request returns to `open` and can be claimed again.
4. Complete: finish the active review attempt with `finish_attempt` and an explicit review outcome of `approved`, `changes_requested`, or `blocked`. `approved` finishes the request and marks the issue `done`; `changes_requested` leaves the issue `ready` and records that follow-up is required; `blocked` marks the issue `blocked`.
5. Follow-up and re-request: `changes_requested` should create an explicit implementation follow-up linked to the request and preserve reviewer findings. When the follow-up is complete, create a fresh review request for the new target version/event (via `replace_review_request`, which inherits the predecessor's purposes unless the new request names different ones) and repeat the discover/claim/complete cycle.

Recovery examples:

- If a session disappears after claim, the review request returns to `open` when the lease expires. Re-discover the request and repeat the claim step with a fresh review attempt.
- If the review attempt is finished with outcome `failed` or `interrupted` (e.g. a handoff or context limit) instead of a review outcome, the request returns to `open` the same way — the caller does not need to wait for the lease to expire. Re-discover and re-claim.
- If the attempt is administratively force-released (a stuck or abandoned session, released via the CLI), the request likewise returns to `open` immediately rather than staying `claimed` against a session that will never return.
- If the implementation changed while the request was claimed, `finish_attempt` returns `STALE_REVIEW_TARGET` and the request becomes `superseded`. Create a new review request against the new target instead of reusing the stale one.
- If the implementation changed before anyone claimed the request, the request stops being reported as `claimable`; claiming it explicitly supersedes it and returns `STALE_REVIEW_TARGET`, and a `claim_issue` review attempt simply starts unbound. Replace the request with one that freezes the new target.
- If two agents race to claim the same request, one wins and the other gets `VERSION_CONFLICT` or `ACTIVE_ATTEMPT_EXISTS`. Re-discover the request and retry the claim with the new state.
- If a review request is created after an unbound attempt has already claimed the issue, `finish_attempt` with `review_outcome=approved` returns `REVIEW_REQUEST_REQUIRED` and does not mutate state. This ensures the new request is not silently orphaned; the caller must resolve or discover the request through the normal review flow (which most often means re-attempting the review with a fresh claim against the newly-bound request).

## Staleness and concurrency

Staleness is enforced at every point that could otherwise hand out or keep
alive a doomed request, not only at completion:

| Point | Behavior |
| --- | --- |
| create / replace | The target is compared with the issue inside the same write transaction. A request whose target already fails the comparison is rejected with `STALE_REVIEW_TARGET` and nothing is written — a request can never be born stale. |
| get / list / planning projections | `claimable` is `status == open` **and** the target still matches. A stale request is reported as not claimable while remaining `open`. |
| claim (`claim_review_request`) | A stale request is resolved as `superseded` and the call returns `STALE_REVIEW_TARGET`. The supersede is durable even though the call reports an error. |
| claim-time binding (`claim_issue` on a review issue) | A stale open request is not bound: the review attempt starts unbound and the request is left `open` for an explicit `replace_review_request`. Approving an unbound attempt while an unresolved request exists still fails with `REVIEW_REQUEST_REQUIRED`. |
| release (lease expiry, `failed`/`interrupted` finish, force release) | A claimed request whose target went stale is resolved as `superseded` rather than returned to `open`. |
| completion (`finish_attempt` with a review outcome) | Unchanged: the request is superseded and the call returns `STALE_REVIEW_TARGET`. |

Before review completion, the service checks the target issue version and
event position against current state. Any change that affects implementation
content, acceptance criteria, artifacts, status, or a new implementation
attempt makes the request stale. Priority-only changes do not. A stale request
cannot approve, request changes, or block; it transitions to `superseded` and
returns `STALE_REVIEW_TARGET`.

The event-position check asks a single question: **did the reviewed issue's own
work change after `target_event_id`?** Three properties follow from that
wording, and each one matters:

- **Issue-scoped.** A review target freezes one issue's work, so only events
  recorded against that issue count. Activity on other issues -- another agent
  claiming work, a comment filed elsewhere -- never supersedes this request.
  A project-global comparison would make every in-flight review stale as soon
  as anyone touched anything, which is unusable once claim-time binding is
  routine.
- **Excludes review-sourced events, `attempt_started`, reservation events,
  and every event emitted by a review attempt.** The review's own lifecycle (its request, and the claim that
  `claim_issue` auto-binds to its `attempt_started` row) is the workflow
  progressing, not the reviewed work changing -- and so is a reservation
  acquired or released against the reviewed issue, since reservations track
  resource ownership among concurrent attempts, not the issue's content.
  A review attempt's own finish, interruption, force-release, or expiry is
  likewise the review workflow's footprint, not a change to the work under
  review; counting it made a request permanently stale after its first
  abandoned review, which stayed invisible while only completion enforced
  staleness. Without this, claim-time reservation acquisition would invalidate a
  reviewer's own review request the moment they claimed it.
- **Asks "anything since?", not "does this still equal the maximum?"**
  `target_event_id` is a client-supplied position taken from `latest_event_id`,
  which read tools report as an unfiltered log cursor. Comparing it for
  equality against any filtered maximum compares two different quantities, and
  marked requests stale the moment they were created whenever the newest event
  happened to be one of the excluded kinds.

`latest_event_id` itself is unchanged and remains the true unfiltered maximum
across the log, so it stays usable as a `get_changes` cursor. It is also what
`finish_attempt`'s change-acknowledgment gate compares against, which is why an
agent can satisfy that gate by echoing back the value it just read.

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
