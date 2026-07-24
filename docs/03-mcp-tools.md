# MCP tool catalog

## 1. Protocol conventions

MCP messages use JSON-RPC 2.0.

Tool inputs and outputs are JSON objects validated by JSON Schema.

Tools return:

- `structuredContent` as the authoritative result;
- an optional short text summary;
- short `next_actions` on workflow-sensitive results;
- no full duplication of large JSON results in text.

The initialize response contains compact baseline workflow instructions. Full
guidance is available through these static Markdown resources:

- `rhizome://guides/agent-workflow`;
- `rhizome://guides/issue-lifecycle`;
- `rhizome://guides/multi-agent-handoff`.

All IDs accepted as `issue_id` may be either:

- internal ULID;
- display ID such as `ISSUE-42`.

Other entity IDs use internal ULIDs only.

## 2. Common response conventions

Potentially large results include:

```text
has_more
next_cursor
truncated
truncation_reason
```

Collections use cursor-based pagination.

Default collection limit: `20`.

Maximum ordinary collection limit: `100`.

Errors use:

```json
{
  "code": "ISSUE_BLOCKED",
  "message": "Issue cannot be claimed while blockers are unresolved.",
  "details": {},
  "retryable": false
}
```

## 3. Tool inventory

The catalog exposes 32 tools:

1. `get_project`
2. `export_project`
3. `validate_import`
4. `apply_import`
5. `list_labels`
6. `create_issue`
7. `update_issue`
8. `get_issue`
9. `list_issues`
10. `archive_issue`
11. `create_review_request` (deprecated — see section 6.6)
12. `get_review_request`
13. `list_review_requests`
14. `cancel_review_request`
15. `supersede_review_request` (deprecated — see section 6.6)
16. `replace_review_request`
17. `manage_issue_relation`
18. `get_issue_graph`
19. `get_planning_graph`
20. `validate_issue_plan`
21. `apply_issue_plan`
22. `add_comment`
23. `record_decision`
24. `list_decisions`
25. `get_issue_activity`
26. `claim_issue`
27. `renew_attempt`
28. `save_attempt_note`
29. `finish_attempt`
30. `get_work_context`
31. `search`
32. `get_changes`

---

## 4. Tool annotations

Every tool returned by `tools/list` carries an explicit
[MCP tool annotation](https://modelcontextprotocol.io/specification) set:
`readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`. These are
assigned in code through one required `toolHints(...)` argument on every
registration call in `internal/adapters/mcp/adapter.go`, so a newly added tool
that omits this argument fails to compile — the classification cannot be
silently skipped.

**Annotations are advisory client guidance, not an authorization boundary.**
A client may use them to decide whether to warn a user or skip a confirmation
prompt, but the server always re-validates every request server-side
regardless of what a client inferred from these hints. Nothing here weakens
or replaces domain-level validation, optimistic concurrency, or lease checks.

`openWorldHint` is `false` for every tool: Rhizome only reads and writes its
own local SQLite project database. No tool fetches a URL or otherwise reaches
into an external system on the server's behalf (artifact `uri` values are
stored as opaque strings, never dereferenced).

`idempotentHint` is `true` only where repeating the exact same call arguments
is *guaranteed* to produce no additional effect beyond the first call — not
merely because a tool happens to accept an optional `idempotency_key`. Two
independent patterns earn a `true` here:

- **Mandatory-key replay** — `apply_issue_plan` and `replace_review_request`
  require `idempotency_key` on every call (it is a required schema field,
  not optional) and the repository replays the original result for a
  repeated key. Same arguments necessarily means the same key, so the
  guarantee holds unconditionally.
