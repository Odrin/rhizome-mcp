# Domain model

## 1. General conventions

- All internal IDs are ULIDs stored as text.
- Only issues have a human-readable identifier.
- Issue display IDs use the format `ISSUE-N`.
- All timestamps are UTC and formatted consistently.
- Long text fields contain Markdown.
- Main tables use SQLite `STRICT`.
- Enums are protected by database `CHECK` constraints.
- Current state is stored in entity tables.
- Historical changes are written to an append-only event table.
- Physical deletion is avoided.

## 2. Project

```text
Project
  id                    ULID
  name                  string nullable
  instructions          markdown nullable
  next_issue_number     integer
  created_at            timestamp
  updated_at            timestamp
```

Rules:

- There is one project row per project database.
- `id` matches `.agent-tracker.json`.
- `next_issue_number` is incremented atomically.
- Issue numbers are never reused.
- Project instructions are not included in agent context unless requested.

## 3. Issue

```text
Issue
  id                      ULID
  sequence_no             integer
  type                    epic | task | bug
  title                   string
  description             markdown nullable
  acceptance_criteria     markdown nullable
  status                  open | ready | blocked | review | done | cancelled
  priority                low | medium | high | critical
  parent_id               ULID nullable
  blocked_reason          string nullable
  version                 integer
  created_by_session_id   ULID nullable
  created_at              timestamp
  updated_at              timestamp
  closed_at               timestamp nullable
  archived_at             timestamp nullable
  archived_by_session_id  ULID nullable
```

External identity:

```json
{
  "id": "01J...",
  "display_id": "ISSUE-42"
}
```

### 3.1. Type rules

- An `epic` is a grouping issue and is not directly executable.
- An epic cannot have a parent.
- A `task` or `bug` may have one epic parent.
- Nested epics are not supported.
- Parent membership is stored in `parent_id`, not as a generic relation.
- Retyping an epic away from `epic`, or archiving an epic, is rejected while it still has non-archived children (`INVALID_EPIC_PARENT`, detail code `HAS_CHILDREN`); children that are already archived do not block either operation.

### 3.2. Statuses

Stored statuses:

```text
open
ready
blocked
review
done
cancelled
```

`in_progress` is not stored. It is an effective status derived from an active work attempt.

### 3.3. Stored status semantics

- `open`: created but not ready to execute.
- `ready`: available for a work attempt if not otherwise blocked.
- `blocked`: manually blocked by an external condition; `blocked_reason` is required.
- `review`: implementation completed and available for a review attempt.
- `done`: completed.
- `cancelled`: no longer required.

### 3.4. Effective and computed fields

```text
display_id
effective_status
is_terminal
is_blocked
is_claimable
unresolved_blocker_count
active_attempt_id
attempt_count
consecutive_failure_count
```

Rules:

```text
effective_status = in_progress
  when an active work attempt exists

effective_status = stored status
  otherwise
```

An issue is blocked when:

- stored status is `blocked`; or
- at least one unresolved issue has a `blocks` relation targeting it.

An issue is claimable for work when:

- type is `task` or `bug`;
- stored status is `ready`;
- it is not archived;
- no active attempt exists;
- no unresolved blocker exists.

An issue is claimable for review when the same conditions hold except stored status is `review`.

### 3.5. Status transitions

```text
open      -> ready | cancelled
ready     -> blocked | review | done | cancelled
blocked   -> ready | cancelled
review    -> ready | blocked | done | cancelled
done      -> ready
cancelled -> open
```

A transition to `blocked` requires `blocked_reason`.

When leaving `blocked`, the current `blocked_reason` is cleared, while history remains in events.

This table describes every stored-status transition the system ever
performs, regardless of mechanism. It does not mean every mechanism can
perform every listed transition: `-> review` and `-> done` are reachable
only through the gated attempt lifecycle (`claim_issue`/`finish_attempt`),
never through a direct `update_issue` patch (§17.1).

### 3.6. Versioning

`version` is incremented for every mutation of the issue row.

Mutations require `expected_version` when they alter:

- title;
- description;
- acceptance criteria;
- type;
- priority;
- stored status;
- parent;
- archive state;
- labels through the issue patch operation.

Append-only operations do not require issue version:

- comments;
- new decisions;
- attempt notes;
- checkpoints;
- events.

## 4. AgentSession

```text
AgentSession
  id                 ULID
  handle_hash        bytes unique
  client_name        string
  client_version     string nullable
  agent_label        string nullable
  model              string nullable
  instance_key       string nullable
  started_at         timestamp
  last_seen_at       timestamp
  ended_at           timestamp nullable
```

Purpose:

- audit which client performed an action;
- display useful source metadata;
- associate attempts and events with an explicitly created agent session.

Rules:

- A client creates a session explicitly and receives an opaque bearer handle
  once. The raw handle is never persisted; only its fixed-length hash is stored.
- A session handle is independent of an MCP connection, `Mcp-Session-Id`, and
  SDK session identity. It can be carried by either supported MCP protocol era
  and can survive client reconnects or server process restarts.
- Clients explicitly end a session. Connection close, HTTP `DELETE`, process
  exit, and a stale session do not end it automatically. A stale active session
  remains valid for recovery and is retained for audit history.
- A concurrent use of one active handle is valid. SQLite write serialization and
  monotonic timestamps make concurrent touches deterministic; ending wins for a
  call whose session lookup observes an ended row.
- `agent_label` is an arbitrary non-unique string.
- `instance_key` is optional and advisory.
- Neither field is used for ownership or security.
- Old sessions are retained.
- `active`, `stale` and `ended` are computed states.
- `last_seen_at` advances only on a tool call classified as mutating
  (`readOnlyHint: false` in `docs/03-mcp-tools.md` section 4). A read-only
  tool call never durably writes `last_seen_at`, so it stays true to its
  advertised no-write contract; activity tracking is therefore correlated
  with actual project mutations, not with every call including reads.
