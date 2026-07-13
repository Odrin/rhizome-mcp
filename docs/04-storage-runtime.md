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
