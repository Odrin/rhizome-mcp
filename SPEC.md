# rhizome-mcp complete specification

This file consolidates the modular project documentation for use by implementation agents.


---


# Product goals and scope

## 1. Purpose

`rhizome-mcp` is a task tracking and coordination server designed primarily for AI coding agents.

It provides a shared project memory and execution model that allows different agents to work on the same repository sequentially or concurrently without relying on a single chat context or a pair of mutable Markdown files.

The system must support environments where work can move between GitHub Copilot, Codex, Claude Code, Antigravity or other MCP-compatible clients.

## 2. Product goals

The first version must:

- let agents create, update, relate and search project issues;
- represent epics, tasks and bugs;
- expose dependency and planning graphs;
- let agents atomically claim work;
- recover automatically when an agent process disappears;
- preserve checkpoints, decisions and execution history;
- minimize repeated context loading and token usage;
- support multiple independent agent clients without permanent agent registration;
- run locally as a native binary;
- use a project-local identity but project-external database;
- remain simple enough for a small Go codebase and SQLite deployment.

## 3. Main user

The primary user is an AI agent connected through MCP.

A human developer is a secondary user and interacts through:

- the agent;
- a minimal CLI;
- JSON, table, Markdown or Mermaid output;
- backup and diagnostic commands.

A graphical user interface is not required for the first version.

## 4. Local-first deployment

The expected setup is:

```text
repository/
  .agent-tracker.json
  source files...

application data directory/
  projects/
    <project-id>/
      tasks.db
```

The repository configuration:

```json
{
  "version": 1,
  "project_id": "01J..."
}
```

The database is not committed to Git.

## 5. Technical baseline

- Go
- SQLite
- preferred SQLite driver: `modernc.org/sqlite`
- pure native binary
- primary MCP transport: `stdio`
- optional future transport: local HTTP
- embedded and automatic migrations
- application-layer services shared by MCP and CLI
- SQLite FTS5 for lexical search

## 6. Core terminology

### Project

A repository-associated workspace with one SQLite database.

### Issue

The primary unit of planning and work. Types:

- `epic`
- `task`
- `bug`

### Work attempt

One leased execution of an issue by an agent session.

### Agent session

A temporary record of one MCP client connection. It is not a permanent agent identity.

### Decision

A durable project or issue-level technical/product decision.

### Attempt note

An operational note written during a work attempt. Checkpoints are a special kind of attempt note.

### Relation

A semantic connection between two issues:

- `blocks`
- `related_to`
- `duplicates`

### Artifact

A link to a file, commit, branch, pull request, URL or other result. Binary data is not stored.

## 7. Autonomy principles

Agents may autonomously:

- create and decompose work;
- create epics, tasks and bugs;
- add labels and relations;
- move issues into `ready`;
- claim work;
- block work with a reason;
- record decisions;
- save checkpoints;
- complete work;
- request review;
- review work;
- reopen completed work.

Review is optional.

The system must not require a permanent human assignee or a permanent agent identity.

## 8. Token-efficiency principles

The MCP contract must follow these rules:

- list operations return compact projections;
- graph nodes are compact by default;
- long related content is opt-in;
- search returns snippets, not full documents;
- pagination is cursor-based;
- graph depth and node count are bounded;
- large results explicitly report truncation;
- structured results are not duplicated as full text;
- a dedicated work-context tool returns a bounded context package;
- delta queries return changes after an event ID;
- previous work is summarized through checkpoints and result summaries.

## 9. Non-goals for the first version

The first version does not include:

- web UI or desktop UI;
- authentication or user accounts;
- multi-user permissions;
- permanent agent identities;
- permanent assignees;
- custom issue statuses or workflows;
- nested epics;
- multiple assignees;
- estimates, milestones, due dates or manual rank;
- binary file attachments;
- vector or semantic search;
- automatic deletion of historical data;
- networked multi-node deployment;
- PostgreSQL support;
- Docker as a requirement.


---


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
- associate attempts and events with a connection.

Rules:

