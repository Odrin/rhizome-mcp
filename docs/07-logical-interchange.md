# Logical project interchange format

## 1. Scope and versioning

This document specifies the logical interchange format for a Rhizome
project, versions 1 and 2. It is a UTF-8 JSON document, intended for
inspection and transfer between installations. It is not a SQLite backup and
does not preserve database implementation details.

MCP delivery is separate from this logical format: `export_project` normally
returns a managed artifact URI, while `delivery: "inline"` returns the document
when it is within the MCP inline limit. Artifact URIs are local runtime
capabilities and are not fields in this document.

The top-level document (version 2) is:

```json
{
  "format": "rhizome-logical-project",
  "version": 2,
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
  "events": [],
  "review_targets": [],
  "review_requests": [],
  "review_outcomes": [],
  "review_events": [],
  "extensions": {}
}
```

A version 1 document is the same shape without the last five fields.

`format` and `version` are required. The exporter emits version `2`. The
importer accepts both version `1` and version `2` and rejects any other
version, or an unsupported `format`, before any mutation with a stable
`UNSUPPORTED_FORMAT_VERSION` error. Both versions reject unknown top-level
fields and unknown fields in every record with `UNSUPPORTED_FIELD`; which
top-level fields are known depends on the document's own declared `version`
(see §7) -- this prevents silently dropping future data, and prevents a
version 1 document from smuggling in version-2-only fields.

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
  `artifacts`, `review_targets`, `review_requests`, and `review_outcomes`:
  `created_at` ascending, then `id` ascending;
- `events` and `review_events`: `created_at` ascending, then `source_id`
  ascending;
- `issue_labels`: `issue_id` ascending, then `label_id` ascending;
- `relations`: `source_issue_id` ascending, `target_issue_id` ascending, then
  `type` ascending.

Object member order is not semantically meaningful. Exporters use the field
order shown below. A version 1 field, and every version 2 array that is
required in the version it belongs to, is emitted as `[]`, never `null` or
absent, even when empty; nullable values are emitted as `null`. The five
version 2 fields (`review_targets`, `review_requests`, `review_outcomes`,
`review_events`, `extensions`) are the exception: each is *optional*, and is
omitted entirely rather than emitted empty when there is nothing to report
(see §7) -- an absent version 2 field means empty, not unset.

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

Raw lease tokens and `lease_token_hash` are never exported. Active attempts
are excluded with their attempt notes, attempt-owned artifacts, and
attempt-referencing events: an execution lease cannot be transferred safely.
`session_id` imports as `null`. Terminal attempts retain their historical
status and timestamps, but destination issue versions and event positions are
not reconstructed from source values.

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
than silently corrupted. `events` includes review-workflow event types
(`review_requested`, `review_claimed`, `review_approved`,
`review_changes_requested`, `review_blocked`, `review_cancelled`,
`review_superseded`) alongside issue-sourced ones; the record carries no
field distinguishing the two on import, so a review-sourced event is not
reconstructed as one on re-export -- if that distinction matters, read
`review_events` instead (below), which is scoped to review-sourced rows
specifically.

The following four record types are version 2 only.

`review_targets` records contain `id`, `issue_id`, `issue_version`,
`latest_event_id`, `artifact_ids`, and `created_at`: the immutable snapshot a
review request freezes at claim time.

`review_requests` records contain `id`, `target_id`, `issue_id`,
`target_issue_version`, `target_event_id`, `artifact_ids`, `status`,
`supersedes_id`, `created_at`, and `resolved_at`. `target_id` must reference
an included `review_targets` record; `supersedes_id`, when present, must
reference another included `review_requests` record. A request whose status
is `claimed` -- bound to a currently active review attempt -- is excluded
the same way an active work attempt is excluded from `attempts`: a claim
cannot be transferred safely.

`review_outcomes` records contain `id`, `request_id`, `attempt_id`,
`outcome`, `reason`, and `created_at`. `request_id` must reference an
included `review_requests` record; `attempt_id` must reference an included
`attempts` record. `reason` is required when `outcome` is `blocked` and
absent otherwise.

