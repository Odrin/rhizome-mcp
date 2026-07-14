# MCP tool catalog

## 1. Protocol conventions

MCP messages use JSON-RPC 2.0.

Tool inputs and outputs are JSON objects validated by JSON Schema.

Tools return:

- `structuredContent` as the authoritative result;
- an optional short text summary;
- no full duplication of large JSON results in text.

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

The first version exposes 22 tools:

1. `get_project`
2. `list_labels`
3. `create_issue`
4. `update_issue`
5. `get_issue`
6. `list_issues`
7. `archive_issue`
8. `manage_issue_relation`
9. `get_issue_graph`
10. `get_planning_graph`
11. `validate_issue_plan`
12. `apply_issue_plan`
13. `add_comment`
14. `record_decision`
15. `get_issue_activity`
16. `claim_issue`
17. `renew_attempt`
18. `save_attempt_note`
19. `finish_attempt`
20. `get_work_context`
21. `search`
22. `get_changes`

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
```

The project instructions are returned only when requested.

### 4.2. `list_labels`

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
  "view": "standard",
  "include": [],
  "limits": {}
}
```

Views:

```text
compact
standard
full
```

Supported includes:

```text
labels
relations
parent_epic
child_issues
active_attempt
decision_summaries
recent_comments
recent_attempt_notes
attempt_history
artifacts
```

Default:

```text
view = standard
include = []
```

Output:

```text
issue
included sections
truncated sections
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

Output:

```text
issue compact projection
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

Only a null `idempotency_key` is currently accepted. No duplicate-retry
semantics are promised.

### 8.2. `record_decision`

Input:

```json
{
  "issue_id": "ISSUE-42",
  "title": "Use renewable leases",
  "summary": "Active attempts use short renewable leases.",
  "content": "Full reasoning in Markdown.",
  "status": "active",
  "supersedes_id": null,
  "idempotency_key": null
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
`decision_recorded` event. Only a null `idempotency_key` is accepted; replay
semantics are not provided.

### 8.3. `get_issue_activity`

Input:

```json
{
  "issue_id": "ISSUE-42",
  "types": [
    "comments",
    "decisions",
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
`comment`, `decision`, `attempt`, `attempt_note`, `event`, and `artifact`.

The `types` input is optional; when omitted or empty, all categories are
returned. Supported categories are exactly `comments`, `decisions`, `attempts`,
`attempt_notes`, `events`, and `artifacts`. The default limit is `20`, the
maximum limit is `100`, and only `newest_first` ordering is supported.

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
- creates an opaque lease token.

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
acceptance_check
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
previous_attempt
checkpoint
requested sections
warnings
truncated
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