- A new session is created for each MCP connection.
- `agent_label` is an arbitrary non-unique string.
- `instance_key` is optional and advisory.
- Neither field is used for ownership or security.
- Old sessions are retained.
- `active`, `stale` and `ended` are computed states.

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

The raw lease token is returned once. Only a hash is stored.

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


---


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

Every item contains `entity_type`.

This tool intentionally replaces several narrow list tools.

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


---


# Storage and runtime

## 1. Database placement

One project uses one SQLite database.

Databases are stored outside repositories:

```text
<application-data>/
  rhizome-mcp/
    projects/
      <project-id>/
        tasks.db
```

The repository contains:

```text
.agent-tracker.json
```

with:

```json
{
  "version": 1,
  "project_id": "01J..."
}
```

The project root is found by searching upward from the current directory.

## 2. SQLite driver

Preferred baseline:

```text
modernc.org/sqlite
```

Reasons:

- pure Go;
- no CGO requirement;
- easier cross-platform builds;
- suitable for standalone binaries.

The concrete driver version must be pinned.

## 3. Required SQLite configuration

At database initialization:

```sql
PRAGMA journal_mode = WAL;
```

For every connection:

```sql
PRAGMA foreign_keys = ON;
PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 5000;
PRAGMA temp_store = MEMORY;
PRAGMA trusted_schema = OFF;
```

Important:

- connection-local pragmas must be applied to every connection;
- WAL mode must be verified after setting;
- databases must be stored on a local filesystem;
- network filesystems and actively synchronized folders are unsupported.

## 4. Connection pool

Recommended first-version defaults:

```go
db.SetMaxOpenConns(4)
db.SetMaxIdleConns(4)
db.SetConnMaxLifetime(0)
db.SetConnMaxIdleTime(0)
```

The exact values are internal defaults, not ordinary user configuration.

SQLite allows one writer at a time; write transactions must remain short.

## 5. Transactions

Operations that must be atomic:

- allocate issue number and create issue;
- update issue and append event;
- claim issue and create attempt;
- renew or finish attempt;
- expire attempt;
- add relation with cycle validation;
- supersede a decision;
- apply batch issue plan;
- mutation plus FTS projection update;
- idempotency record plus mutation result.

Use `BEGIN IMMEDIATE` for operations that are known to write and require early writer acquisition.

Do not perform inside a transaction:

- model calls;
- MCP calls;
- HTTP requests;
- filesystem scans;
- Git commands;
- expensive graph building;
- long serialization.

## 6. Retry policy

Retry complete transactions only for lock contention:

```text
SQLITE_BUSY
SQLITE_LOCKED
```

Suggested bounded backoff:

```text
25 ms
75 ms
200 ms
```

Do not retry domain or constraint failures.

## 7. Tables

Conceptual table set:

```text
projects
agent_sessions
issues
labels
issue_labels
issue_relations
comments
decisions
work_attempts
attempt_notes
artifacts
issue_events
idempotency_records
schema_migrations
search_index (FTS5 virtual table)
```

## 8. Constraints

All ordinary tables should use `STRICT`.

Important constraints:

```text
issues.sequence_no UNIQUE
one active attempt per issue
label name unique case-insensitively
no self relation
canonical uniqueness for relations
foreign keys enabled
enum CHECK constraints
blocked status requires blocked_reason
```

Critical partial unique index:

```sql
CREATE UNIQUE INDEX idx_one_active_attempt_per_issue
ON work_attempts(issue_id)
WHERE status = 'active';
```

Application validation remains necessary, but the database protects critical invariants.

## 9. Recommended indexes

```sql
CREATE INDEX idx_issues_status_priority
ON issues(status, priority);

CREATE INDEX idx_issues_parent
ON issues(parent_id);

CREATE INDEX idx_issues_archived
ON issues(archived_at);

CREATE INDEX idx_comments_issue_created
ON comments(issue_id, created_at);

CREATE INDEX idx_decisions_issue_status
ON decisions(issue_id, status);

CREATE INDEX idx_attempts_issue_started
ON work_attempts(issue_id, started_at DESC);

CREATE INDEX idx_attempts_active_lease
ON work_attempts(lease_expires_at)
WHERE status = 'active';

CREATE INDEX idx_relations_source
ON issue_relations(source_issue_id, type);

CREATE INDEX idx_relations_target
ON issue_relations(target_issue_id, type);

CREATE INDEX idx_events_issue_id
ON issue_events(issue_id, id);
```