- **Fail-safe-on-retry gating** — a mutation guarded by a precondition that
  the first successful call itself invalidates: optimistic-concurrency
  `expected_version` (`update_issue`, `archive_issue`,
  `cancel_review_request`, `supersede_review_request`,
  `replace_review_request`'s predecessor), a claimability check
  (`claim_issue`), an active-lease check (`finish_attempt`), or a storage
  constraint (`manage_issue_relation`'s unique `(source, target, type)` index
  on add, not-found on remove; `apply_import`'s empty-destination
  requirement). After the first call, the precondition no longer holds, so a
  bare repeat with identical arguments fails without any further write —
  analogous to the MCP specification's own `delete_file` example.
  `replace_review_request` satisfies both patterns at once: the mandatory
  key gives replay, and the predecessor's `expected_version` additionally
  fails safe if a caller races without noticing the key collision.

Tools that only ever append or insert with no such gate and no mandatory key
(`create_issue`, `create_review_request`, `add_comment`, `record_decision`,
`save_attempt_note`, `renew_attempt`) are `idempotentHint: false`: a bare
repeat creates a second issue, comment, decision, or note, or (for
`renew_attempt`) pushes the lease expiry further out again. An optional
`idempotency_key` on these tools changes behavior only when the caller
actually supplies one — it is not part of the unconditional invocation
contract, so it does not change the hint.

`destructiveHint` follows the guidance's own examples — overwrite, archive,
cancel, supersede, bulk-apply, or otherwise destroying prior effective state —
rather than the tool's read/write split alone:

- `update_issue` can overwrite title, description, status, and
  `blocked_reason`.
- `archive_issue`, `cancel_review_request`, and `supersede_review_request`
  each end the prior lifecycle state of their target.
- `manage_issue_relation` can remove an existing relation (`action: "remove"`).
- `apply_import` and `apply_issue_plan` are bulk-apply operations with a wide
  blast radius even though individual writes are additive.
- `finish_attempt` can transition an issue's status (including to `blocked`,
  overwriting a prior `blocked_reason`) as part of ending the lease.
- `record_decision` can flip an existing decision's `status` to `superseded`
  in the same transaction when `supersedes_id` is supplied.
- `create_review_request` is **not** destructive: it only records a
  `supersedes_id` link and never closes the predecessor itself — that split
  responsibility is exactly what `replace_review_request` replaces (see
  section 6.6 for the deprecation policy).
- `replace_review_request` is destructive: a successful call ends the
  predecessor's lifecycle (superseded) as part of creating its successor,
  in the same transaction.

### 4.1. Annotation matrix

| Tool | readOnly | destructive | idempotent | openWorld |
| --- | --- | --- | --- | --- |
| `get_project` | ✓ | | ✓ | |
| `export_project` | ✓ | | ✓ | |
| `validate_import` | ✓ | | ✓ | |
| `apply_import` | | ✓ | ✓ | |
| `list_labels` | ✓ | | ✓ | |
| `create_issue` | | | | |
| `update_issue` | | ✓ | ✓ | |
| `get_issue` | ✓ | | ✓ | |
| `list_issues` | ✓ | | ✓ | |
| `archive_issue` | | ✓ | ✓ | |
| `create_review_request` | | | | |
| `get_review_request` | ✓ | | ✓ | |
| `list_review_requests` | ✓ | | ✓ | |
| `cancel_review_request` | | ✓ | ✓ | |
| `supersede_review_request` | | ✓ | ✓ | |
| `replace_review_request` | | ✓ | ✓ | |
| `manage_issue_relation` | | ✓ | ✓ | |
| `get_issue_graph` | ✓ | | ✓ | |
| `get_planning_graph` | ✓ | | ✓ | |
| `validate_issue_plan` | ✓ | | ✓ | |
| `apply_issue_plan` | | ✓ | ✓ | |
| `add_comment` | | | | |
| `record_decision` | | ✓ | | |
| `list_decisions` | ✓ | | ✓ | |
| `get_issue_activity` | ✓ | | ✓ | |
| `claim_issue` | | | ✓ | |
| `renew_attempt` | | | | |
| `save_attempt_note` | | | | |
| `finish_attempt` | | ✓ | ✓ | |
| `get_work_context` | ✓ | | ✓ | |
| `search` | ✓ | | ✓ | |
| `get_changes` | ✓ | | ✓ | |

A blank cell means the hint is `false`. `openWorldHint` is `false` for every
tool, per the local-first rationale above.

---

## 5. Project and discovery

### 5.1. `get_project`

Purpose:

Return metadata and server capabilities for the current project.

Input:

```json
{
  "include_instructions": false
}
```

Output:

```text
project
session
app_version
schema_version
config_version
limits
supported_issue_types
supported_statuses
supported_relation_types
supported_priorities
latest_event_id
guides
next_actions
```

The project instructions are returned only when requested. `guides` links the
three workflow resources advertised by the server.

### 5.2. `export_project`

Purpose:

Export the current project as the version 1 logical interchange document.

Input:

```json
{}
```

Output:

The structured content is the full logical project document with the required
`format`, `version`, `exported_at`, `project`, and entity arrays. The tool
returns the document directly as structured content and does not duplicate it as
text.

### 5.3. `validate_import`

Purpose:

Validate a version 1 logical project interchange document without mutating storage and return a deterministic dry-run summary.

Input:

```json
{
  "document": "{\"format\": \"rhizome-logical-project\", \"version\": 1, \"project\": {\"id\": \"01ARZ3NDEKTSV4RRFFQ69G5FAV\"}, \"issues\": [], \"labels\": [], \"issue_labels\": [], \"relations\": [], \"comments\": [], \"decisions\": [], \"attempts\": [], \"attempt_notes\": [], \"artifacts\": [], \"events\": []}"
}
```

Output:

The structured content is the dry-run summary containing deterministic counts, zero writes, and sorted conflicts. The tool does not duplicate the full document payload in text.

### 5.4. `apply_import`

Purpose:

Apply a validated version 1 logical project interchange document into an empty destination and return a deterministic apply result with created counts, zero conflicts on success, and the latest destination event ID.

Input:

```json
{
  "document": "{\"format\": \"rhizome-logical-project\", \"version\": 1, \"project\": {\"id\": \"01ARZ3NDEKTSV4RRFFQ69G5FAV\"}, \"issues\": [], \"labels\": [], \"issue_labels\": [], \"relations\": [], \"comments\": [], \"decisions\": [], \"attempts\": [], \"attempt_notes\": [], \"artifacts\": [], \"events\": []}"
}
```

Output:

The structured content is the apply result containing deterministic counts, sorted conflicts, and the latest event ID. The tool does not duplicate the full document payload in text.

### 5.5. `list_labels`

Input:

```json
{
  "query": null,
  "limit": 50,
  "cursor": null
}
```

Output:

```text
items
next_cursor
has_more
```

Deterministic ordering:

```text
normalized_name ASC
```

---

## 6. Issue operations

### 6.1. `create_issue`

Input:

```json
{
  "type": "task",
  "title": "Implement atomic claim",
  "description": null,
  "acceptance_criteria": null,
  "status": "open",
  "priority": "medium",
  "parent_issue_id": null,
  "blocked_reason": null,
  "labels": [],
  "create_missing_labels": true,
  "idempotency_key": null
}
```

Rules:

- `type`, `title` are required.
- `status` defaults to `open`.
- `priority` defaults to `medium`.
- `blocked_reason` is required when status is `blocked`.
- Parent constraints are validated.
- `idempotency_key` is optional. When supplied, it must be a non-blank string up to 128 runes. Reusing the same key with the same normalized request replays the original issue response; reusing it with a different request returns `IDEMPOTENCY_CONFLICT`.

Output:

```text
issue compact projection
```

### 6.2. `update_issue`

Patch semantics:

- absent field: leave unchanged;
- `null`: clear a nullable field;
- empty string: an explicit value if allowed.

Input:

```json
{
  "issue_id": "ISSUE-42",
  "expected_version": 7,
  "changes": {
    "title": "Implement atomic issue claim",
    "description": null,
    "acceptance_criteria": null,
    "type": "task",
    "priority": "high",
    "status": "ready",
    "parent_issue_id": null,
    "blocked_reason": null,
    "labels": ["database", "concurrency"]
  },
  "create_missing_labels": true,
  "idempotency_key": null
}
```

Only changed fields should be present.

`idempotency_key` is optional. When supplied, it must be a non-blank string up
to 128 runes. Reusing the same key with the same normalized request (`issue_id`,
`expected_version`, `changes`, and `create_missing_labels`) replays the original
patch response, including after `expected_version` has since moved on from a
later, unrelated update. Reusing the key with a different normalized request
returns `IDEMPOTENCY_CONFLICT`.

Output:

```text
issue standard projection
changed_fields
```

### 6.3. `get_issue`

Input:

```json
{
  "issue_id": "ISSUE-42",
  "view": "standard"
}
```

Views:

```text
compact
standard
full
```

Default:

```text
view = standard
```

Output:

```text
issue projection
```

### 6.4. `list_issues`

Input filters:

```json
{
  "types": [],
  "statuses": [],
  "effective_statuses": [],
  "priorities": [],
  "labels": [],
  "parent_issue_id": null,
  "is_blocked": null,
  "is_claimable": null,
  "include_archived": false,
  "limit": 20,
  "cursor": null,
  "view": "compact"
}
```

Output:

```text
items
next_cursor
has_more
```

Deterministic ordering:

```text
priority DESC
is_claimable DESC
sequence_no ASC
```

`view` accepts exactly two values, `compact` and `full`. `view` defaults to
`compact` (including when the field is omitted entirely). Unknown values
(anything other than `compact` or `full`) are rejected as an unsupported
field with a structured validation error. `full` still honors the same
`limit`/cursor pagination bounds as `compact` — it is not a way to bypass
paging.

**`compact` (default) field set** — identifiers, title, classification, and
computed status/claimability fields only. No free-text issue bodies:

```text
id
display_id
sequence_no
type
title
status
effective_status
priority
is_blocked
is_claimable
unresolved_blocker_count
labels
updated_at
```

**`full` field set** — the complete issue record plus every computed field,
byte-identical to the pre-1.0 default response shape:

```text
id
display_id
sequence_no
type
title
description
acceptance_criteria
status
priority
parent_issue_id
blocked_reason
version
created_at
updated_at
closed_at
archived_at
labels
effective_status
unresolved_blocker_count
is_blocked
is_claimable
active_attempt_id
```

`full` adds `description`, `acceptance_criteria`, `parent_issue_id`,
`blocked_reason`, `version`, `created_at`, `closed_at`, `archived_at`, and
`active_attempt_id` on top of every `compact` field; nothing in `compact` is
ever different from its `full` value, and `full` is never missing a field
`compact` has.

**Migration note.** Before this change, `compact` was the only projection and
it silently returned every field listed above (including full `description`
and `acceptance_criteria` bodies) for every item — a project with a real
backlog could produce a response of tens to hundreds of kilobytes from a
single default `list_issues` call. If an existing client relied on full issue
bodies (or on `parent_issue_id`, `blocked_reason`, `version`, `created_at`,
`closed_at`, `archived_at`, or `active_attempt_id`) being present in
`list_issues` items, pass `view: "full"` to get that exact shape back
unchanged; no other input changes are required. Clients that only ever used
the fields now in the `compact` set need no changes at all.

**Response budget.** A 100-issue `list_issues` call in the default (`compact`)
view stays under **64 KB** of structured-content JSON regardless of how large
each issue's `description`/`acceptance_criteria` bodies are, because those
bodies are never present in the compact projection. This is enforced by an
integration test (`TestIntegrationListIssuesCompactViewStaysWithinByteBudget`
in `integration/list_issues_test.go`) that creates 100 issues with multi-kilobyte
description and acceptance-criteria bodies and asserts the default response
stays within budget; measured response size for that fixture is approximately
46 KB. The equivalent `view: "full"` call over the same 100 issues measures
approximately 582 KB in the same test — illustrating why `full` is opt-in.

### 6.5. `archive_issue`

Input:

```json
{
  "issue_id": "ISSUE-42",
  "expected_version": 9,
  "idempotency_key": null
}
```

Rules:

- active attempts prevent archiving;
- related data remains intact;
- archived issues are hidden by default.

`idempotency_key` is optional. When supplied, it must be a non-blank string up
to 128 runes. Reusing the same key with the same normalized request (`issue_id`
and `expected_version`) replays the original archive response, including after
the issue has already been archived by that same call. Reusing the key with a
different normalized request returns `IDEMPOTENCY_CONFLICT`.

Output:

```text
issue compact projection
```

### 6.6. Review requests

Review requests bind review work to an issue version, event position, and
optional artifact set. A review request is claimable only while its status is
`open`.

#### `create_review_request`

**Deprecated.** `supersedes_id` only records a predecessor link; it never
closes that predecessor. Coordinating creation with a separate
`supersede_review_request` call leaves the review lifecycle in a partial
state after a failure or concurrency conflict between the two calls. Prefer
`replace_review_request` (below), which does both atomically. Retained as a
compatibility alias for one release; `supersedes_id` retains its current
(non-closing) semantics for as long as the alias exists.

Input:

```json
{
  "issue_id": "ISSUE-42",
  "target_issue_version": 9,
  "target_event_id": 1842,
  "artifact_ids": [],
  "supersedes_id": null
}
```

`issue_id`, `target_issue_version`, and `target_event_id` are required.
`artifact_ids` may contain at most 20 IDs. Creating another review request for
the same target returns `REVIEW_ALREADY_EXISTS`.

#### `replace_review_request`

Atomically supersedes a predecessor review request and creates its open
successor in one SQLite transaction: no partial state is observable between
"predecessor closed" and "successor created." The predecessor determines the
issue scope, so there is no separate `issue_id` field.

Input:

```json
{
  "predecessor_request_id": "01ARZ3NDEKTSV4RRFFQ69G5FAV",
  "predecessor_expected_version": 1,
  "target_issue_version": 10,
  "target_event_id": 1900,
  "artifact_ids": [],
  "idempotency_key": "replace-2026-07-24-01"
}
```

`predecessor_request_id`, `predecessor_expected_version`, `target_issue_version`,
`target_event_id`, and `idempotency_key` are required. Unlike every other
review-request tool, `idempotency_key` here is mandatory, not optional: this
operation does not hold the predecessor's attempt lease token, so replaying a
retried call safely (rather than risking a second successor from a client-side
retry) depends on the key.