- A call that omits a handle remains compatible and persists NULL attribution.
  A supplied handle must resolve to an active session before any business write
  starts; an invalid handle causes no partial project or audit writes.

There is no permanent `Agent` entity in the first version.

## 5. WorkAttempt

```text
WorkAttempt
  id                        ULID
  issue_id                  ULID
  session_id                ULID nullable
  agent_label               string nullable
  kind                      work | review
  status                    active | completed | failed |
                            interrupted | expired | cancelled
  issue_version_at_start    integer
  context_event_id_at_start integer
  lease_token_hash          bytes
  lease_expires_at          timestamp
  started_at                timestamp
  last_heartbeat_at         timestamp
  finished_at               timestamp nullable
  result_summary            markdown nullable
  next_steps_json           JSON nullable
  verification_json         JSON nullable
  failure_reason_code       string nullable
  interruption_reason_code  string nullable
  reason_details            string nullable
```

### 5.1. Ownership

When an issue is claimed, the server returns:

```text
attempt_id
lease_token
lease_expires_at
```

The raw lease token is returned once. Only a hash is stored: neither
`work_attempts` (which stores `lease_token_hash`) nor `idempotency_records`
(section 13) ever persists the raw value. If `claim_issue` is retried with
the same `idempotency_key` while the attempt is still active, the server
rotates the lease and returns a freshly generated token rather than
replaying the original — the previous token stops working the moment the
retry succeeds. If the attempt is no longer active, the retry fails with
`ATTEMPT_NOT_ACTIVE` instead of fabricating a lease for a finished attempt.

Operations on an active attempt require:

```text
attempt_id + lease_token
```

A new MCP session can continue an attempt if it retained the token.

### 5.2. Lease behavior

- An agent periodically renews the lease.
- If the lease expires, the attempt becomes `expired`.
- Expiry removes the active lock.
- Expiry does not rewrite the stored issue status.
- The issue becomes claimable again only if its stored status and blockers permit it.
- An expired attempt cannot be resumed.
- A forced administrative release is a CLI operation, not a normal MCP operation.

### 5.3. Attempt kind

- Claiming a `ready` issue creates `kind = work`.
- Claiming a `review` issue creates `kind = review`.

### 5.4. Attempt outcomes

Work attempt target statuses:

```text
done
review
ready
blocked
```

Completing work with `target_issue_status: ready` while the issue is already `ready` succeeds as a no-op: the attempt completes and the issue returns to the queue unchanged (version bumps, blocked_reason clears) rather than being rejected as an invalid transition.

Review outcomes:

```text
approved           -> done
changes_requested  -> ready
blocked            -> blocked
```

### 5.5. Failure reason codes

```text
implementation_error
environment_error
missing_dependency
invalid_requirements
tests_failed
context_lost
timeout
other
```

### 5.6. Interruption reason codes

```text
handoff
user_request
context_limit
client_shutdown
environment_change
other
```

## 6. AttemptNote

```text
AttemptNote
  id               ULID
  attempt_id       ULID
  kind             progress | finding | warning | checkpoint
  content          markdown
  next_steps_json  JSON nullable
  important        boolean
  created_at       timestamp
```

Semantics:

- `progress`: ordinary progress update.
- `finding`: notable technical finding.
- `warning`: risk or problem.
- `checkpoint`: restartable summary.

The most recent checkpoint is preferred over the full note history when building work context.

## 7. Comment

```text
Comment
  id                    ULID
  issue_id              ULID
  content               markdown
  created_by_session_id ULID nullable
  author_label          string nullable
  created_at            timestamp
  edited_at             timestamp nullable
```

Comments are communication, not durable decisions or execution checkpoints.

For the first version, comments are effectively append-only through MCP.

## 8. Decision

```text
Decision
  id                    ULID
  issue_id              ULID nullable
  title                 string
  summary               string
  content               markdown
  status                active | superseded | rejected
  supersedes_id         ULID nullable
  created_by_session_id ULID nullable
  created_at            timestamp
```

Rules:

- A decision may be project-level or issue-level.
- `summary` is required and used in compact context.
- Decisions are not deleted.
- Creating a decision with `supersedes_id` atomically marks the old decision `superseded`.

## 9. IssueRelation

```text
IssueRelation
  id                    ULID
  source_issue_id       ULID
  target_issue_id       ULID
  type                  blocks | related_to | duplicates
  created_by_session_id ULID nullable
  created_at            timestamp
```

Canonical semantics:

```text
A blocks B
A duplicates B
A related_to B
```

Derived reverse views:

```text
B blocked_by A
B duplicated_by A
B related_to A
```

Rules:

- Self-relations are forbidden.
- Relations are project-local.
- Duplicate relations are forbidden.
- `related_to` is symmetric and stored once in canonical order.
- Cycles in `blocks` are forbidden.
- Epic membership is not a relation.

## 10. Label

```text
Label
  id          ULID
  name        string
  description string nullable
  created_at  timestamp
```

```text
IssueLabel
  issue_id
  label_id
```

Rules:

- Label names are unique case-insensitively.
- Labels do not alter workflow.
- Tools may create missing labels when explicitly allowed.

## 11. Artifact

```text
Artifact
  id          ULID
  issue_id    ULID
  attempt_id  ULID nullable
  type        file | directory | url | commit | branch |
              pull_request | log | other
  uri         string
  title       string nullable
  metadata    JSON nullable
  created_at  timestamp
```

Rules:

- Binary content is not stored.
- Repository file paths should be relative to project root.
- Unsafe path traversal is rejected.
- Artifacts may be attached to checkpoints or final attempt results.