Exact index selection should be verified with real query plans.

## 10. Migrations

Migration files are embedded into the binary.

Required behavior:

- sequential integer versions;
- descriptive names;
- checksums;
- automatic forward migration;
- no automatic downgrade;
- one migration owner at startup;
- post-migration foreign key check.

Migration table:

```sql
CREATE TABLE schema_migrations (
    version     INTEGER PRIMARY KEY,
    name        TEXT NOT NULL,
    checksum    TEXT NOT NULL,
    applied_at  TEXT NOT NULL
) STRICT;
```

Startup sequence:

1. Resolve project.
2. Open database.
3. Configure connection.
4. Verify WAL.
5. Acquire migration lock.
6. Validate migration checksums.
7. Apply pending migrations.
8. Run `PRAGMA foreign_key_check`.
9. Start services.

## 11. Search

Use SQLite FTS5.

Index these sources:

- issue title;
- issue description;
- comments;
- decision title;
- decision summary;
- decision content;
- attempt note content.

Do not index by default:

- raw metadata JSON;
- heartbeats;
- every issue event;
- lease tokens;
- internal stack traces.

The FTS index is updated in the same transaction as source changes.

Provide a rebuild operation through CLI/maintenance code.

## 12. WAL and checkpoints

Default:

```sql
PRAGMA wal_autocheckpoint = 1000;
```

Do not checkpoint after every write.

Useful checkpoints:

- before backup;
- during clean shutdown;
- after large migrations;
- through maintenance commands.

Prefer `PASSIVE` during normal operation.

Use `TRUNCATE` only in controlled maintenance scenarios.

## 13. Backup

Do not copy only `tasks.db` while the server is running in WAL mode.

Supported backup mechanisms:

- SQLite online backup API;
- `VACUUM INTO`;
- controlled shutdown and complete file copy.

CLI should expose a safe command such as:

```bash
rhizome-mcp backup --output project-backup.db
```

## 14. Health checks

Minimal health command:

```bash
rhizome-mcp doctor
```

Checks:

```sql
PRAGMA quick_check;
PRAGMA foreign_key_check;
```

Also verify:

- expected schema version;
- WAL mode;
- FTS5 availability;
- write permissions;
- data directory permissions;
- free disk space;
- oversized WAL;
- expired attempts;
- migration consistency;
- one-active-attempt invariant.

Deep mode:

```bash
rhizome-mcp doctor --full
```

may run:

```sql
PRAGMA integrity_check;
```

## 15. Time handling

All domain time comes from an injected clock:

```go
type Clock interface {
    Now() time.Time
}
```

Production uses a real UTC clock.

Tests use a controllable fake clock.

This is mandatory for deterministic testing of:

- lease expiry;
- heartbeat;
- stale sessions;
- cleanup;
- event timestamps;
- retry timing.

## 16. Lease cleanup

Use both:

- lazy expiry during claim/list/context operations;
- a lightweight background cleanup loop.

Cleanup must:

- find expired active attempts;
- mark them `expired`;
- append an event;
- release the active-attempt uniqueness constraint;
- preserve notes and results.

No issue status rewrite is required.

## 17. Configuration

User-configurable in the first version:

```text
database path override
busy timeout
durability: normal | full
backup directory
log level
```

Keep internal:

```text
journal mode
foreign keys
trusted schema
pool size
page size
cache size
mmap size
autocheckpoint
```

## 18. Logging

Use structured local logging.

Log:

- startup and schema version;
- project resolution;
- migration application;
- domain error codes;
- transaction retries;
- lease expiry;
- backup and health operations.

Never log:

- raw lease tokens;
- sensitive environment variables;
- entire large descriptions by default.


