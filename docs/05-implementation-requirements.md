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