## 12. IssueEvent

```text
IssueEvent
  id             integer monotonic
  issue_id       ULID nullable
  event_type     string
  session_id     ULID nullable
  attempt_id     ULID nullable
  payload        JSON
  created_at     timestamp
```

Typical event types:

```text
issue_created
issue_updated
issue_archived
status_changed
labels_changed
relation_added
relation_removed
comment_added
decision_recorded
attempt_started
attempt_completed
attempt_failed
attempt_interrupted
attempt_expired
checkpoint_saved
```

Heartbeat renewals do not need separate events.

Events provide:

- audit history;
- delta synchronization;
- context-change detection;
- debugging.

## 13. IdempotencyRecord

```text
IdempotencyRecord
  idempotency_key string
  operation       string
  request_hash    bytes
  response_json   JSON
  created_at      timestamp
```

Unique constraint:

```text
(operation, idempotency_key)
```

Rules:

- Same key and same request return the saved result.
- Same key and different request return `IDEMPOTENCY_CONFLICT`.
- Records are not automatically deleted in the first version.
- `response_json` never contains a raw secret. `claim_issue`'s stored
  response omits the lease token entirely (see section 5.1); a replay
  rotates the lease and returns a fresh token computed at replay time,
  never a persisted one.

## 14. SearchIndex

SQLite FTS5 projection over:

- issue title and description;
- comments;
- decision title, summary and content;
- attempt notes.

The index is derived and rebuildable.

It is not a source of truth.

## 15. Repeated attempt failures

The server computes consecutive `failed` and `expired` attempts.

After a configurable internal threshold, responses include:

```text
REPEATED_ATTEMPT_FAILURES
```

The server does not automatically block the issue.

## 16. Completion-time consistency checks

Before finishing an attempt, the server checks:

- attempt is active;
- lease is valid;
- token is valid;
- issue is not archived;
- issue is not cancelled;
- unresolved blockers were not added;
- issue version changes are acknowledged when required.

Changes that normally produce warnings:

- priority;
- labels;
- title;
- parent epic.

Changes that require refreshed context and explicit acknowledgment:

- description;
- acceptance criteria;
- stored status;
- newly added blockers;
- manual blocking.

Acknowledgment contains the current issue version and latest event ID.

## 17. Workflow policy and gate evaluation

Workflow policies add project-configurable quality gates without introducing
custom statuses or a custom workflow engine. Stored issue statuses remain
exactly `open`, `ready`, `blocked`, `review`, `done`, `cancelled` (§3.2);
`in_progress` remains derived (§3.3). "Review is optional" (docs/01 §7)
remains the default; a policy that adds a `review_approval` requirement makes
review mandatory only for the issues it matches.

### 17.1. Enforcement points

Gates are evaluated at exactly four fixed points, each corresponding to one
existing lease-authenticated transition:

```text
claim_work                claim_issue on a task or bug
complete_work_to_review   finish_attempt(kind=work, outcome=completed, target_issue_status=review)
complete_work_to_done     finish_attempt(kind=work, outcome=completed, target_issue_status=done)
approve_review            finish_attempt(kind=review, review_outcome=approved)
```

No other point evaluates gates, and no additional enforcement point exists.
Today, `update_issue` can drive a direct `ready -> review`, `ready -> done`,
or `review -> done` transition on its own, gated only by `CanTransition`
(§3.5) and never by an attempt lifecycle -- this is the one path that could
otherwise bypass gate evaluation entirely, since it needs neither a claimed
attempt nor a review target. This contract changes that: `update_issue` may
still set stored status directly to `open`, `ready`, `blocked`, or
`cancelled`, but a patch whose target is `review` or `done` must now be
rejected with `INVALID_STATUS_TRANSITION` -- the same error code
`update_issue` already returns for every other transition it does not
support, now also covering these two. `review -> done` requires going
through `approve_review`.

**This guard is scoped to executable issue types (task and bug).** Its whole
justification is that a gated status must be earned through a work attempt,
which presupposes the issue can hold one. An epic cannot: `EvaluateClaim`
rejects a claim on any non-executable type with `issue type is not executable`,
so for an epic the guard forbade a route that does not exist, and the two rules
together left a finished epic with no path to a terminal status at all
(ISSUE-224). For a non-executable type, `update_issue` may therefore patch
status directly to `done`. A direct patch to `review` stays rejected for every
type, including epics: `review` means "inspect this attempt's result", and a
type that cannot hold an attempt has nothing to inspect.

The patch path additionally permits `open -> done` for non-executable types
only. `CanTransition` refuses it, correctly, because for executable work
`ready` means "queued for an attempt" and finishing without ever being queued
is incoherent. An epic is never queued, and forcing `open -> ready -> done`
would park it in `ready` -- a status a non-executable type can never be claimed
out of. `CanTransition` itself is unchanged, since `finish_attempt` and other
direct writes share it; the allowance lives only in the patch path.

An epic already sitting in `ready` (a state `apply_issue_plan` and older data
both produce) is not rejected and is not a trap: `ready -> done` is already a
valid transition, and with the guard scoped it now succeeds.

Closing an epic does **not** verify that its children are terminal. That is
deliberate -- an epic may legitimately close with a cancelled or descoped child
-- so confirming a child set is complete remains the caller's responsibility.

