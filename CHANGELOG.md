# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Configurable workflow gates** — Projects can define versioned workflow policies that require acceptance criteria, named attempt evidence (implementation, tests, or any structured key), and purpose-scoped review approvals (e.g. security) before work moves. Policies select issues by type and labels, compose additively, and are enforced atomically inside the same transactions that claim work and change issue status at four fixed points (`claim_work`, `complete_work_to_review`, `complete_work_to_done`, `approve_review`); an unmet requirement fails with a structured `WORKFLOW_GATE_UNSATISFIED` error naming each requirement and reason. Claiming freezes an immutable requirement snapshot per attempt (and per review target at request creation), so editing a policy never moves an in-flight attempt's gates. Persisted by migrations `009`–`010` and `012` with append-only audit trails throughout.

- **Gate tools and diagnostics** — New `manage_workflow_policy` (create/update/archive), `get_workflow_policy`, and `list_workflow_policies` tools administer policies in a new governance capability group that the agent tool profile deliberately excludes: an agent works within the gates a maintainer sets. Agents keep `submit_gate_evidence` (lease-authenticated, idempotent upsert per attempt and key) and the read-only `evaluate_gates` diagnostic, which reports what any enforcement point would decide — same evaluator, no authority to transition state.

- **Gate state across every surface** — `get_work_context` always carries a compact `gates` summary (enforcement point derived from issue state, snapshot fingerprint, satisfied/requirement counts, unmet keys with reasons and imperative next actions). The board shows per-attempt gate progress joined to active attempts, and the served issue-detail page renders the full summary with next actions; gate state participates in the board's semantic ETag. Logical interchange carries policies, audit events, both snapshot kinds, evidence, and purpose-scoped approvals in a new `extensions.gates` namespace (no format version bump; v1 and pre-gates v2 documents import unchanged with implementation-only review purposes). Migration `014` indexes policy requirement identifiers and evidence summaries/details for full-text search (`workflow_policy` and `gate_evidence` entity types), `rebuild-search-index` reproduces the trigger-maintained rows, the activity feed includes gate evidence, and `doctor` gains three gate invariants (policy payload shape, review targets without a frozen snapshot, evidence/attempt issue consistency). End-to-end integration tests prove the acceptance-criteria, implementation, tests, and security-review gates through the real MCP surface. A project with zero policies sees no behavior change.

- **Resource reservations** — `claim_issue` accepts an optional `resources` field to atomically reserve files, directories, globs, or logical resources alongside a claim, all-or-nothing. New `reserve_resources`, `release_resources`, `list_resource_reservations`, and `get_resource_reservation` MCP tools manage reservations on an active work attempt directly. Only work attempts may hold reservations; a conflict with another attempt's active reservation fails with `RESOURCE_RESERVATION_CONFLICT`.

