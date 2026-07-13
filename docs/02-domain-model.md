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
