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

## 3. MVP implementation choices

These choices were resolved during MVP implementation and are preserved in the active MCP decision named `Implementation baseline`:

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

## 4. Confirmed MVP defaults

The MVP uses:

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

Future changes must be recorded as a superseding MCP decision when they affect public contracts or invariants.

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