---


# Implementation requirements

## 1. Architecture

Required dependency direction:

```text
MCP adapter      CLI adapter      future HTTP/UI
      \              |              /
             application services
                     |
                   domain
                     |
              repository ports
                     |
               SQLite adapters
```

MCP handlers must not contain domain logic.

Suggested package structure:

```text
cmd/
  rhizome-mcp/

internal/
  domain/
    project.go
    issue.go
    relation.go
    label.go
    comment.go
    decision.go
    attempt.go
    artifact.go
    event.go
    errors.go

  application/
    project_service.go
    issue_service.go
    relation_service.go
    graph_service.go
    planning_service.go
    attempt_service.go
    context_service.go
    search_service.go
    change_service.go

  adapters/
    mcp/
    cli/
    sqlite/

  config/
  migrations/
  clock/
  logging/
```

## 2. Domain services

At minimum, services should separate:

- issue mutations and queries;
- relation validation and cycle detection;
- graph traversal;
- plan validation and batch application;
- attempt lifecycle;
- context assembly;
- search;
- change feed;
- project lifecycle.

## 3. Determinism

Never rely on SQLite's implicit row order.

All lists, graph nodes, graph edges and validation errors must have explicit ordering.

Examples:

Issues:

```text
priority DESC
is_claimable DESC
sequence_no ASC
```

Graph nodes:

```text
depth ASC
sequence_no ASC
```

Validation errors:

```text
entity index
field path
error code
```

Stable ordering reduces context churn and improves reproducibility.

## 4. Graph traversal

Use breadth-first traversal for bounded graph queries.

Rules:

- enforce depth before expanding;
- enforce node limit;
- track visited nodes;
- permit cycles for `related_to`;
- prevent cycles in stored `blocks`;
- normalize edge direction;
- include epic hierarchy as derived `contains` edges;
- report truncation explicitly.

The planning graph is a specialized projection over the same graph service, not a separate implementation.

## 5. Cycle detection

Adding `A blocks B` must reject the relation if a path already exists from `B` to `A`.

Cycle validation must occur inside the same transaction as relation insertion.

Batch plans must validate cycles using:

- existing database relations;
- all relations proposed in the batch.

## 6. Idempotency

Mutation handlers that support idempotency must:

1. Normalize the request.
2. Compute a stable request hash.
3. Look up `(operation, idempotency_key)`.
4. Return saved response for the same hash.
5. Return `IDEMPOTENCY_CONFLICT` for a different hash.
6. Execute mutation and store response atomically.

Batch application requires an idempotency key.

## 7. Optimistic concurrency

Issue patch operations use:

```text
issue_id
expected_version
changes
```

The update must be implemented as a conditional write:

```sql
UPDATE issues
SET ..., version = version + 1
WHERE id = ? AND version = ?;
```

Zero affected rows require distinguishing:

- missing issue;
- archived issue;
- version conflict.

## 8. Claiming and leases

`claim_issue` must be atomic.

Within one short transaction:

1. Resolve and lock the issue for mutation.
2. Expire stale attempts if needed.
3. Validate type and stored status.
4. Validate blockers.
5. Validate no active attempt.
6. Create attempt.
7. Store token hash.
8. Append event.
9. Commit.
10. Return raw token once.

Lease tokens must be generated using a cryptographically secure random source.

Token comparison should avoid trivial timing leakage where practical.

## 9. Completion consistency

When finishing an attempt:

1. Verify token and active status.
2. Verify lease has not expired.
3. Load current issue version.
4. Determine changes since `context_event_id_at_start`.
5. Classify changes as warnings or acknowledgment-required.
6. Validate blockers and archive/cancel state.
7. Validate requested target transition.
8. Save result, verification and artifacts.
9. Finish attempt.
10. Update issue status.
11. Append events.
12. Commit atomically.

A failed completion request must not destroy the active attempt or result data supplied by the caller.

The agent can save a checkpoint and retry after refreshing context.

## 10. Work context assembly

Default context must remain small.

Include by default:

- issue identity;
- title and description;
- acceptance criteria;
- priority and effective status;
- unresolved blockers;
- summaries of active decisions;
- last previous result summary;
- previous next steps;
- latest checkpoint;
- repeated-failure warnings.

Do not include by default:

- all comments;
- all decisions;
- full decision content;
- all notes;
- complete attempt history;
- complete event history;
- full graph.

Support explicit `include` sections and section-specific limits.

## 11. MCP schema quality

Tool descriptions must be short and clearly distinguish tools.

Descriptions should state:

- when to use the tool;
- when a nearby tool is more appropriate.

JSON Schema must contain:

- enums;
- required fields;
- maximum lengths;
- array limits;
- `additionalProperties: false` where practical.

Keep tool registration order stable.

Do not generate schemas dynamically from project state.

## 12. Token and response limits

Baseline input limits:

```text
title: 300 characters
description: 100,000 characters
acceptance criteria: 50,000 characters
comment: 50,000 characters
decision title: 300 characters
decision summary: 2,000 characters
decision content: 100,000 characters
attempt note/checkpoint: 50,000 characters
label name: 64 characters
labels per issue: 50
relations in one operation: 100
batch issues: 50
graph depth: 5
graph nodes: 500
search results: 100
search snippet: 1,000 characters
```

Also enforce:

- valid UTF-8;
- no NUL characters;
- maximum request body size;
- bounded JSON nesting;
- safe artifact URI length.

## 13. Error handling

Define stable domain error types.

Do not expose:

- raw SQL;
- SQLite error text;
- stack traces;
- internal filesystem paths unless relevant and safe.

Every error response includes:

```text
code
message
details
retryable
```

Retryable examples:

- transient storage contention after internal retry exhaustion;
- version conflict;
- issue changed during attempt.

Non-retryable examples:

- invalid transition;
- cycle;
- invalid parent;
- limit exceeded.

## 14. CLI

The first CLI is not a full task management UI.

Minimum useful commands:

```text
rhizome-mcp init
rhizome-mcp serve
rhizome-mcp project info
rhizome-mcp issue list
rhizome-mcp issue show ISSUE-42
rhizome-mcp search <query>
rhizome-mcp graph ISSUE-42
rhizome-mcp doctor
rhizome-mcp backup
```

Useful output formats:

```text
table
json
markdown
mermaid
```

## 15. Testing requirements

### Unit tests

- status transitions;
- claimability;
- parent constraints;
- relation canonicalization;
- cycle detection;
- repeated failure calculation;
- change classification;
- token hashing;
- plan normalization;
- cursor encoding and decoding.

### SQLite integration tests

- migrations from empty database;
- foreign key enforcement;
- active attempt unique index;
- idempotency atomicity;
- optimistic update conflict;
- relation cycle race;
- FTS update and rebuild;
- WAL mode and concurrent readers;
- backup correctness.

### Concurrency tests

- two agents claim the same issue;
- claim races with blocker creation;
- completion races with issue update;
- lease expiry races with renewal;
- duplicate idempotent requests;
- batch application races with ordinary creation.

### Clock-driven tests

- attempt expiry;
- stale session detection;
- repeated failures;
- cleanup loop;
- lease renewal boundaries.

### MCP contract tests

- tool list stability;
- valid input schema;
- valid output schema;
- compact default responses;
- truncation metadata;
- domain error serialization.

## 16. Versioning

Minimum version set:

```text
application version: SemVer
database schema version: migration integer
project config version: integer
MCP protocol version: negotiated by MCP
```

Do not introduce custom per-tool version numbers in the first version.

Breaking tool-contract changes require a major application version.

## 17. Definition of first-version completeness

The first version is complete when:

- a repository can be initialized;
- an MCP client can start the server through stdio;
- issues can be created, updated, listed and archived;
- epics and parent membership work;
- labels and relations work;
- blocker cycles are rejected;
- graphs and planning graphs are bounded;
- an issue can be claimed atomically;
- lease renewal and expiry work;
- a second agent can continue after expiry;
- checkpoints and decisions survive interruption;
- completion detects issue changes;
- batch plans validate and apply atomically;
- search returns ranked snippets;
- changes can be fetched after an event ID;
- backup and doctor commands work;
- all critical concurrency invariants are tested.