Output:

```text
predecessor
successor
latest_event_id
```

`predecessor` and `successor` are each a full review request record (see the
shared field list below). `successor.supersedes_id` always points back to
`predecessor.id`, and `predecessor.status` is always `superseded`.

Failure modes, all structured and side-effect-free (zero writes):

- Stale `predecessor_expected_version` → `VERSION_CONFLICT` (retryable).
- Predecessor is currently `claimed` → `REVIEW_REQUEST_CLAIMED`. This
  operation does not carry the attempt's lease token, so it cannot detach or
  orphan an active review attempt; the lease holder must `finish_attempt` or
  otherwise interrupt its attempt first, which naturally returns the
  predecessor to `open` for the review requester to try again — or a client
  can resolve the review outcome and create a fresh request instead.
- Predecessor is any other terminal status (`approved`, `changes_requested`,
  `blocked`, `cancelled`, `superseded`) → `REVIEW_REQUEST_NOT_REPLACEABLE`.
- The successor's target already has an unrelated active request →
  `REVIEW_ALREADY_EXISTS`.
- Reusing `idempotency_key` with a different normalized request →
  `IDEMPOTENCY_CONFLICT`. Reusing it with the same request replays the
  original `predecessor`/`successor`/`latest_event_id` without any new
  writes or events.

#### `get_review_request`

Input:

```json
{
  "review_request_id": "01ARZ3NDEKTSV4RRFFQ69G5FAV"
}
```

#### `list_review_requests`

Input:

```json
{
  "status": "open",
  "claimable": true,
  "limit": 20,
  "cursor": null
}
```

`status` and `claimable` are optional filters. Supported statuses are:

```text
open
claimed
approved
changes_requested
blocked
cancelled
superseded
```

Output:

```text
items
next_cursor
has_more
```

#### `cancel_review_request` and `supersede_review_request`

**`supersede_review_request` is deprecated** for the same reason as
`create_review_request.supersedes_id` above: it closes a request without
creating or identifying a replacement, so a client must coordinate a second
`create_review_request` call itself. Prefer `replace_review_request`.
`cancel_review_request` is not deprecated — cancelling with no successor
remains a distinct, legitimate operation with no atomicity problem to fix.

Both operations require the request ID and its current version:

```json
{
  "review_request_id": "01ARZ3NDEKTSV4RRFFQ69G5FAV",
  "expected_version": 1
}
```

They apply only to open or claimed review requests and return the updated
review request.

Every review-request tool — including each of `replace_review_request`'s
`predecessor` and `successor` fields — returns a review request with:

```text
id
issue_id
target_issue_version
target_event_id
artifact_ids
status
supersedes_id
active_attempt_id
claimable
version
created_at
resolved_at
```

---

## 7. Relations and graphs

### 7.1. `manage_issue_relation`

Input:

```json
{
  "action": "add",
  "source_issue_id": "ISSUE-12",
  "target_issue_id": "ISSUE-42",
  "relation_type": "blocks",
  "idempotency_key": null
}
```

Actions:

```text
add
remove
```

Types:

```text
blocks
related_to
duplicates
```

Rules:

- relation identity is the canonical tuple;
- no relation ID is required for removal;
- cycles in `blocks` are rejected;
- symmetric `related_to` is canonicalized.

`idempotency_key` is optional. When supplied, it must be a non-blank string up
to 128 runes. Reusing the same key with the same normalized request (`action`,
`source_issue_id`, `target_issue_id`, and `relation_type`) replays the original
mutation response. Reusing the key with a different normalized request returns
`IDEMPOTENCY_CONFLICT`.

Output:

```text
relation
affected_issues
```

### 7.2. `get_issue_graph`

Input:

```json
{
  "root_issue_id": "ISSUE-42",
  "depth": 2,
  "direction": "both",
  "relation_types": ["blocks", "related_to"],
  "include_hierarchy": true,
  "include_terminal": true,
  "max_nodes": 100,
  "view": "compact"
}
```

Limits:

```text
depth default 2, maximum 5
max_nodes default 100, maximum 500
```

Output:

```text
root_issue_id
nodes
edges
summary
entry_points
truncated
truncation_reason
```

Graph format uses normalized `nodes` and `edges`, not recursive trees.

Epic hierarchy is represented as a derived `contains` edge.

