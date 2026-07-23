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

The first version exposes 31 tools:

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
11. `create_review_request`
12. `get_review_request`
13. `list_review_requests`
14. `cancel_review_request`
15. `supersede_review_request`
16. `manage_issue_relation`
17. `get_issue_graph`
18. `get_planning_graph`
19. `validate_issue_plan`
20. `apply_issue_plan`
21. `add_comment`
22. `record_decision`
23. `list_decisions`
24. `get_issue_activity`
25. `claim_issue`
26. `renew_attempt`
27. `save_attempt_note`
28. `finish_attempt`
29. `get_work_context`
30. `search`
31. `get_changes`

---

## 4. Project and discovery

### 4.1. `get_project`

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

### 4.2. `export_project`

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

### 4.3. `validate_import`

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

### 4.4. `apply_import`

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

### 4.5. `list_labels`

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

## 5. Issue operations

### 5.1. `create_issue`

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

### 5.2. `update_issue`

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

### 5.3. `get_issue`

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

### 5.4. `list_issues`

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

`view` defaults to `compact`, which is currently the only supported list
projection.

### 5.5. `archive_issue`

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

### 5.6. Review requests

Review requests bind review work to an issue version, event position, and
optional artifact set. A review request is claimable only while its status is
`open`.

#### `create_review_request`

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

Both operations require the request ID and its current version:

```json
{
  "review_request_id": "01ARZ3NDEKTSV4RRFFQ69G5FAV",
  "expected_version": 1
}
```

They apply only to open or claimed review requests and return the updated
review request.

Every review-request tool returns a review request with:

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

## 6. Relations and graphs

### 6.1. `manage_issue_relation`

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

### 6.2. `get_issue_graph`

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

### 6.3. `get_planning_graph`

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

---

## 7. Batch planning

### 7.1. `validate_issue_plan`

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

### 7.2. `apply_issue_plan`

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

## 8. Communication and durable knowledge

### 8.1. `add_comment`

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

### 8.2. `record_decision`

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

### 8.3. `list_decisions`

Lists project-level decisions when `issue_id` is omitted, or decisions scoped
to one issue when it is supplied. Results use deterministic cursor pagination.

### 8.4. `get_issue_activity`

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

---

## 9. Agent work lifecycle

### 9.1. `claim_issue`

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

### 9.2. `renew_attempt`

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

### 9.3. `save_attempt_note`

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

### 9.4. `finish_attempt`

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

### 9.5. `get_work_context`

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

---

## 10. Search and synchronization

### 10.1. `search`

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

### 10.2. `get_changes`

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

## 11. Error codes

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