---


# Deferred features and implementation-time decisions

## 1. Explicitly deferred features

Do not include these in the first version unless required to satisfy a core invariant.

### User experience

- web UI;
- desktop UI;
- terminal UI;
- visual dashboard;
- interactive graph editor.

### Identity and access

- permanent users;
- permanent agent entities;
- authentication;
- authorization;
- teams and roles;
- remote access security.

### Workflow

- custom statuses;
- custom workflows;
- arbitrary custom fields;
- multiple assignees;
- permanent assignee field;
- nested epics;
- separate subtask type;
- estimates;
- due dates;
- milestones;
- manual rank or backlog ordering.

### Storage and search

- binary attachments;
- built-in file blob storage;
- semantic/vector search;
- external search services;
- PostgreSQL;
- distributed database access;
- multi-node service.

### Automation

- automatic deletion of old events or attempts;
- automatic task blocking after repeated failures;
- remote notifications;
- resource subscriptions;
- background model calls.

## 2. Candidate post-MVP features

These are plausible future additions, not current requirements.

- `open_question` entity;
- logical project import/export format;
- project instruction resources;
- richer review workflow;
- HTTP transport bound to localhost;
- IDE extension;
- GUI dependency graph;
- hybrid FTS and embeddings;
- PostgreSQL backend;
- multi-project dashboard;
- optional permanent agent profiles;
- capability matching.

## 3. Decisions to make during implementation planning

The implementation agent must select and document:

- exact Go version;
- exact MCP Go SDK and version;
- exact `modernc.org/sqlite` version;
- ULID library;
- migration library or custom migration runner;
- cursor encoding format;
- lease default duration and allowed range;
- stale session threshold;
- repeated failure warning threshold;
- exact application data paths per OS;
- logging package;
- CLI package;
- JSON Schema generation strategy;
- transaction helper design;
- token hash algorithm;
- request hash canonicalization;
- backup API implementation.

These choices must not contradict the domain model.

## 4. Suggested defaults requiring confirmation

Reasonable initial defaults:

```text
lease duration: 5 minutes
minimum lease: 30 seconds
maximum lease: 1 hour
session stale threshold: 15 minutes
repeated failure warning: 3 consecutive failed/expired attempts
busy timeout: 5 seconds
graph depth: 2
graph maximum depth: 5
graph default nodes: 100
graph maximum nodes: 500
```

They may be adjusted during planning if justified.

## 5. Known design trade-offs

### No permanent agent identity

Benefits:

- works with heterogeneous clients;
- no registration lifecycle;
- no stale agent cleanup;
- no dependency on clients remembering IDs.

Trade-off:

- history groups actions by sessions and labels rather than a guaranteed long-lived actor.

### No stored `in_progress`

Benefits:

- no permanently stuck issues;
- state follows the active lease;
- recovery is deterministic.

Trade-off:

- queries must compute effective status.

### SQLite

Benefits:

- one binary;
- no service dependency;
- simple backup and portability;
- strong fit for local-first use.

Trade-off:

- one writer at a time;
- not suitable for multi-node deployment;
- WAL requires local filesystem.

### No full UI

Benefits:

- focus on MCP and correctness;
- smaller first version;
- easier packaging.

Trade-off:

- human inspection relies on CLI and agent output.

### Unified activity tool

Benefits:

- smaller tool catalog;
- one paginated timeline interface.

Trade-off:

- output is heterogeneous and requires an `entity_type` discriminator.

## 6. Prohibited silent behavior

The implementation must not silently:

- truncate graphs or lists;
- reuse issue numbers;
- ignore version conflicts;
- overwrite changes from another agent;
- create duplicate active attempts;
- recover expired attempts;
- remove blockers;
- delete history;
- downgrade schemas;
- fall back from WAL without reporting it;
- accept invalid project identity;
- expose raw lease tokens in logs.