**Response budget.** Each node carries the same enriched issue fields as
`list_issues`' `view: "full"` item shape (identifiers, classification,
`parent_issue_id`, `blocked_reason`, timestamps, labels, and the computed
status/claimability fields) — but `description` and `acceptance_criteria` are
always `null` on every node: the repository query that loads graph snapshots
selects `NULL AS description, NULL AS acceptance_criteria` at the SQL layer,
so the two unbounded free-text fields are excluded before the request even
reaches the graph traversal engine, not merely omitted by convention. Node
count is bounded by `max_nodes` (default 100, maximum 500), so response size
scales predictably with a config knob the caller controls, not with issue
body length.

### 7.3. `get_planning_graph`

Input:

```json
{
  "root_issue_id": null,
  "depth": 3,
  "max_nodes": 100,
  "include_review": true,
  "include_related": false
}
```

Behavior:

- includes epic hierarchy;
- includes blockers;
- excludes archived issues;
- highlights claimable entry points;
- includes active attempt summaries;
- excludes full descriptions.

Output:

```text
nodes
edges
entry_points
blocking_nodes
summary
warnings
truncated
```

**Response budget.** Shares the same node projection, storage-level
`description`/`acceptance_criteria` exclusion, and `max_nodes` bound (default
100, maximum 500) documented in section 7.2 for `get_issue_graph` — see that
section's response budget note.

---

## 8. Batch planning

### 8.1. `validate_issue_plan`

Dry-run only.

Input:

```json
{
  "issues": [],
  "relations": [],
  "decisions": []
}
```

New entities may define local refs:

```json
{
  "ref": "storage-layer",
  "type": "task",
  "title": "Implement storage layer"
}
```

Validation includes:

- enum values;
- field limits;
- parent constraints;
- local refs;
- relation duplicates;
- `blocks` cycles;
- batch limits.

Output:

```text
valid
errors
warnings
summary
normalized_plan
```

Errors are deterministically sorted by:

```text
entity index
field path
error code
```

### 8.2. `apply_issue_plan`

Input is the validated plan plus:

```json
{
  "idempotency_key": "plan-storage-v1"
}
```

Limits:

```text
50 new issues
100 relations
50 label assignments
20 decisions
```

Behavior:

- performs the same validation again;
- executes in one transaction;
- rolls back completely on any error;
- assigns issue numbers atomically.

Output:

```text
created_issues by local ref
created_relations
created_decisions
latest_event_id
```

---

## 9. Communication and durable knowledge

### 9.1. `add_comment`

Implemented as append-only issue communication. The issue must exist and must
not be archived. When the MCP connection has a durable session, the created
comment and its `comment_added` event use that session for attribution;
otherwise both attributions are NULL. The operation writes one compact event
payload containing only the comment ID and returns the created comment.

Input:

```json
{
  "issue_id": "ISSUE-42",
  "content": "The claim transaction must also create the event.",
  "idempotency_key": null
}
```

Output:

```text
comment
```

`idempotency_key` is optional. When supplied, it must be a non-blank string up
to 128 runes. Reusing the same key with the same normalized request (`issue_id`
and `content`) replays the original comment response. Reusing the key with a
different normalized request returns `IDEMPOTENCY_CONFLICT`.

### 9.2. `record_decision`

Input:

```json
{
  "issue_id": "ISSUE-42",
  "title": "Use renewable leases",
  "summary": "Active attempts use short renewable leases.",
  "content": "Full reasoning in Markdown.",
  "status": "active",
  "supersedes_id": null
}
```

Output:

```text
decision
superseded_decision_id
```

Decisions are append-only records and may be project-level or issue-level.
Supplying `supersedes_id` atomically creates an active replacement and marks
one active predecessor superseded; the predecessor must have the same scope.
The standalone operation writes one compact, session-attributed
`decision_recorded` event.

`record_decision` does not accept `idempotency_key`: the field is not part of
its published input schema. Unlike the other mutations in this catalog,
`supersedes_id` makes one call responsible for two conditional writes (marking
a predecessor superseded and inserting its replacement); replaying that
combination safely would require storing and re-validating the predecessor's
state as part of the cached response, which is disproportionate to the value
for an append-only decision log. Retry `record_decision` by first checking
`list_decisions` for a decision already recorded with the intended content.

### 9.3. `list_decisions`

Lists project-level decisions when `issue_id` is omitted, or decisions scoped
to one issue when it is supplied. Results use deterministic cursor pagination.

### 9.4. `get_issue_activity`

Input:

```json
{
  "issue_id": "ISSUE-42",
  "types": [
    "comments",
    "decisions",
    "reviews",
    "attempts",
    "attempt_notes",
    "events",
    "artifacts"
  ],
  "limit": 20,
  "cursor": null,
  "order": "newest_first"
}
```