`claim_work`, `complete_work_to_review`, `complete_work_to_done`, and
`approve_review` are not four separate functions in the current
implementation: `claim_issue` is one call chain (`AttemptRepository.ClaimIssue`),
and the other three are one shared call chain
(`AttemptRepository.FinishAttempt`) that branches on the attempt's `kind`
and the caller's `target_issue_status` / `review_outcome` before a single
`UPDATE issues SET status = ...` statement. A gate implementation hooks
these two call chains, branching the same way, rather than expecting three
independent functions to attach to (§17.11 enumerates the exact current call
sites this contract's gates must reach).

Three other paths write `issues.status` directly on row creation, with no
`CanTransition` check at all because there is no prior status to transition
from, and can place a new issue directly into `review`, `done`, or
`cancelled`: `create_issue`, `apply_issue_plan` (batch creation), and
`apply_import` (logical interchange import).

**Resolved by ISSUE-201:** a new issue created directly with `status:
review` or `status: done` is a transition from a virtual `open`, and is
gated exactly as if it had reached that status through the corresponding
completion path -- `create_issue{status: review}` and each
`apply_issue_plan` entry with `status: review` are evaluated against
`complete_work_to_review`; `status: done` against `complete_work_to_done`.
No new enforcement-point names are introduced; these two existing points
are simply reached from a second call site. `apply_issue_plan` is create,
repeated per entry: it applies the identical rule to each planned issue,
not a separate one. `status: cancelled` remains ungated for all three
paths, matching `update_issue`'s own scope above: gates protect the path
into `review`/`done`, not the path out of active work.

There is no claimed attempt and no review target at creation time, so
`attempt_evidence` and `review_approval` requirements -- both applicable at
`complete_work_to_review` and/or `complete_work_to_done` per §17.4 -- can
never be satisfied by a bare create. They are still *evaluated* at that
point (this is not the "unmatched combination" case §17.4 treats as
not-applicable) and therefore always fail with `WORKFLOW_GATE_UNSATISFIED`
whenever an active policy requires either kind at that enforcement point.
Only `issue_field_nonblank` can be satisfied at create time, checked
directly against the fields supplied in the same call (e.g.
`acceptance_criteria`). In practice: creating straight into `review` or
`done` is only possible while no active policy matching the issue's
type/labels requires attempt evidence or review approval at that
completion path -- the same outcome `update_issue`'s direct-transition
rejection produces for an existing issue, reached here through a different
call site instead of a status patch.

`apply_import` remains exempt from gate evaluation -- it restores
historical terminal state, not a live transition -- but it no longer skips
validation: every imported issue runs the same field/enum/limit validation
`create_issue` runs (`CreateIssueInput.Validate`), regardless of status.
Import can restore an issue that no active policy would currently allow to
be *created* directly in `review`/`done`; that is intentional (§17.6
already establishes that policy changes are never retroactive), not a
bypass, since import never re-runs live policy evaluation for any status.

### 17.2. WorkflowPolicy and PolicyRequirement

```text
WorkflowPolicy
  id                 ULID
  selector
    issue_types      Type[] (empty means every executable type: task, bug)
    labels_all       string[] (every value must be present on the issue; empty means no label constraint)
  status             active | archived
  version            integer, incremented on any selector or requirement change
  created_at
  updated_at

PolicyRequirement
  policy_id          ULID (owning policy)
  key                string, unique within its policy, stable across edits
  kind               issue_field_nonblank | attempt_evidence | review_approval
  field              string, issue_field_nonblank only; this version permits only "acceptance_criteria"
  evidence_key       string, attempt_evidence only; a caller-defined stable key (e.g. "tests", "manual_qa")
  purpose            string, review_approval only; a caller-defined stable purpose (e.g. "security", "design")
  allow_not_applicable  boolean, attempt_evidence only; defaults to false. When true, submitted
                     evidence for this requirement's evidence_key (ISSUE-171) may use
                     result=not_applicable instead of result=satisfied; when false (the
                     default), only result=satisfied is accepted and a not_applicable
                     submission fails.
```

An epic never matches a selector: epics are never executable targets (§3.1),
so no policy can add a requirement to one.

A selector matches an issue when: `issue_types` is empty or contains the
issue's type, **and** every value in `labels_all` is present among the
issue's labels (case-insensitive, matching label-name normalization
elsewhere in the domain model, §10). There is no `labels_any`, no override,
no priority, and no exclusion -- a non-matching selector contributes nothing,
and there is no way for one policy to suppress or outrank another.

### 17.3. Composition and ordering

Every `active` policy whose selector matches the issue contributes its
requirements. The effective requirement set for an issue is the union of all
matching policies' requirements, ordered by `policy_id` then `key`, and
deduplicated only when both `policy_id` and `key` are identical (which in
practice only collapses a requirement seen twice through redundant lookups;
distinct policies are never merged, even when they declare the same `kind`
and the same `evidence_key` or `purpose` -- each is a separate requirement
that must be independently satisfied, and each produces its own detail entry
on failure). An `archived` policy contributes nothing. There is no
expression language, no conditional composition, and no way for a
requirement to depend on another requirement.

### 17.4. Which requirement kinds gate which enforcement points

Applicability is fixed by requirement kind; it is not independently
configurable per policy or per requirement:

```text
issue_field_nonblank   claim_work, complete_work_to_review, complete_work_to_done
attempt_evidence       complete_work_to_review, complete_work_to_done
review_approval        complete_work_to_done, approve_review
```

Rationale, not configuration: `issue_field_nonblank` is a property of the
issue itself, so it is meaningful before an attempt exists (claim_work) and
at either completion path. `attempt_evidence` is produced during a work
attempt, so it cannot be evaluated before one exists; it does not apply to
`approve_review` because a review attempt does not carry the original work
attempt's evidence. `review_approval` gates every path that can land the
issue in `done` -- both `complete_work_to_done` (a work attempt finishing
directly to `done` without ever creating a review target) and
`approve_review` (a review attempt approving the same issue) -- so a
required approval cannot be bypassed by skipping review.

An unmatched combination (e.g. an `attempt_evidence` requirement considered
at `claim_work`) is never evaluated; it is not an error, it is simply not
applicable at that point.

