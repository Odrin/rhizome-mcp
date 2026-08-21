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

The project root is found by searching upward from the current directory for the default opening path, while project-aware serving can also pin a specific absolute root via `--project-root` or `RHIZOME_PROJECT_ROOT`. Routed opens remain existing-only and do not create or initialize a new project automatically.

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
- idempotency record plus mutation result;
- evaluate workflow gates and snapshot the effective requirement set, as
  part of claim issue and finish attempt (docs/02 §17).

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
workflow_policies
workflow_policy_requirements
workflow_policy_audit_events
```

Requirement snapshots (docs/02 §17.6) are stored alongside the attempt or
review request they belong to, not as a separate table -- a work attempt
carries its own frozen requirement list, and so does a review request.
Exact column layout for policies, requirements, snapshots, evidence
(ISSUE-171), and review approval records (ISSUE-173) is decided by those
implementation tasks, not by this document.

### 7.1. `issue_events` is the single ordered event log

Every durable event -- issue lifecycle and review lifecycle alike -- is one
row in `issue_events`, sharing its single `INTEGER PRIMARY KEY
AUTOINCREMENT` sequence. A `source TEXT NOT NULL DEFAULT 'issue' CHECK
(source IN ('issue', 'review'))` column (migration `008_unify_event_log`)
records provenance only; no query branches on it. A review event's
`issue_id` is populated directly from its review request at insert time
(there is no join to derive it), and its `request_id`/`target_id` remain
available in the JSON `payload`, the same place they were always recorded.
`internal/adapters/sqlite`'s `appendReviewEvent` helper is the single
insertion point every review event append site uses; there was previously
a second, independently-sequenced `review_events` table, which is why
`GetChanges`, `context_event_id_at_start`, the review-target staleness
comparison, and the activity feed's events arm could each disagree about
what "the latest event" meant. Any future event-producing feature (workflow
policy audit events, ISSUE-170; reservation events, ISSUE-178) appends
through this same table and helper pattern rather than introducing another
independently-sequenced `AUTOINCREMENT` table.

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
- attempt note content;
- review status and artifact IDs, under a title denormalized from the
  parent issue's current title (kept in sync by triggers on both tables,
  including a rename of the parent issue).

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

### 15.1. Storage timestamp format

Every `TEXT` timestamp column is written in one fixed-width canonical form:
`2006-01-02T15:04:05.000000000Z` — always UTC, always exactly 9 fractional
digits (30 characters). `internal/adapters/sqlite/timestamps.go`'s
`formatStorageTime`/`parseStorageTime` are the sole source of this format;
every write in `internal/adapters/sqlite` goes through `formatStorageTime`
(or a helper that delegates to it) rather than formatting with
`time.RFC3339Nano` directly.

This matters because SQLite compares `TEXT` with `memcmp`, and this schema's
lease and ordering predicates — `lease_expires_at <= ?`, `lease_expires_at >
?`, `occurred_at`/`created_at` ordering, and keyset pagination cursors —
compare these strings directly in SQL. `time.RFC3339Nano` trims trailing
zero fractional digits, producing variable-width strings (a whole-second
value formats as `...05Z`, a value with a fraction as `...05.5Z` or
`...05.123456789Z`); under `memcmp`, a whole-second value's `Z` terminator
sorts *after* a fractional value's `.`, so `"...05Z" > "...05.1Z"` even
though `05.0` is chronologically earlier. Fixing every value to the same
width removes the ambiguity.

`parseStorageTime` stays lenient on read: it parses via `time.RFC3339Nano`
(which accepts 0 to 9 fractional digits, any width) and separately rejects a
non-UTC offset, so it accepts both the fixed-width form and un-migrated or
externally authored data (older backups, logical-project interchange
imports). Only the write side is fixed-width; the parse side is
permissive by design.

The one deliberate exception is `formatLogicalProjectTimestamp`
(`internal/adapters/sqlite/projects.go`), which renders timestamps for the
logical interchange export document (JSON), not a SQLite column. That
document is never compared via SQL `memcmp`, so it keeps the trimmed
`time.RFC3339Nano` form; widening it would be an interchange
format-version change, not a storage-comparison fix.

Migration `007_fixed_width_timestamps` rewrites every existing `TEXT`
timestamp column (all `NOT NULL` and nullable `_at` columns across the
schema) to the fixed-width form in place, using a `substr`/`instr`-based
SQLite expression rather than a Go-side rewrite — this keeps the migration a
plain, auditable SQL script consistent with every other migration in this
package, and the expression is idempotent (re-applying it to an
already-fixed-width value is a no-op). `schema_migrations.applied_at` is
deliberately left untouched: it is never compared with an inequality
predicate in SQL, so rewriting the migration bookkeeping table's own
historical rows was judged unnecessary risk for zero benefit.

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