Output:

```text
items
next_cursor
has_more
```

Every item contains `entity_type` and exactly one matching typed payload among
`comment`, `decision`, `review`, `attempt`, `attempt_note`, `event`, and
`artifact`.

The `types` input is optional; when omitted or empty, all categories are
returned. Supported categories are exactly `comments`, `decisions`, `reviews`,
`attempts`, `attempt_notes`, `events`, and `artifacts`. The default limit is
`20`, the maximum limit is `100`, and only `newest_first` ordering is supported.

Pagination uses an opaque, versioned cursor; invalid cursors fail with
structured invalid-argument errors. The response includes `items`,
`next_cursor`, and `has_more`. Each item carries wrapper identity, scope, and
occurrence fields plus the typed payload. Attempts do not expose lease tokens
or lease hashes. Event payloads preserve durable activity metadata. Results are
returned from one consistent read snapshot and are ordered deterministically by
`occurred_at` descending, then a fixed category rank, then source ID. Global or
null-scope decisions and events are excluded from issue activity; full
issue-owned event history, including issue creation, remains included.

**Response budget.** Item count is bounded by `limit` (default `20`, maximum
`100`); this bound is enforced, not just documented. Each item's own
free-text field is bounded at write time, not by activity itself: comment
content up to 50,000 runes (`add_comment`), decision content up to 100,000
runes (`record_decision`), and attempt/attempt-note content up to 50,000
runes each (`finish_attempt` / `save_attempt_note`). A page of `limit` items
that are all near their per-item maximum is a real, if unusual, worst case —
for size-sensitive callers, narrow `types` to the categories you need and
prefer the default `limit` over the maximum.

---

## 10. Agent work lifecycle

### 10.1. `claim_issue`

Input:

```json
{
  "issue_id": "ISSUE-42",
  "lease_seconds": null,
  "idempotency_key": null
}
```

Behavior:

- checks claimability;
- determines `work` or `review`;
- creates attempt atomically;
- records issue version and event ID;
- creates an opaque lease token;
- accepts an optional `idempotency_key` that replays the original claim response for the same normalized request and returns `IDEMPOTENCY_CONFLICT` for a different request with the same key.

Output:

```text
issue compact projection
attempt
lease_token
lease_expires_at
minimal_work_context
warnings
```

The raw lease token is returned once.

### 10.2. `renew_attempt`

Input:

```json
{
  "attempt_id": "01J...",
  "lease_token": "opaque-token",
  "lease_seconds": null
}
```

Output:

```text
lease_expires_at
server_time
```

No content-heavy audit event is written for every heartbeat.

### 10.3. `save_attempt_note`

Input:

```json
{
  "attempt_id": "01J...",
  "lease_token": "opaque-token",
  "kind": "checkpoint",
  "content": "Repository layer is implemented.",
  "next_steps": [
    "Implement claim transaction",
    "Add concurrency tests"
  ],
  "important": true,
  "artifacts": [],
  "idempotency_key": null
}
```

Kinds:

```text
progress
finding
warning
checkpoint
```

`idempotency_key` is optional. When supplied, it must be a non-blank string up
to 128 runes. Reusing the same key with the same normalized request (the
lease-token proof, `kind`, `content`, `next_steps`, `important`, and
`artifacts`) replays the original note response without creating another note,
event, or artifact set. Reusing the key with a different normalized request
returns `IDEMPOTENCY_CONFLICT`.

Output:

```text
attempt_note
artifacts
```

### 10.4. `finish_attempt`

Common input:

```text
attempt_id
lease_token
outcome
result_summary
next_steps
verification
artifacts
acknowledged_changes
idempotency_key
```

`idempotency_key` is optional for `finish_attempt`. When supplied, the
normalized request (including the lease-token proof and caller artifact fields,
but excluding the transient MCP session and generated artifact values) is
hashed and stored with the final response in the same SQLite transaction.
Retrying the same key with the same normalized request replays that exact
response, including after reconnect or database reopen, without creating
another event or artifact set. Reusing the key with a different normalized
request returns `IDEMPOTENCY_CONFLICT`; a request without a key retains the
ordinary non-idempotent finish behavior.

Work outcomes:

```text
completed
failed
interrupted
```

Work completion also supplies:

```text
target_issue_status: done | review | ready | blocked
blocked_reason
failure_reason_code
interruption_reason_code
reason_details
```

Review completion supplies:

```text
review_outcome:
  approved
  changes_requested
  blocked
```

Output:

```text
attempt
issue
warnings
latest_event_id
artifacts
```

