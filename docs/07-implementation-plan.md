# Implementation plan

This roadmap delivers the complete first version as small, independently verified vertical slices. The primary orchestrator owns architecture, schema sequencing, shared contracts, integration, and acceptance. The Go MCP specialist implements one bounded batch at a time on the shared branch.

## Delivery rules

- Serialize production edits to minimize merge and contract rework.
- Parallelize only independent read-only research or review.
- Keep each batch to one service or one to three tightly related tools.
- Add focused tests with every production change.
- Run focused tests, all tests, and `go vet` before checkpointing.
- Do not introduce deferred features.

## Phase 0: decisions and workflow

- Record implementation decisions in `docs/decisions/0001-implementation-baseline.md`.
- Configure the workspace orchestrator and bounded implementation specialist.
- Treat the existing in-memory task implementation as a disposable prototype, not a compatibility contract.

## Phase 1: foundation

- Introduce the required adapter, application, domain, ports, and SQLite package boundaries.
- Add injected time, ULID generation, domain errors, validation, pagination, and deterministic normalization.
- Implement strict project identity discovery and initialization.
- Implement SQLite connection configuration, WAL verification, transaction retry, embedded migrations, health checks, and the complete initial schema.

Exit gate: a temporary repository can be initialized, migrated, reopened, and verified with WAL, FTS5, foreign keys, migration checksums, active-attempt uniqueness, and a no-CGO build.

**Phase 1 completed on 2026-07-13.** Verified exit gate:

- temporary repository initialization, nested discovery, external database migration, project seeding, clean close, and stable reopen;
- WAL, FTS5, per-connection foreign keys, migration version/checksum history, quick and foreign-key checks, and one-active-attempt uniqueness;
- race-enabled full tests, vetting, and `CGO_ENABLED=0 go test ./...`.

## Phase 2: projects and issues

- Implement project, issue, label, and event domain/repository/application layers.
- Add immutable `ISSUE-N` allocation, parent rules, stored and effective status, archives, optimistic versions, and deterministic pagination.
- Deliver `get_project`, `list_labels`, `create_issue`, `update_issue`, `get_issue`, `list_issues`, and `archive_issue`.

Exit gate: the issue lifecycle works through an MCP client and survives restart, including invalid transitions, invalid parents, archive behavior, and version conflicts.

## Phase 3: relations, graphs, and planning

- Implement canonical relations, transactional blocker-cycle detection, and blocker-derived claimability.
- Build one bounded breadth-first graph engine reused by issue and planning projections.
- Implement deterministic plan validation, atomic application, and idempotent response replay.
- Deliver `manage_issue_relation`, `get_issue_graph`, `get_planning_graph`, `validate_issue_plan`, and `apply_issue_plan`.

Exit gate: graph bounds, relation races, cycle rejection, deterministic validation, rollback, and concurrent idempotent application pass.

## Phase 4: sessions, claims, leases, and recovery

- Implement temporary sessions and work attempts using injected time and hashed opaque tokens.
- Implement atomic claim, renewal, lazy expiry, periodic cleanup, checkpoints, completion consistency, verification, and artifacts.
- Deliver `claim_issue`, `renew_attempt`, `save_attempt_note`, and `finish_attempt`.

Exit gate: simultaneous claim, blocker/claim, expiry/renewal, completion/update, token, interruption, and takeover tests pass under the race detector. No issue can remain permanently locked.

## Phase 5: knowledge and work context

- Implement append-only comments, decisions and supersession, artifacts, and unified activity.
- Implement compact default work context with explicitly bounded optional sections.
- Deliver `add_comment`, `record_decision`, `get_issue_activity`, and `get_work_context`.

Exit gate: decisions and checkpoints survive interruption and default context remains compact and deterministic.

## Phase 6: search and changes

- Complete transactional FTS5 indexing and rebuild support.
- Implement ranked bounded snippets, deterministic tie-breaking, filters, and event-based incremental changes.
- Deliver `search` and `get_changes`.

Exit gate: transaction visibility, rebuild equivalence, malformed cursor handling, filtering, and incremental refresh pass.

## Phase 7: CLI, maintenance, and release

- Deliver `init`, `serve`, `project info`, `issue list`, `issue show`, `search`, `graph`, `doctor`, and `backup` over the same application services.
- Add maintenance-only attempt release and FTS rebuild operations.
- Finalize stdio startup, stderr-only logs, shutdown, backup validation, documentation, and packaging.

Exit gate: exactly 22 tools are stable; all tests, race tests, vetting, no-CGO and target-platform builds pass; fresh initialization, stdio MCP use, doctor, backup, and a two-agent lease-expiry handoff succeed.

## Required verification

Each milestone must cover the applicable unit, SQLite integration, clock-driven, concurrency, MCP contract, and CLI tests listed in `docs/05-implementation-requirements.md`. Lists and graphs must report truncation, all queries must order explicitly, and error responses must expose stable domain errors rather than SQLite details.