### 17.5. Requirement satisfaction

- `issue_field_nonblank`: satisfied when the named issue field's current
  value is non-blank (trimmed, non-empty) at the moment of evaluation. It is
  always evaluated against the issue's current stored value, never a
  snapshot -- a field cannot regress from satisfied to unsatisfied mid-review
  in a way that matters, because `update_issue` changes to acceptance
  criteria during an active attempt already require acknowledgment (§16).
- `attempt_evidence`: satisfied when the completing work attempt has
  recorded structured evidence under the requirement's `evidence_key`
  (ISSUE-171 defines how evidence is attached to a lease-authenticated
  attempt). Evaluated against the current attempt's evidence at completion
  time, using the evidence keys named in the attempt's frozen requirement
  snapshot (§17.6) -- not against live policy state.
- `review_approval`: satisfied when an immutable approval record exists for
  the issue and the requirement's `purpose` (ISSUE-173). A review request
  names the purposes it covers (`purposes`, a unique sorted list of 1-10
  normalized keys, `[implementation]` by default); creating or replacing one
  resolves the target's currently-active `review_approval` requirements and
  rejects a request whose purposes do not cover all of them
  (`REVIEW_PURPOSE_REQUIRED`, docs/03 §7.6). Approving that request grants
  one immutable approval row per purpose it covers, in the same transaction
  as the approval. `approve_review` checks the approving request's own
  purposes against its review target's frozen snapshot (§17.6) -- always
  satisfied by construction, since creation already required full coverage
  of that same frozen snapshot. `complete_work_to_done` has no review target
  of its own to check against, so it instead checks a live, issue-scoped
  lookup of every purpose the issue holds at least one *still-fresh*
  approval for, regardless of which request or target granted it. An
  approval is fresh while nothing disqualifying has happened to the issue
  after the `target_event_id` it was granted against -- the same predicate
  `approve_review` applies to its own frozen target, so both evaluation
  paths now share one freshness rule (ISSUE-223).

  Plain existence was the original rule and was a hole: `done -> ready` is
  an ordinary transition (§5), so an issue approved for `security` could be
  reopened, modified, and completed straight back to `done` with no new
  review, satisfied by an approval granted for code no reviewer had seen.
  Having the reviewer-free path be laxer than the reviewer-involving one is
  backwards for a gate whose point is that a human looked. "Was this issue
  ever signed off" is a legitimately weaker check, but it must not be the
  invisible default meaning of `review_approval`; if it is wanted it belongs
  in an explicit, separately named requirement kind.

  A consequence worth stating plainly: reopening an issue is itself a
  disqualifying event, so once a `review_approval` requirement is in force,
  a reopened issue reaches `done` through `approve_review` -- a fresh
  request and a real reviewer -- rather than through `complete_work_to_done`
  on a stale approval. The approval rows themselves are still immutable and
  append-only; freshness is evaluated at read time and is never written back.

### 17.6. Snapshot timing

Policy edits are never retroactive:

- **Work attempts** snapshot the full effective requirement set (§17.3) at
  the moment `claim_work` succeeds, and store it with the attempt. Both
  `complete_work_to_review` and `complete_work_to_done` re-evaluate
  satisfaction (§17.5) against that frozen requirement list, never against
  whatever policies are active at completion time. A policy edited or
  archived after claim does not add, remove, or change requirements for an
  attempt already in flight.
- **Review targets** snapshot the `review_approval` requirement set at the
  moment the review request is created (docs/09), and store it with the
  request. `approve_review` re-evaluates satisfaction against that frozen
  list, never against policies active at approval time.
- An existing snapshot never changes, even if every policy that produced it
  is later archived or edited. A new claim or a new review request always
  computes a fresh snapshot from then-current policies.

### 17.7. Gate failure shape

An unmet requirement at any enforcement point fails the call with:

```text
WORKFLOW_GATE_UNSATISFIED
```

and the message `workflow gate requirements are not satisfied`, with one
detail entry per unmet requirement. Details use the project-wide error
detail shape (docs/03 §13) -- gate failures are not an exception to it:

```json
{
  "field": "<requirement_key>",
  "code": "WORKFLOW_GATE_UNSATISFIED",
  "message": "policy_id=<policy_id> enforcement_point=<enforcement_point>: <reason>"
}
```

So of the four dimensions a gate failure reports, exactly one is a
structured field and three are packed into the message:

| dimension          | where it lives                                     |
| ------------------ | -------------------------------------------------- |
| `requirement_key`  | structured -- the detail's `field`                  |
| `policy_id`        | packed -- `message`, as `policy_id=<id>`            |
| `enforcement_point`| packed -- `message`, as `enforcement_point=<point>` |
| `reason`           | packed -- `message`, after the `: ` separator       |

A client that must branch programmatically can branch on `code` and
`field`; `policy_id`, `enforcement_point`, and `reason` are human-readable
diagnostics in `message` and are not a parsing contract. All four remain
available structurally inside the domain (`domain.UnmetRequirement`) --
only the error transport packs them. This keeps every error in the system
on one detail shape rather than giving gate errors a bespoke one; the same
pack-identifying-dimensions-into-the-message pattern is used for
`FOREIGN_KEY_VIOLATION` details.

Multiple unmet requirements from different policies (or the same policy)
all appear as separate details in one response; the call fails atomically
and makes no partial state change (§4/§5 of docs/04 -- gate evaluation runs
inside the same transaction as the claim or transition it guards).

### 17.8. Worked examples

Missing acceptance criteria blocks `claim_work`:

```json
{
  "error": {
    "code": "WORKFLOW_GATE_UNSATISFIED",
    "message": "workflow gate requirements are not satisfied",
    "details": [
      {
        "field": "acceptance_criteria",
        "code": "WORKFLOW_GATE_UNSATISFIED",
        "message": "policy_id=01J1POLICYAAAAAAAAAAAAAAAA enforcement_point=claim_work: issue field 'acceptance_criteria' is blank"
      }
    ]
  }
}
```

Missing implementation evidence blocks `complete_work_to_review`:

```json
{
  "error": {
    "code": "WORKFLOW_GATE_UNSATISFIED",
    "message": "workflow gate requirements are not satisfied",
    "details": [
      {
        "field": "implementation_evidence",
        "code": "WORKFLOW_GATE_UNSATISFIED",
        "message": "policy_id=01J1POLICYBBBBBBBBBBBBBBBB enforcement_point=complete_work_to_review: no attempt_evidence recorded for key 'implementation'"
      }
    ]
  }
}
```

Missing test evidence blocks `complete_work_to_done`:

```json
{
  "error": {
    "code": "WORKFLOW_GATE_UNSATISFIED",
    "message": "workflow gate requirements are not satisfied",
    "details": [
      {
        "field": "test_evidence",
        "code": "WORKFLOW_GATE_UNSATISFIED",
        "message": "policy_id=01J1POLICYBBBBBBBBBBBBBBBB enforcement_point=complete_work_to_done: no attempt_evidence recorded for key 'tests'"
      }
    ]
  }
}
```

Missing security review blocks `approve_review` (and equally blocks a work
attempt from completing straight to `done`, §17.4):

```json
{
  "error": {
    "code": "WORKFLOW_GATE_UNSATISFIED",
    "message": "workflow gate requirements are not satisfied",
    "details": [
      {
        "field": "security_review",
        "code": "WORKFLOW_GATE_UNSATISFIED",
        "message": "policy_id=01J1POLICYCCCCCCCCCCCCCCCC enforcement_point=approve_review: no review_approval recorded for purpose 'security'"
      }
    ]
  }
}
```

Two matching policies contribute independent requirements; both appear when
both are unmet, ordered by `policy_id` then `key`:

```json
{
  "error": {
    "code": "WORKFLOW_GATE_UNSATISFIED",
    "message": "workflow gate requirements are not satisfied",
    "details": [
      {
        "field": "acceptance_criteria",
        "code": "WORKFLOW_GATE_UNSATISFIED",
        "message": "policy_id=01J1POLICYAAAAAAAAAAAAAAAA enforcement_point=claim_work: issue field 'acceptance_criteria' is blank"
      },
      {
        "field": "acceptance_criteria",
        "code": "WORKFLOW_GATE_UNSATISFIED",
        "message": "policy_id=01J1POLICYDDDDDDDDDDDDDDDD enforcement_point=claim_work: issue field 'acceptance_criteria' is blank"
      }
    ]
  }
}
```

(Both policies happen to declare a requirement with the `key`
`acceptance_criteria`, but they are not deduplicated because their
`policy_id`s differ -- each is evaluated and reported independently, even
though satisfying the single underlying field satisfies both at once. The
two details are therefore identical in `field` and `code`, and differ only
in the `policy_id=` prefix packed into `message`.)

Policy edited during active work does not retroactively change an in-flight
attempt's gates. A policy adds an `attempt_evidence` requirement with key
`"manual_qa"` after an attempt has already claimed the issue:

```text
t0  claim_work succeeds; attempt snapshot = [acceptance_criteria]
t1  policy edited: adds attempt_evidence "manual_qa"
t2  complete_work_to_review evaluates the t0 snapshot, not the live policy
    -> "manual_qa" is not required for this attempt
t3  a *new* claim on a different issue matching the same policy snapshots
    [acceptance_criteria, manual_qa] and is subject to both
```

Direct `update_issue` transitions into `review` or `done` are rejected the
same way any other unsupported status transition is, before gate evaluation
is even reached -- gates guard the four enforcement points, not a fifth path
that bypasses them:

```json
{
  "error": {
    "code": "INVALID_STATUS_TRANSITION",
    "message": "update_issue cannot set status to 'review' or 'done' directly",
    "details": [
      {
        "field": "changes.status",
        "code": "UNSUPPORTED_DIRECT_TRANSITION"
      }
    ]
  }
}
```

### 17.9. Limits

```text
max requirements per policy      50
max labels_all entries           20
max policy key length            128 runes  (key, evidence_key, purpose)
```

These follow the existing convention of small bounded collections
elsewhere in the domain model (docs/06 §5) rather than unbounded growth.
There is no separate limit on the number of active policies in a project;
a project that needs more than a handful of policies is already well
outside typical MVP usage, and policy count is admin-configured, not
generated by agent activity the way issues or events are.

### 17.10. Compatibility

A project with zero configured policies (every existing project today, and
any new project until an operator defines one) sees no behavior change: no
selector ever matches, no requirement is ever contributed, `claim_issue` and
`finish_attempt` never return `WORKFLOW_GATE_UNSATISFIED`, and their
existing response shapes are unchanged. Adding the new error code is
additive to docs/03 §13, not a breaking change to any existing response.

Attempts and review requests that exist before this feature is implemented
have no requirement snapshot; they are treated as an empty requirement set
at their respective enforcement points (equivalent to "no policy matched"),
never as an error condition.

Logical interchange, work context, and board visibility for gate state are
specified by their own surface contracts, delivered by ISSUE-175: the
`extensions.gates` interchange namespace in docs/07 §4.1, the always-present
`gates` summary in `get_work_context`'s output (docs/03 §11), and the
board's gate-progress rows in docs/13 §7. All three carry the same no-policy
compatibility behavior this section defines: zero policies means an empty
summary, an absent interchange namespace, and a board that states no gate
requirements apply.

### 17.11. Current call sites gates must reach

