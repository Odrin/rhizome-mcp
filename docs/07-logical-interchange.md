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

Import caps the document at 1 MiB
(`domain.MaxLogicalProjectImportBytes`). This is a guard against a
malicious or runaway payload, not a capacity plan for any one section:
every entity type -- comments, events, and the `extensions` namespaces
alike -- shares that one budget, and the cap has deliberately not been
raised as sections were added. A project large enough to exceed it needs a
streaming importer, not a bigger constant.

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

A version 1 document is the same shape without the last five fields, and
without the optional `source` field on `events` records (§4) — those six
additions are the whole of version 2.

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
`sequence_no`, `display_id`, `archived_at`, and `archived_by_session_id`:

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
  "closed_at": null,
  "version": 3
}
```

Archived issues are excluded entirely, including their issue-owned history.
The importer applies existing enum, parent, and blocked-reason constraints.
It assigns a fresh destination sequence number, maps every non-null
`parent_id`, and imports `created_by_session_id` as `null`.

`version` is the issue's optimistic-concurrency version, restored verbatim.
It is **version 2 only** and optional: it is omitted when it equals `1`, and
a version 1 record shape rejects the key outright. An issue that carries no
`version` is restored at version `1` — the behavior every import had before
the field existed.

The version is carried because it is not private bookkeeping. Review targets,
review requests, gate snapshots, review approvals, and attempts each freeze
the issue version they observed, and review-target staleness is decided by
comparing a frozen version with the issue's current one (docs/09). Restoring
every issue at version `1` while preserving those frozen numbers made a
review request that was fresh and claimable at export arrive permanently
stale (ISSUE-230). Rewriting the frozen numbers downward instead was
rejected: they are immutable audit facts, and a gate snapshot's fingerprint
is computed over a payload containing one (§4.1).

Once a record states its issue's version, the importer enforces the ceiling
that follows from it: no `review_targets[].issue_version`,
`review_requests[].target_issue_version`, `attempts[].issue_version_at_start`,
gate snapshot `issue_version`, or `review_approvals[].target_issue_version`
for that issue may exceed it, because the destination could never reach that
position. The check is skipped for an issue that states no version, so a
version 1 document, or a version 2 document written before this field
existed, keeps importing exactly as it always did.

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
`attempt_id`, `payload`, and `created_at`. `source_id` is not a destination
identity — the destination assigns its own monotonic event IDs — but it is
not inert either: it is the key the importer remaps every event-log cursor
through (see "Event cursor remapping" below). Event payloads are
retained as opaque valid JSON after their referenced IDs are remapped where
the event schema names an entity ID; `session_id` imports as `null`. Events
with unknown types or unremappable payload references are rejected rather
than silently corrupted. `events` includes review-workflow event types
(`review_requested`, `review_claimed`, `review_approved`,
`review_changes_requested`, `review_blocked`, `review_cancelled`,
`review_superseded`) alongside issue-sourced ones.

`events` records additionally carry `source` in **version 2 only**: either
`"issue"` or `"review"`, mirroring the unified event log's own `source`
column. It is optional — an absent `source` imports as `"issue"` — and a
version 1 document may not carry it at all (version 1 predates the unified
log and cannot express the distinction, so its frozen record shape rejects
the key). A version 2 exporter always emits it.

This tag is not cosmetic and must not be dropped by a consumer that
rewrites documents: review-target staleness is evaluated against
issue-sourced events only (docs/09), so review events restored without
their tag would count as reviewed work changing, and a restored project
would resolve reviews differently from the original it was exported from.
For a typed view scoped to review-sourced rows specifically, read
`review_events` (below).

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
values are namespaced by owning feature (currently `reservations` and
`gates`) and are not otherwise interpreted or validated by this format; each
namespace defines and validates its own value shape. See §7 for when a
feature should add a namespace to `extensions` versus a new top-level array.

### 4.1. The `gates` namespace (ISSUE-175)

`extensions.gates` carries the durable workflow-gate state of docs/02 §17,
with its own namespace `version` (currently 1): `policies` and
`policy_events` (the policy table and its append-only audit trail),
`attempt_snapshots` and `review_target_snapshots` (the frozen requirement
snapshots of docs/02 §17.6), `evidence` and `evidence_events`
(attempt-authenticated gate evidence and its audit trail), and
`review_approvals` (the immutable purpose-scoped approvals of docs/02
§17.5). The namespace is emitted only when at least one record exists, so a
project that never configured a gate exports exactly the document it
exported before.

Attempt-owned records (attempt snapshots, evidence, evidence events) follow
the reservations rule: only rows whose owning attempt itself crosses the
boundary are exported. Approvals additionally require their approving
attempt to be exported. Policy selector/requirement blobs and both snapshot
payloads are carried verbatim and re-inserted verbatim: a snapshot's
fingerprint is computed over its canonical payload, so rewriting the
embedded policy identities to destination IDs would falsify it. Embedded
policy IDs inside snapshots are therefore frozen audit identities naming the
*source* document's policies -- the same rule event payloads already follow
-- while every reference *column* (attempt, target, request, issue, policy)
is remapped like any other import.

Review targets and requests additionally carry an optional `purposes` field
(docs/02 §17.5), omitted when it equals the `["implementation"]`
compatibility default. On import, an absent `purposes` and an absent `gates`
namespace both produce exactly what a pre-gates project holds after
migration 012: implementation-only purposes, no policies, and -- for every
imported review target without a carried snapshot -- the same empty sentinel
snapshot the migration backfill writes, which evaluates identically to the
row's absence (zero requirements). A version 1 document therefore imports
with no gate state and unchanged behavior.

### 4.2. Event cursor remapping

Four durable columns name a position in the event log rather than an entity:
`review_targets.latest_event_id`, `review_requests.target_event_id`,
`attempts[].context_event_id_at_start`, and, in the `gates` namespace,
`review_approvals[].target_event_id`. Each answers one question — *has
anything happened after this position?* — and review-target staleness is
decided by asking it (docs/09).

Imported events receive fresh destination IDs, so a cursor carried verbatim
answers that question against a log it does not belong to. When the source
log has run past the destination's, the cursor sits above every ID the
destination will ever assign and the answer is "no" forever: an imported
review request stays claimable no matter what happens to the reviewed issue
afterwards (ISSUE-231).

The importer therefore builds a source-to-destination event ID mapping while
it replays `events`, and translates all four cursors through it. The rule is
a floor: a cursor becomes the highest destination ID among the events whose
`source_id` is at or below it, and `0` when no exported event is. A cursor of
`0` — "nothing has happened yet" — stays `0`.

The floor rule is what makes sparse documents well-defined, and documents are
sparse by construction: archived issues and active attempts are excluded with
their events (§5), so a cursor naming an event the document does not carry is
ordinary rather than exceptional. It also covers a version 1 document
unchanged: version 1 has `events` and `attempts` but no review entities, so
only `context_event_id_at_start` is remapped, by the same rule. A document
with no events at all maps every cursor to `0`, which is correct — nothing
has been accounted for, so any later destination activity counts.

## 5. Explicit exclusions

The following are intentionally excluded:

- `.agent-tracker.json`, the destination SQLite database, migration state,
  FTS indexes, idempotency records, and runtime configuration;
- session records and session lifecycle state;
- archived issues and their owned data;
- active attempts, their leases, raw tokens, token hashes, heartbeat
  ownership, and their dependent history;
- active resource reservations, and any reservation whose owning attempt is
  still active (version 2 only -- see docs/02 §18.7);
- claimed review requests and their active review attempt binding (version
  2 only -- see §4);
- generated issue display IDs, source event IDs as destination identities
  (they survive only as `events[].source_id`, the key cursors are remapped
  through -- §4.2), and every source row version other than the issue version
  a version 2 document carries (§3);
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
`review_events`, and `extensions` (§4), plus two optional fields *within*
existing records: `events[].source` (§4) and `issues[].version` (§3). That
second kind of addition is permitted only because version 1's own record
shape is untouched — a version 1 document still rejects the key — which is
what the per-version key table in
`internal/domain/logical_project_import.go` exists to enforce.
"Optional" here means precisely: the
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
   version 3.

Whichever option a feature takes, it adds to this same shared version 2
definition rather than claiming a version number of its own -- see
ISSUE-215, which this section's policy exists to settle. ISSUE-182
(reservations) took option 1: reservations are self-contained history that
needs no new reference-remapping machinery, so they ride in
`extensions.reservations` with their own namespace version, and their shape
is documented in docs/02 §18.7. ISSUE-175 (workflow gates) is the other
feature expected to extend version 2.

A version 3 is warranted only when a change cannot be expressed as an
addition under version 2 -- for instance, changing the meaning or required
status of an existing field. A future version must define its own required
and optional fields, migration behavior, and whether it can safely
down-convert.

Export remains deterministic for a fixed logical project state. Volatile
values such as export time do not alter record ordering or record content.
Consumers comparing documents should compare normalized entity content and
may ignore `exported_at`.