Completion checks:

- lease validity;
- issue archive/cancel state;
- blockers;
- issue changes since claim;
- required acknowledgments.

### 10.5. `get_work_context`

Input:

```json
{
  "issue_id": "ISSUE-42",
  "include": [],
  "limits": {}
}
```

Minimal default includes:

```text
issue title and description
acceptance criteria
effective status
unresolved blockers
active decision summaries
review summaries
previous attempt result summary
previous attempt next steps
latest checkpoint
warnings
```

Optional includes:

```text
parent_epic
relations
related_issue_summaries
recent_comments
recent_attempt_notes
decision_content
attempt_history
artifacts
project_instructions
changes_since_previous_attempt
```

Output:

```text
issue
blockers
decisions
reviews
previous_attempt
checkpoint
requested optional sections
warnings
truncated
truncated_sections
next_actions
```

**Response budget.** `get_work_context` is scoped to one issue, so its full
`description`/`acceptance_criteria` bodies (needed to actually work the
issue) are an intentional, expected part of the default response — this is
unlike `list_issues`, where the same fields were being repeated once per
backlog item for no benefit. Every optional list section (`related_issue_summaries`,
`recent_comments`, `recent_attempt_notes`, `decision_content`,
`attempt_history`, `artifacts`, `changes_since_previous_attempt`) is capped at
1–20 items via `limits` (default varies per section; see the audited request
schema), and at most 10 sections can be requested at once
(`MaxWorkContextIncludes`), so optional-section growth is bounded. `blockers`
and `parent_epic` reuse the same per-issue projection as the primary `issue`
field (including full `description`/`acceptance_criteria`), and `blockers` in
particular has no configurable cap — it is bounded only by how many issues
directly block the requested one, which is normally small for a real
dependency graph. `related_issue_summaries` is named "summaries" but, like
`blockers`, currently returns the full per-issue projection rather than a
truncated preview; this is a known imprecision worth tightening in a future
change but is not addressed here, since (unlike the `list_issues` default) it
requires an explicit `include` entry and is capped at 20 items.

---

## 11. Search and synchronization

### 11.1. `search`

Input:

```json
{
  "query": "\"renewable lease\" OR heartbeat",
  "entity_types": [
    "issue",
    "comment",
    "decision",
    "review",
    "attempt_note"
  ],
  "issue_id": null,
  "epic_id": null,
  "statuses": [],
  "labels": [],
  "include_archived": false,
  "limit": 20,
  "cursor": null,
  "snippet_length": 300
}
```

Supported entity types are `issue`, `comment`, `decision`, `review`, and
`attempt_note`.

Maximum snippet length: `1000`.

Output:

```text
results:
  entity_type
  entity_id
  issue_id
  title
  snippet
  score
next_cursor
has_more
```

Full source documents are never returned by search.

**Response budget.** `snippet_length` truncation is enforced at the storage
layer (a SQL `substr` over the FTS5 snippet, re-validated against
`MaxSearchSnippetRunes` on the way out), not merely documented. Combined with
the `limit` cap, a `search` response is bounded by at most `limit` (maximum
`100`) results, each with a `title` and a `snippet` of at most
`snippet_length` runes (maximum `1000`, default `300`); the worst case is
therefore on the order of 100 KB, and the default (`limit` `20`,
`snippet_length` `300`) is on the order of 10 KB.

### 11.2. `get_changes`

Input:

```json
{
  "since_event_id": 1842,
  "issue_id": null,
  "event_types": [],
  "limit": 50
}
```

Maximum limit: `200`.

Output:

```text
events
latest_event_id
has_more
next_event_id
```

This tool supports incremental refresh instead of repeatedly reading full state.

## 12. Error codes

Required domain error codes:

```text
ISSUE_NOT_FOUND
ISSUE_ARCHIVED
ISSUE_BLOCKED
ISSUE_NOT_CLAIMABLE
INVALID_STATUS_TRANSITION
VERSION_CONFLICT
ACTIVE_ATTEMPT_EXISTS
ATTEMPT_NOT_FOUND
ATTEMPT_NOT_ACTIVE
LEASE_EXPIRED
INVALID_LEASE_TOKEN
ISSUE_CHANGED_DURING_ATTEMPT
UNRESOLVED_BLOCKERS_ADDED
BLOCKS_CYCLE
RELATION_ALREADY_EXISTS
INVALID_EPIC_PARENT
IDEMPOTENCY_CONFLICT
LIMIT_EXCEEDED
VALIDATION_ERROR
```

Internal SQLite errors and stack traces are logged locally and mapped to stable domain errors.