Recorded here so a later implementer does not have to rediscover it: as of
this contract, `issues.status` is written by exactly five call sites across
three files, and none of them share a single persistence choke point.

Attempt-mediated (the four enforcement points; both `finish_attempt` rows
share one function that branches internally):

```text
claim_work                internal/adapters/sqlite/attempts.go  AttemptRepository.ClaimIssue
                           (inserts work_attempts; issues.status itself is not written here)
complete_work_to_review    internal/adapters/sqlite/attempts.go  AttemptRepository.FinishAttempt
                           kind=work, outcome=completed, target_issue_status=review
complete_work_to_done      internal/adapters/sqlite/attempts.go  AttemptRepository.FinishAttempt
                           kind=work, outcome=completed, target_issue_status=done
approve_review             internal/adapters/sqlite/attempts.go  AttemptRepository.FinishAttempt
                           kind=review, outcome=completed, review_outcome=approved
```

Not attempt-mediated (must be closed per §17.1; create-time semantics for
the three creation paths are resolved there too, per ISSUE-201 -- all four
rows require implementation, tracked by ISSUE-172):

```text
update_issue (direct transition)   internal/adapters/sqlite/issues.go   IssueRepository.UpdateIssue
                                    guarded only by domain.CanTransition; must reject
                                    a "review" or "done" target per §17.1

create_issue (initial status)      internal/adapters/sqlite/issues.go   IssueRepository.CreateIssue
                                    INSERT, no CanTransition check (new row); status review/done
                                    routes through complete_work_to_review/complete_work_to_done
                                    per §17.1

apply_issue_plan (initial status)  internal/adapters/sqlite/planning.go applyPlan
                                    INSERT per entry, no CanTransition check (new row); same rule
                                    as create_issue, applied independently to each planned issue

apply_import (initial status)      internal/adapters/sqlite/projects.go ApplyLogicalProjectImport
                                    INSERT, writes the imported status verbatim; exempt from gate
                                    evaluation (historical restore, §17.1) but must run
                                    CreateIssueInput.Validate like every other creation path
```

Confirmed **not** status-mutating, and therefore out of scope for this
contract: `archive_issue` (only writes `archived_at`); `ExpireAttempts` and
the background cleanup loop (only write `work_attempts.status`, never
`issues.status` -- an issue whose attempt lease expires keeps its current
stored status); `ForceReleaseAttempt` (same, plus an `attempt_interrupted`
event); `CancelReviewRequest` (resolves the review request and terminates any
review attempt bound to it, plus an `attempt_cancelled` event, but never
touches the reviewed issue's status -- docs/09); `doctor` (read-only
diagnostics). None of these require a gate hook.

## 18. Resource identity and reservation overlap

Epic ISSUE-176 adds lease-backed, exclusive reservations over project
resources so concurrent work attempts cannot edit overlapping content. This
section is the canonical identity, normalization, and overlap contract
(`internal/domain/reservation.go`, `reservation_glob.go`,
`reservation_overlap.go`); persistence, acquisition tools, and visibility
are specified separately by ISSUE-178 through ISSUE-183.

### 18.1. Resource kinds

```text
file       a single project-relative path
directory  a project-relative path denoting itself and every descendant
glob       a project-relative pattern over path segments (§18.3)
logical    a namespace:name pair naming a non-filesystem resource
```

All matching is lexical. The database never stats a file, resolves a
symlink, or inspects git -- a reservation's validity depends only on its
normalized identity, never on filesystem contents. All reservations are
exclusive in v1; there is no shared/read mode (docs/06 non-goals).

### 18.2. Path normalization (file, directory, glob)

A path is project-relative, slash-separated, UTF-8, at most 4096 runes.
Normalization splits on `/`, drops every empty and `.` segment, and joins
the remainder back with `/` -- this is the only silent repair; nothing
else is corrected. Every other forbidden form is a hard validation error:

```text
error detail code               condition
INVALID_UTF8 / NUL_NOT_ALLOWED  from ValidateText (docs/02 general convention)
MAX_RUNES                       path exceeds 4096 runes
BACKSLASH_NOT_ALLOWED           path contains '\'
ABSOLUTE_PATH_NOT_ALLOWED       path starts with '/'
VOLUME_FORM_NOT_ALLOWED         path starts with a Windows drive letter and ':'
PARENT_SEGMENT_NOT_ALLOWED      any segment is '..'
EMPTY_ROOT_NOT_ALLOWED          every segment was empty or '.' (nothing left)
```

Case sensitivity: the normalized `Display()` form preserves the caller's
original spelling. A separate comparison `Key()` ASCII-folds `A-Z` to
`a-z` per segment; every other byte, including any non-ASCII UTF-8
sequence, is left byte-exact. This is a deliberate, conservative choice:
folding only the ASCII range avoids the most common cross-platform false
negative (a project edited on both a case-sensitive and a case-insensitive
filesystem) without attempting locale-aware or Unicode case folding, which
has no single correct answer across filesystems. There is no
per-project or per-resource override of this rule.

### 18.3. Glob grammar

A glob is a path (§18.2's normalization and forbidden forms apply
identically) whose segments may additionally be exactly `*` (matches
exactly one segment) or, only as the final segment and at most once,
exactly `**` (matches zero or more trailing segments -- so `a/**` matches
`a` itself as well as every descendant of `a`). No other wildcard form
exists:

```text
error detail code            condition
INVALID_GLOB_SEGMENT         segment contains '?', '[', ']', '{', '}', or '*'/'**'
                              embedded in a literal (e.g. "a*b") -- there is
                              no escape syntax to write a literal wildcard
                              character
STARSTAR_MUST_BE_LAST        '**' appears more than once, or not as the
                              final segment
```

