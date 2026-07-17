# Logical project interchange format

## 1. Scope and versioning

This document specifies the version 1 logical interchange format for a
Rhizome project. It is a UTF-8 JSON document, intended for inspection and
transfer between installations. It is not a SQLite backup and does not
preserve database implementation details.

The top-level document is:

```json
{
  "format": "rhizome-logical-project",
  "version": 1,
  "exported_at": "2026-07-17T18:24:06.023717Z",
  "project": {},
  "issues": [],
  "labels": [],
  "issue_labels": [],
  "relations": [],
  "comments": [],
  "decisions": [],
  "attempts": [],
  "attempt_notes": [],
  "artifacts": [],
  "events": []
}
```

`format` and `version` are required. The exporter emits only version `1`.
The importer must reject an unsupported `format` or version before any
mutation with a stable `UNSUPPORTED_FORMAT_VERSION` error. A version 1
document rejects unknown top-level fields and unknown fields in every record
with `UNSUPPORTED_FIELD`; this prevents silently dropping future data.

All timestamps are required RFC 3339 strings in UTC, with fractional seconds
preserved when present. JSON `null` represents a nullable stored value; an
absent field is invalid unless this document says it is optional. JSON objects
must not contain duplicate keys.

## 2. Identity and ordering

Every exported durable ID is a canonical ULID. IDs identify records within
the document and are remapped to newly generated destination IDs on import;
they are never assumed to be valid in the destination project. The stable
logical identity of an issue is its exported `id`, not its display ID.
`display_id` and `sequence_no` are omitted because issue numbers are assigned
by the destination project and may differ after import.

Arrays are deterministic:

- `issues`, `labels`, `comments`, `decisions`, `attempts`, `attempt_notes`,
  and `artifacts`: `created_at` ascending, then `id` ascending;
- `events`: `created_at` ascending, then `source_id` ascending;
- `issue_labels`: `issue_id` ascending, then `label_id` ascending;
- `relations`: `source_issue_id` ascending, `target_issue_id` ascending, then
  `type` ascending.

Object member order is not semantically meaningful. Exporters use the field
order shown below. Arrays are emitted as `[]`, never `null`; nullable values
are emitted as `null`.

## 3. Project and issue records

`project` contains:

```json
{
  "id": "01J...",
  "name": null,
  "instructions": null,
  "created_at": "2026-07-17T18:24:06Z",
  "updated_at": "2026-07-17T18:24:06Z"
}
```

The source project ID and timestamps are historical metadata. Import does not
replace the destination project identity or its existing project row. The
destination may merge `name` and `instructions` only under an explicit future
import policy; version 1 creates an empty destination project from those
values and rejects import into a nonempty project.

Each `issues` record contains all durable issue fields except
`sequence_no`, `display_id`, `version`, `archived_at`, and
`archived_by_session_id`:

```json
{
  "id": "01J...",
  "type": "task",
  "title": "Implement export",
  "description": null,
  "acceptance_criteria": null,
  "status": "ready",
  "priority": "high",
  "parent_id": "01J...",
  "blocked_reason": null,
  "created_by_session_id": null,
  "created_at": "2026-07-17T18:24:06Z",
  "updated_at": "2026-07-17T18:24:06Z",
  "closed_at": null
}
```

Archived issues are excluded entirely, including their issue-owned history.
The importer applies existing enum, parent, and blocked-reason constraints.
It assigns a fresh destination sequence number and version, maps every
non-null `parent_id`, and imports `created_by_session_id` as `null`.

## 4. Supporting entity records

`labels` records contain `id`, `name`, `description`, and `created_at`.
`issue_labels` records contain `issue_id` and `label_id`; they have no
independent ID or timestamp. Label names retain their source spelling, but
case-insensitive uniqueness is validated before import.

`relations` records contain `id`, `source_issue_id`, `target_issue_id`,
`type`, `created_by_session_id`, and `created_at`. The importer maps entity
references, imports `created_by_session_id` as `null`, applies canonical
relation rules, and rejects self-relations, duplicates, and `blocks` cycles.