- **Reservation visibility in work context and the board** — `get_work_context` gains an `active_reservation_count`/`conflict_count` default summary plus optional `resource_reservations` (the issue's own reservations, active and released) and `reservation_conflicts` sections; the latter diagnoses a caller-supplied `desired_resources` set against active reservations elsewhere in the project without acquiring anything. The status board (served and offline snapshot) shows an active-reservation count and lists active reservations grouped under their owning attempt; the served issue detail page adds a current/historical reservations section. The board's JSON API, CLI table output, and semantic ETag all carry reservation data too, so the served board's live-refresh poll detects acquire/release.

- **Reservations across the durable surfaces** — Reservations are persisted by migration `011` and indexed for full-text search by migration `013`; their authority is derived from the owning work attempt's lease (no separate reservation clock), so renewal extends it and completion, failure, interruption, force release, or expiry releases it with an auditable reason. Logical interchange version 2 carries released reservations in its extensions map (an active reservation is rejected on import), the activity feed gains a `reservation` entity kind, and `doctor` checks reservation integrity (`active_reservations_without_live_attempt`, `reservation_release_state_consistency`).

- **Reservation guarantees documented** — New [docs/12-resource-reservations.md](docs/12-resource-reservations.md) states what a reservation does and does not guarantee (exclusivity and all-or-nothing acquisition between attempts; no filesystem lock against processes that bypass the server, and no authorization boundary), the acquire/inspect/release lifecycle with every release reason, resource-set selection rules, and the canonical two-agent conflict/expiry/reacquisition scenario. The `rhizome://guides/agent-workflow` MCP guide, `SECURITY.md`, `AGENT_BRIEF.md`, `SPEC.md`, and the README spec index cover reservations too. Cross-process integration tests (`integration/reservation_race_test.go`) prove one-winner acquisition across separate server processes for exact, ASCII-case-folded, ancestor-directory, glob, and logical overlaps, that a loser writes nothing at all, and that a killed agent's expired lease frees its reservations for reacquisition.

### Changed

- **Tool catalog compaction with enforced byte budgets** — The advertised `tools/list` catalog dropped from ~129 KiB to ~96 KiB (roughly a quarter of every agent session's fixed prompt cost) with no loss of caller-facing information: advertised output schemas are now compact projections of the strict validation schemas (per-object `required` arrays and `additionalProperties` markers stripped, `oneOf` advertised as `anyOf`, legacy `view: "full"` payload branches and the inline export document collapsed to one-line described objects), while field names, types, nullability, enums, and descriptions are advertised in full and input schemas are unchanged. The strict schemas remain the validation contract — the output-conformance suite still validates every real response against them. The catalog now carries the same budget-plus-test treatment as response payloads: 112 KiB total and 16 KiB per tool, enforced by new adapter tests (docs/03 §3 "Catalog budget").

- **`list_issues` compact items include `version`** — The default compact projection now carries the issue's optimistic-concurrency `version`, so the natural list-then-mutate flow (`update_issue`, `archive_issue`, and every other `expected_version` precondition) no longer needs an interposed `get_issue` call just to learn it. The 100-issue compact listing stays well within its 64 KiB budget (~48 KB measured).

- **Architectural review follow-ups (2026-08)** — The 2026-08 architectural review closed with all 33 follow-up issues done. Lifecycle policy moved out of the SQLite adapter into `internal/domain` (claimability, finish targets, change classification, cycle detection), backed by shared attempt-termination, lease-authentication and unit-of-work helpers, a collapsed service bundle, and adapter enums/limits derived from domain rather than hand-copied. `issue_events` and `review_events` became one ordered project event log, so `get_changes` cursors over a single strictly increasing sequence. The injected clock is now threaded through service composition and every ID generator, and timestamps are stored fixed-width so SQL lease and ordering comparisons are correct for whole-second values. Supporting hygiene landed too: namespaced configuration keys, a portable project-root-discovering `connect`, a single CLI dispatch/usage contract, board scope brought into the docs, CI race and instrumented-coverage gates, verified release artifacts, and single-sourced agent workflow guides whose tool counts and inventory are pinned to the live catalog by tests.

### Fixed

- **Annotation matrix doc drift** — docs/03 §4.1 still listed the removed `create_review_request` and `supersede_review_request` tools and was missing the seven tools added since (both agent-session tools, the three workflow-policy tools, and both gate tools); section 4's prose referenced the removed tools too. The matrix now matches the live catalog exactly and is kept from drifting by a new test that parses the documented table and asserts it against the in-code annotation matrix (itself asserted against `tools/list`), mirroring the error-code catalog's drift test.

- **Review-target staleness is enforced before a reviewer is committed** — Staleness was detected only at review completion, so a request could be created against a target that never matched the issue, be advertised as claimable, consume a review attempt, and only then fail. `create_review_request`/`replace_review_request` now compare the requested target with the issue inside their own write transaction and reject a mismatch with `STALE_REVIEW_TARGET` without writing anything; `claimable` in get/list is `open` **and** target-still-matching; claiming a stale request supersedes it; `claim_issue` no longer binds a stale request to a new review attempt; and lease expiry, a `failed`/`interrupted` finish, or a force release resolves a claimed-but-stale request as `superseded` instead of returning it to `open` (docs/09 "Staleness and concurrency"). The staleness comparison now also ignores events emitted by review attempts, which previously made a request permanently stale after its first abandoned review.

- **A cancelled review request no longer leaves a reviewer with authority** — `cancel_review_request` cleared the request's `active_attempt_id` but left the bound review attempt active. `finish_attempt` reads an unbound review attempt whose issue has no unresolved request as an *optional* review, so the cancelled reviewer could still finish with `review_outcome=approved` and move the reviewed issue to `done` after its review had been called off. Cancelling a `claimed` request now terminates that attempt in the same transaction (status `cancelled`, an `attempt_cancelled` event, reservations released), so the revoked lease can no longer approve, request changes, block, renew, or otherwise act — every attempt operation requires an `active` attempt. Cancellation still changes no issue status, and cancelling an `open` request terminates nothing (docs/09 "State transitions").

- **Imported issues restore their own version, so imported reviews stay fresh** — Logical import inserted every issue at version 1 while preserving the issue versions frozen by review targets, review requests, gate snapshots, review approvals, and attempts. A review request that was fresh and claimable at export therefore arrived permanently stale, unclaimable by anyone. Version 2 issue records now carry an optional `version` (omitted when it is 1, and rejected outright by a version 1 record shape), and import restores it verbatim; a document that states no version still restores at version 1, exactly as before. Once a document does state an issue's version, the importer also rejects any frozen cursor above it — a position the destination could never reach (docs/07 §3).

### Removed

- **Deprecated review request tools** — Removed `create_review_request` and `supersede_review_request` MCP tools after one-release compatibility window. The atomic `replace_review_request` tool provides the combined functionality and is the supported replacement.

## [1.2.1] - 2026-08-13

### Fixed

- **Output schema compatibility** — Added explicit `Type: "object"` to union schemas in MCP tool outputs (`export_project`, `create_issue`, `update_issue`, `archive_issue`, `claim_issue`, `finish_attempt`). Ensures strict compatibility with MCP clients that enforce schema validation. Adds test coverage for output schema types.

- **Timeout handling in integration tools** — Added timeout context handling to integration tool calls and board issue creation to improve reliability and prevent indefinite hangs during long-running operations.

### Changed

- **Improved documentation** — Enhanced MCP tool documentation with clearer descriptions of lifecycle tool inputs and side effects.

### Internal

- Updated Rhizome Implementer agent model version.

## [1.2.0] - 2026-08-07

### Added

- **Interactive local board** — `rhizome-mcp board --serve` launches a loopback-only web UI for monitoring project status, active leases, blockers, reviews, and the planning graph.
- **Live board updates** — The served board refreshes its state without a page reload.
- **Issue detail pages and search** — Browse board issues in detail and search the served board for project work.

### Fixed

- **Shared project routing** — Correctly tracks active use of routed projects so idle cleanup does not close entries while requests still use them.

## [1.1.0] - 2026-08-05

### Added

- **Stateless MCP transport** — Supports modern and legacy local HTTP clients. Added MCP version 2026-07-28 support.
- **Agent session attribution** — Durable audit handles outlive connections.
- **Shared project routing** — Serves initialized projects from one data root.
- **MCP projections and artifacts** — Added compact issues and export files.
- **Coverage reporting** — CI publishes unit and integration coverage badges.

### Changed

- **MCP responses are smaller** — Removed duplicate payloads and bounded output.
- **Project routing documentation** — Clarifies client and runtime constraints.

### Fixed

- **Shared-project runtime** — Fixed cancellation, leases, and cleanup retries.

## [1.0.1] - 2026-07-25

### Changed

- **Project routing is now documented as shipped behavior** — `serve` accepts `--project-root` and `RHIZOME_PROJECT_ROOT`, routed opens are existing-only under one configured data root, and project selection is carried by ordinary tool arguments such as `project_ref` rather than transport routing metadata.
- **Operational behavior is now explicit** — the default opening is pinned, idle entries are evicted by LRU, leases prevent closing, shutdown drains in-flight work, and routed opens do not run init or migration flows for missing projects.
- **Compatibility guidance clarified** — global registrations may continue to use bare `serve`, while workspace-specific registrations should use `serve --project-root <absolute-root>` for deterministic project selection.


### Added

- **VS Code Marketplace extension** — "Rhizome MCP" (publisher `odrin`) bundles a platform-specific binary and registers the MCP server automatically via `mcpServerDefinitionProviders`; no separate install or `.vscode/mcp.json` editing needed. Covers darwin-x64/arm64, linux-x64/arm64, alpine-x64/arm64, and win32-x64/arm64. Published automatically from tagged releases, with a pre-release channel for beta tags.
- **npm distribution (`npx rhizome-mcp`)** — a dependency-free Node launcher package plus six per-platform binary packages (`@rhizome-mcp/<platform>`), so any MCP client can run the server via `npx` with no Go toolchain or manual binary install.
- **Official MCP Registry listing** — `io.github.Odrin/rhizome-mcp` is published to `registry.modelcontextprotocol.io` and kept current automatically on every tagged release.

## [1.0.0] - 2026-07-23

### Added

- **Core task tracking** — Full issue, epic, bug, decision, and comment lifecycle with atomic operations and optimistic concurrency control
- **MCP server transport** — Native MCP via stdio (primary) and local HTTP with built-in security boundaries (loopback-only, no auth required for local use)
- **Work claiming and leases** — Atomic task claiming with renewable leases, preventing permanent in-progress locks when agents disappear
- **Comprehensive planning and dependency graphs** — Full-text search, issue relations (depends, blocks, relates), and bounded graph queries with configurable depth and node limits
- **Checkpoints and recovery** — Durable attempt snapshots and notes for agent handoff and replay-safe failure recovery
- **Logical project interchange** — JSON import/export format for moving projects between installations and version control
- **Review workflow** — Multi-stage review requests with approval, changes-requested, and blocked outcomes; supports artifact attachment
- **Local-first SQLite backend** — Single writer, no remote dependency, full portability via single backup file
- **Multi-channel distribution** — Published to npm (`rhizome-mcp` launcher + `@rhizome-mcp/<platform>` binaries), VS Code Marketplace and Open VSX (`odrin.rhizome-mcp`), and the official MCP Registry (`io.github.Odrin/rhizome-mcp`). All channels CI-automated on every tag with OIDC-verified npm publishing and idempotent republish for consistency.
- **Command-line tools** — `init`, `serve`, `connect`, `board`, `doctor`, `backup`, `project info`, `issue list`, `issue show`, `search`, and `graph` commands. The VS Code extension bundles `board` for status monitoring and agent coordination.
- **Integration and installation automation** — GitHub Releases with checksummed binaries for Linux, macOS, and Windows; shell and PowerShell installers

### Constraints (by design)

- No web UI, desktop UI, or TUI in this version
- No authentication, authorization, or permanent agent identity
- SQLite single-writer model (not suitable for multi-node deployment)
- Deferred: custom statuses, arbitrary custom fields, nested epics, estimates, due dates, permanent assignees, teams/roles
- Deferred: remote access security, binary attachments, semantic search, PostgreSQL backend, multi-project dashboard

### Documentation

The specification is split across nine focused documents (docs/01 through docs/09) for selective agent loading:
- Product scope, domain model, MCP tools, storage, implementation requirements, deferred features
- Logical interchange format, HTTP transport contract, review workflow specification