This is a deliberately restricted grammar, not a general glob or regex
engine -- it exists to keep overlap decidable by exact, finite comparison
(§18.4), not by sampling, prefix heuristics, or a regex engine's own
(possibly exponential) matching behavior.

### 18.4. Overlap semantics

Overlap is symmetric, independent of input order, and considers only each
resource's normalized comparison key:

```text
file      x file       equal normalized path
directory x directory  equal, or one is an ancestor of the other
directory x file       the file equals the directory's path, or is a descendant
file      x glob       the glob matches the file
directory x glob       the glob matches the directory's own path, or matches
                        at least one of its descendants
glob      x glob       the two patterns' languages intersect (exact finite
                        segment comparison over the shared fixed-length
                        prefix, then checking whether the shorter side's
                        trailing '**', if any, can reach the longer side's
                        length -- never sampling, never a prefix heuristic)
logical   x logical    equal normalized namespace and case-sensitive name
path      x logical    never overlap, regardless of kind or content
```

`internal/domain.Overlaps(a, b NormalizedResource) bool` implements this
table; `FuzzOverlapsSymmetric` and `FuzzNormalizePathIdempotent` /
`FuzzNormalizeLogicalIdempotent` (`internal/domain/reservation_fuzz_test.go`)
prove symmetry and normalization idempotence respectively.

### 18.5. Logical resources

A logical resource is `namespace:name`. `namespace` must match
`[a-z][a-z0-9.-]{0,63}` exactly -- lowercase only, no case-folding is
applied because the grammar simply does not accept uppercase input in the
first place (`INVALID_NAMESPACE`). `name` is trimmed, 1-256 runes, and
compared case-sensitively with no folding (`REQUIRED` if blank after trim,
`MAX_RUNES` over length). Two logical resources overlap only on an exact
match of both fields; a logical resource never overlaps any path resource.

### 18.6. Limits

```text
max resources per reservation mutation   50
max path/glob length                     4096 runes
max logical namespace length             64 runes ([a-z][a-z0-9.-]{0,63})
max logical name length                  256 runes
```

### 18.7. Operational surfaces

A reservation is visible on four durable surfaces. All four expose the
*display* value and never the comparison key or the normalized automaton
form -- those are internal comparison machinery (§18.2/§18.4), meaningless
as text and misleading if a reader could match on one.

**Issue activity.** Reservations appear as their own activity entity kind
(`entity_type: "reservation"`, category `reservations`), carrying the
reservation summary: id, issue, attempt, kind, display value, status, and
the release timestamp and reason when released. An item's activity
timestamp is `released_at` when the reservation has been released and
`created_at` otherwise, so releasing a reservation is itself new activity.
The `reservation_reserved` / `reservation_released` events are separate
items under the `events` category and are not duplicated here.

**Search.** Indexed as `entity_type: "reservation"`, with the display value
as the document title and the resource kind plus the release reason (when
present) as its content. `comparison_value` and `normalized_json` are never
indexed. Live indexing and index rebuild produce identical documents.

**Logical interchange.** Reservations cross the interchange boundary in the
version-2 `extensions` map under the `reservations` namespace, rather than
as a top-level array (docs/07 §7, option 1; ISSUE-215). The namespace
carries its own `version`, independent of the document version:

```json
"extensions": {
  "reservations": {
    "version": 1,
    "records": [
      {
        "id": "01J1RESERVATIONAAAAAAAAAAA",
        "issue_id": "01J1ISSUEAAAAAAAAAAAAAAAAA",
        "attempt_id": "01J1ATTEMPTAAAAAAAAAAAAAAA",
        "kind": "file",
        "display_value": "src/main.go",
        "comparison_value": "src/main.go",
        "normalized_json": {"kind": "file", "segments": ["src", "main.go"]},
        "status": "released",
        "created_at": "2026-01-01T00:00:00.000000000Z",
        "released_at": "2026-01-01T01:00:00.000000000Z",
        "release_reason": "completed"
      }
    ]
  }
}
```

Only *released* reservations are exported and only released reservations
import. An active reservation is owned by an active attempt, active
attempts do not cross the boundary (docs/07 §5), and importing an active
row would resurrect a live claim on resources that nothing in the
destination holds a lease for -- so `status: "active"` is rejected
explicitly rather than silently downgraded. A released reservation whose
owning attempt is still active is excluded from export for the same
reason: its attempt is not in the document, so the row would import
dangling. Import additionally rejects a record whose `issue_id` disagrees
with its own attempt's issue. Unlike activity and search, interchange does
carry `comparison_value` and `normalized_json`, so an import is a faithful
insert rather than a re-normalization under whatever rules are current;
both are inert for a released row, since the active-identity unique index
is partial on `status = 'active'`.

The reserved/released events are not carried in the namespace: they are
ordinary `issue_events` rows and are already in the document's `events`
array, exactly as review events are.

**Backup and doctor.** Backup needs no reservation-specific path -- SQLite
online backup copies the table with everything else. Doctor validates two
invariants that the storage CHECK constraints cannot catch in a restored
or externally-modified file: no active reservation is owned by a missing,
non-active, or lease-expired attempt; and every row's release state is
self-consistent (released implies both `released_at` and `release_reason`;
active implies neither). Search-index rebuild recreates reservation
documents.

### 18.8. Resolved decisions

No case-sensitivity, symlink, glob, or namespace question is left open by
this contract: case sensitivity is the ASCII-fold rule in §18.2; symlinks
are never resolved (§18.1); the glob grammar is exactly §18.3, with no
escape mechanism and no additional metacharacters; namespace and name
grammar are exactly §18.5. Anything not fixed here (persistence schema,
acquisition tools, board/context visibility, interchange) is explicitly the
responsibility of ISSUE-178 through ISSUE-183, not an oversight of this
contract.
