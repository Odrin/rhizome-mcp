# Implementation plan

This is the living roadmap for the first release. It records current capability, remaining order, and exit gates. Git history and tests are the detailed completion record; do not turn this file into a chronological command log.

## Delivery rules

- Define work as coherent, independently testable vertical slices.
- Run two ready implementation units concurrently when they have disjoint write and package/test scopes, no contract or ordering dependency, and independent focused checks; use at most three workers.
- Keep shared APIs, domain types, ports, schemas, migrations, registries, roadmap edits, integration validation, and commits serialized.
- Split a slice when it contains multiple novel contracts or transactions, not solely because it spans several layers.
- Add focused tests with every production change and apply the validation tiers below.
- Keep domain logic out of adapters, transactions short, ordering deterministic, and responses explicitly bounded.
- Do not add deferred features.

## Current state

- **Active phase:** Phase 6, search and changes.
- **Next unit:** complete transactional FTS5 indexing and rebuild support.
- **Then:** implement ranked bounded search and event-based incremental changes, and expose `search` and `get_changes`.
- **Later:** Phase 7 CLI, maintenance, and release.

## Phase 0: decisions and workflow - complete

The implementation baseline is recorded in `docs/decisions/0001-implementation-baseline.md`, and the workspace uses an orchestrator plus a bounded low-cost implementer.

## Phase 1: foundation - complete

Delivered package boundaries, injected time and IDs, validation and pagination, strict project identity, configured SQLite, embedded checksummed migrations, health checks, FTS5 availability, and the initial strict schema.

Exit gate satisfied: initialization, migration, reopen, WAL, foreign keys, checksums, active-attempt uniqueness, integrity checks, and no-CGO operation.

## Phase 2: projects and issues - complete

Delivered project, issue, label, and event layers plus `get_project`, `list_labels`, `create_issue`, `update_issue`, `get_issue`, `list_issues`, and `archive_issue`. This includes immutable issue numbers, parent rules, effective status, archive behavior, optimistic versions, and deterministic pagination.

Exit gate satisfied: the issue lifecycle works through MCP and survives restart, including invalid transitions and parents, archives, and version conflicts.

## Phase 3: relations, graphs, and planning - complete

Delivered canonical relation management, in-transaction blocker-cycle rejection, shared bounded breadth-first graph traversal, planning projections, deterministic plan validation, and atomic idempotent plan application. Tools: `manage_issue_relation`, `get_issue_graph`, `get_planning_graph`, `validate_issue_plan`, and `apply_issue_plan`.

Exit gate satisfied: graph bounds, relation races, cycle rejection, deterministic validation, rollback, and concurrent idempotent application.

## Phase 4: sessions, claims, leases, and recovery - complete

Delivered durable connection sessions; atomic claims and renewals; hashed opaque tokens; lazy and background expiry; takeover; notes, checkpoints, and artifacts; completion consistency; review outcomes; session attribution; and idempotent finish replay. Tools: `claim_issue`, `renew_attempt`, `save_attempt_note`, and `finish_attempt`.

Exit gate satisfied under the race detector: simultaneous claims, blocker/claim, expiry/renewal, completion/update, invalid token, interruption, and takeover. No issue remains permanently locked.

## Phase 5: knowledge and work context - complete

Delivered:

- Append-only `add_comment` with compact event and session attribution.
- Project- and issue-level `record_decision` with atomic same-scope supersession.
- Unified, snapshot-consistent `get_issue_activity` across six categories with bounded deterministic pagination and no lease-secret exposure.
- Compact work-context storage for issue state, blockers, active decision summaries, the latest recovery-relevant attempt, checkpoint, and repeated-failure warnings.
- Include-gated parent epic, project instructions, direct relations, and bounded related-issue summaries.
- Include-gated, bounded recent comments and attempt notes with deterministic newest-first ordering and per-section truncation.
- Include-gated, bounded active issue decision content with deterministic newest-first ordering and per-section truncation.
- Include-gated, bounded attempt history with deterministic newest-first ordering, full persisted attempt metadata, and no lease-token exposure.
- Include-gated, bounded artifacts with deterministic newest-first ordering and full validated metadata.
- Include-gated, bounded chronological issue changes since the latest recovery-relevant attempt boundary.
- Thin MCP `get_work_context` with closed bounded schemas, typed mappings, compact defaults, and no lease-secret exposure.

Exit gate satisfied: decisions and checkpoints survive interruption; every optional section is explicitly bounded and reports truncation; and default context is compact and deterministic. The focused contract coverage and repository-wide standard, race, vet, and no-CGO suites pass.

## Phase 6: search and changes - planned

- Complete transactional FTS5 indexing and rebuild support.
- Implement ranked bounded snippets, deterministic tie-breaking, filters, and event-based incremental changes.
- Deliver `search` and `get_changes`.

Exit gate: transaction visibility, rebuild equivalence, malformed cursor handling, filtering, and incremental refresh pass.

## Phase 7: CLI, maintenance, and release - planned

- Deliver `init`, `serve`, `project info`, `issue list`, `issue show`, `search`, `graph`, `doctor`, and `backup` over shared application services.
- Add maintenance-only attempt release and FTS rebuild operations.
- Finalize stdio startup, stderr-only logs, shutdown, backup validation, documentation, packaging, and target-platform builds.

Exit gate: exactly 22 tools are stable; fresh initialization, stdio MCP use, doctor, backup, and a two-agent lease-expiry handoff succeed; the full release suite passes.

## Validation tiers

### Worker check

For each delegated patch, run formatting plus the smallest test that can falsify the requested behavior. The worker runs only commands named in its brief.

### Slice acceptance

After review, run affected package and integration tests. Run `go test -count=1 ./...` when a slice changes a shared contract, schema, migration, or cross-layer wiring. Add a focused race test for concurrency-sensitive work and clock-driven boundary tests for time behavior. Do not rerun an identical successful command without a reason.

### Phase checkpoint

Run the expensive repository-wide gate once when closing a phase or preparing a release:

```text
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
CGO_ENABLED=0 go test -count=1 ./...
```

Also run any phase-specific migration, MCP contract, CLI, backup, or target-platform checks required by `docs/05-implementation-requirements.md`.

## Roadmap maintenance

- Update Current state and the affected phase after accepted behavior changes.
- Replace stale status instead of appending a dated narrative or repeated command list.
- Preserve phase ordering and exit gates; never weaken a gate to match incomplete work.
- Record durable architectural choices in an ADR, not in milestone prose.