`review_events` records contain `source_id`, `request_id`, `target_id`,
`attempt_id`, `event_type`, `payload`, and `created_at`: the same
review-sourced rows already present in `events` (see above), re-shaped with
`request_id`/`target_id` promoted out of the opaque payload into their own
fields. It exists for tools and humans that want the review workflow's
history scoped and typed without parsing every event's payload; it is
export-only and read-only -- an importer replays `events`, which already
contains every one of these rows, and must not also replay `review_events`,
or every review event would be duplicated. Referential integrity
(`request_id` and `target_id` must resolve to included `review_requests` /
`review_targets` records) is still validated on parse regardless.

`extensions` is a reserved top-level object, present only in version 2. Its
values are namespaced by owning feature (for example a future `gates` or
`reservations` key) and are not otherwise interpreted or validated by this
format; each namespace defines and validates its own value shape. See §7 for
when a feature should add a namespace to `extensions` versus a new top-level
array.

## 5. Explicit exclusions

The following are intentionally excluded:

- `.agent-tracker.json`, the destination SQLite database, migration state,
  FTS indexes, idempotency records, and runtime configuration;
- session records and session lifecycle state;
- archived issues and their owned data;
- active attempts, their leases, raw tokens, token hashes, heartbeat
  ownership, and their dependent history;
- claimed review requests and their active review attempt binding (version
  2 only -- see §4);
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
entire import. An apply requires an empty destination project to avoid
unspecified merge behavior, regardless of the document's version.

References must target records of the correct included type. The importer
rejects dangling references, invalid ULIDs, duplicate logical IDs,
noncanonical enum values, malformed timestamps, invalid artifact metadata,
and source active attempts. It does not coerce malformed values.

## 7. Compatibility policy

Version 1's fields are frozen: no future version may add a field to a
version 1 record or change a version 1 field's meaning. version 1 documents
remain importable indefinitely.

Version 2 is version 1 plus five additional, wholly optional top-level
fields: `review_targets`, `review_requests`, `review_outcomes`,
`review_events`, and `extensions` (§4). "Optional" here means precisely: the
importer's required-field check for a version 2 document is the same set
required for version 1 (§1); a version 2 document that omits any of the five
is valid, and an absent field means the same thing an empty array (or empty
object, for `extensions`) would mean -- there is nothing to report, not that
the exporter forgot to ask. This is deliberate, not incidental: it is what
lets a version 2 importer accept a version 1 document without a separate
code path, and what lets a version 2 exporter with nothing to put in one of
these fields omit it rather than pad it out.

New work that needs to carry additional interchange data has two options,
in order of preference:

1. **Add a namespace to `extensions`.** This is the default for anything
   that does not need referential integration with the entity records in §3
   and §4 -- a self-contained blob a feature can define, validate, and
   evolve on its own schedule without touching the shared version tables in
   `internal/domain/logical_project_import.go` at all. Pick a namespace key
   that names the owning feature (e.g. `"gates"`, `"reservations"`) and
   document its shape wherever that feature's own contract lives; this
   document does not need to change.
2. **Add a new top-level array to the shared version 2 definition**, when
   the data genuinely needs entity records with IDs that participate in the
   same reference-remapping and referential-integrity machinery every other
   entity type uses (the way `review_targets`/`review_requests`/
   `review_outcomes` need to reference `issues` and each other). This means
   adding the array to `logicalProjectDocumentKeysV2` (and the matching
   struct fields, semantic validation, export query, and import insertion),
   coordinating with whoever else is also extending version 2 at the same
   time -- **not** independently declaring a new "version 2" or bumping to
   version 3. ISSUE-175 (workflow gates) and ISSUE-182 (reservations) are
   the two features expected to do this next; both add sections to this
   same shared version 2 definition rather than each claiming a version
   number of their own (see ISSUE-215, which this section's policy exists
   to settle).

A version 3 is warranted only when a change cannot be expressed as an
addition under version 2 -- for instance, changing the meaning or required
status of an existing field. A future version must define its own required
and optional fields, migration behavior, and whether it can safely
down-convert.

Export remains deterministic for a fixed logical project state. Volatile
values such as export time do not alter record ordering or record content.
Consumers comparing documents should compare normalized entity content and
may ignore `exported_at`.