`comments` records contain `id`, `issue_id`, `content`,
`created_by_session_id`, `author_label`, `created_at`, and `edited_at`.
`created_by_session_id` imports as `null`.

`decisions` records contain `id`, `issue_id`, `title`, `summary`, `content`,
`status`, `supersedes_id`, `created_by_session_id`, and `created_at`.
`supersedes_id` may be null and, when present, must reference an included
decision with the same mapped issue scope. `created_by_session_id` imports as
`null`. All statuses, including historical `superseded` and `rejected`
records, are retained.

`attempts` records contain `id`, `issue_id`, `session_id`, `agent_label`,
`kind`, `status`, `issue_version_at_start`, `context_event_id_at_start`,
`lease_expires_at`, `started_at`, `last_heartbeat_at`, `finished_at`,
`result_summary`, `next_steps`, `verification`, `failure_reason_code`,
`interruption_reason_code`, and `reason_details`.

Raw lease tokens and `lease_token_hash` are never exported. `session_id`
imports as `null`. Imported active attempts are rejected with
`UNSUPPORTED_ACTIVE_ATTEMPT`: an execution lease cannot be transferred
safely. Terminal attempts retain their historical status and timestamps, but
destination issue versions and event positions are not reconstructed from
source values.

`attempt_notes` records contain `id`, `attempt_id`, `kind`, `content`,
`next_steps`, `important`, and `created_at`.

`artifacts` records contain `id`, `issue_id`, `attempt_id`, `type`, `uri`,
`title`, `metadata`, and `created_at`. Artifact content is not embedded.
Repository-relative `file` and `directory` URIs remain relative to the
destination repository; consumers must treat them as potentially missing.

`events` records contain `source_id`, `issue_id`, `event_type`, `session_id`,
`attempt_id`, `payload`, and `created_at`. `source_id` is historical only:
the destination assigns its own monotonic event IDs. Event payloads are
retained as opaque valid JSON after their referenced IDs are remapped where
the event schema names an entity ID; `session_id` imports as `null`. Events
with unknown types or unremappable payload references are rejected rather
than silently corrupted.

## 5. Explicit exclusions

The following are intentionally excluded:

- `.agent-tracker.json`, the destination SQLite database, migration state,
  FTS indexes, idempotency records, and runtime configuration;
- session records and session lifecycle state;
- archived issues and their owned data;
- active attempt leases, raw tokens, token hashes, and heartbeat ownership;
- generated issue display IDs, source row versions, and source event IDs;
- binary artifact content, filesystem contents, absolute local paths, and
  machine-specific credentials;
- SQLite internals, WAL files, query plans, and implementation indexes.

The exclusion of sessions avoids falsely attributing imported history to a
currently connected client. Nullable session references in retained records
are imported as `null`.

## 6. Import validation and atomicity

Import has two phases. The dry-run phase parses JSON with bounded input size,
validates the full document, builds all ID mappings, checks referential
closure and logical constraints, and reports every deterministic validation
error without writing. Records are validated in the array order defined in
section 2; errors identify a JSON path.

The apply phase repeats validation, then writes all records, relations,
history, derived search entries, and destination events in one transaction.
Any storage, mapping, constraint, or search-index failure rolls back the
entire import. A version 1 apply requires an empty destination project to
avoid unspecified merge behavior.

References must target records of the correct included type. The importer
rejects dangling references, invalid ULIDs, duplicate logical IDs,
noncanonical enum values, malformed timestamps, invalid artifact metadata,
and source active attempts. It does not coerce malformed values.

## 7. Compatibility policy

Version 1 is backward-compatible only with other version 1 implementations.
An implementation may add an optional top-level `compatibility` object in a
future version, but it must not add fields to version 1 records. A future
version must define its own required and optional fields, migration behavior,
and whether it can safely down-convert to version 1.

Export remains deterministic for a fixed logical project state. Volatile
values such as export time do not alter record ordering or record content.
Consumers comparing documents should compare normalized entity content and
may ignore `exported_at`.
