# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.4.0] - 2026-08-29

### Added

- **Free-form toolset selection: `serve --toolsets`** — When none of the four named profiles fits a deployment, `serve --toolsets issues,planning` (or `RHIZOME_TOOLSETS`) composes the advertised catalog directly from the capability groups every tool already declares (`core`, `issues`, `planning`, `review`, `knowledge`, `lifecycle`, `governance`, `migration`, `sync`); the `core` pair (`open_project`, `get_project`) stays always-on so a client can still route and diagnose a missing tool. Mutually exclusive with `--profile`/`RHIZOME_TOOL_PROFILE`: both flags together fail as a usage error, and a mixed flag/environment combination fails startup before any transport opens rather than silently preferring either input. An unsupported, duplicate, or empty group name fails startup with an error naming the valid vocabulary; selecting toolsets from the environment prints the same explicit stderr notice profile selection does. In toolset mode `get_project`/`open_project` report `"tool_profile": "custom"` plus a `toolsets` array of the advertised groups. Like profiles, toolsets are an exposure and prompt-size control, not an authorization boundary (docs/03 §5.4).

- **Claude Code plugin and self-hosted marketplace** — `/plugin marketplace add Odrin/rhizome-mcp` followed by `/plugin install rhizome-mcp@rhizome` installs the server and both workflow skills into Claude Code in one step, with no manual MCP registration. The plugin launches the server as `npx -y rhizome-mcp serve`, so nothing has to be on `PATH` and no Go toolchain is needed; it deliberately passes no `--project-root`, because routing is stateless and the project is carried by ordinary `project_ref` arguments (docs/11). The plugin's `skills/` tree is a copy of the canonical `.github/skills` tree — plugin skills are discovered only from the plugin root, while the skills-CLI convention and guide generation target `.github/skills` — kept identical by `plugin_skills_sync_test.go` and refreshed with `scripts/sync-plugin-skills.sh`. The repository root hosts only the marketplace manifest, so the development `.mcp.json` is never shipped to an install.

- **Scripted, reproducible demo assets** — A new `demo/` directory holds four scenario drivers that talk to a real `serve --http-address` process with `curl` and `jq`, so each one doubles as a living smoke test of the docs/08 HTTP contract, plus the VHS tapes and a `record.sh` that rebuilds every asset in `site/assets/demo/` and fails if a GIF exceeds 2 MiB. The recordings show real server output: a second claim denied with `ACTIVE_ATTEMPT_EXISTS`, derived `in_progress` on the board, lease expiry, and a successor resuming from the checkpoint (`lease-expiry.gif`); an approval refused with `REVIEW_REQUEST_REQUIRED` because the request was pinned to an older issue version, then re-pinned by `replace_review_request` (`review-superseded.gif`); a claim failing atomically on an overlapping reservation that names the holding attempt and its lease (`reservation-conflict.gif`); and the status board on a seeded project (`board.png`, `board.html`).

- **Guarantee-framed comparison page** — New [site/compare.md](site/compare.md) compares rhizome-mcp with beads, Kata, Guild, and Backlog.md on what each guarantees under failure — crash recovery, double-claim prevention, review staleness, response budgets, attempt resumability, resource reservations, gates — rather than on feature nouns. Every cell is a mechanism claim pinned to a specific competitor release (beads v1.2.2, Kata v0.16.0, Guild v0.3.2, Backlog.md v1.50.1), and every rhizome yes-cell links to the spec section or the integration test that enforces it; the editorial rules for keeping the page honest are embedded in it as a comment header.

- **Agent-driven installation guide and discovery metadata** — A root [llms-install.md](llms-install.md) gives agent-driven installers (Cline and similar) a non-interactive, idempotent setup path: npx, native binary, or from source; per-client registration; and a verification step. The README badge row gains npm-downloads and MCP Registry badges, and the npm launcher package carries expanded keywords so directory and search listings can find it.

### Changed

- **The README leads with the guarantee instead of the feature list** — It now opens with the outcome — when a coding agent dies mid-task, the task frees itself — followed by the lease-expiry recording and a section table of contents. The Why list promotes the tested token-budget contract to second position, gains a resource-reservations bullet, and embeds the review-superseded and reservation-conflict recordings behind `<details>`; a new "How it compares" section links the full comparison page; quick start gains the Claude Code plugin install; and the monitoring section shows the board screenshot. Release-verification detail moved to [CONTRIBUTING.md](CONTRIBUTING.md), and `site/README.md` mirrors the new lead.

### Fixed

- **The first review request for an issue was unreachable over MCP** — `create_review_request` and `supersede_review_request` were both dropped after their deprecation window on the strength of "the atomic `replace_review_request` tool provides the combined functionality," but that was only ever true of supersession: `replace_review_request` requires a `predecessor_request_id`, so with no create tool advertised there was no way to open the *first* request for any issue. Nothing else fills the gap — `finish_attempt` with `target_issue_status: "review"` does not create a request, and the application/repository create path (purpose coverage, born-stale rejection, content-idempotent duplicates) had been left fully implemented with zero callers. Step 1 of the workflow docs/03 §7.6 and docs/09 describe therefore had no tool behind it, and every existing review test seeded its request through `sqlite.ReviewRepository` directly, so no test could notice. `create_review_request` is restored to the `review` capability group, taking `issue_id`, `target_issue_version`, `target_event_id`, and optional `artifact_ids`/`purposes`. It carries no `supersedes_id` — recording a supersession link without closing the predecessor is exactly the split `replace_review_request` exists to fix — and no `idempotency_key`: a repeat matching the target's still-live request replays it, but a cancel resolves that gate without changing the issue, so the guarantee is conditional and `idempotentHint` stays `false` (docs/03 §4). A new integration test walks request → discover → claim → resolve using only `tools/call`, and fails with `unknown tool` if the registration is ever removed again.

- **Re-requesting a review after `changes_requested` pointed at a tool that rejects it** — docs/09 step 5 and its stale-target recovery example told readers to open the follow-up request with `replace_review_request`, but replacement accepts only an `open` predecessor: a `changes_requested` or `superseded` one fails with `REVIEW_REQUEST_NOT_REPLACEABLE`. Both paths now name `create_review_request`, and docs/09, `site/workflow.md`, and the agent-workflow and issue-lifecycle guides state the split explicitly — `create_review_request` where no request is live, `replace_review_request` only to supersede one that is still `open`. The guide also notes that completing an attempt to `review` does not open a request on its own.

### Internal

- **Releases stamp the plugin manifest version** — A Claude Code marketplace install reads the plugin manifest from the repository, so the run-local stamping used for `server.json` cannot version it. After a fully successful release, a new `stamp-plugin-manifest` job writes the tag version into `plugins/rhizome-mcp/.claude-plugin/plugin.json` on `main` through the contents API (the same mechanism the coverage badges already use), skipping when the manifest is already current and skipping entirely for the `workflow_dispatch` VS Code republish fallback.

## [1.3.3] - 2026-08-26

### Fixed

- **npm launcher signal-forwarding test no longer races the child's final write** — The test asserted on the launcher's captured stdout after the child process's `exit` event, which can fire while the fake binary's `fake-binary-received:SIGTERM` acknowledgement is still buffered in the shared `stdio: 'inherit'` pipe; on slower runners the assertion ran first, which failed the v1.3.2 release's `Test npm launcher` jobs on Linux and macOS. The test now awaits `close`, which fires only after every writer has released the pipe and all buffered output has been delivered, so the assertion always sees the complete output.

## [1.3.2] - 2026-08-26

### Changed

- **`save_attempt_note` and `replace_review_request` are documented to first-call standard** — A tool-description review flagged both as too thin for an agent to use correctly on the first attempt. `save_attempt_note`'s advertised description now discloses the active-lease prerequisite, the four note kinds, that checkpoints seed successor recovery via `get_work_context`, and keyed-retry idempotency; its `kind` parameter explains when each value applies (and when `record_decision` or `add_comment` fits better), and `idempotency_key` documents replay-versus-duplicate-append behavior. Every `replace_review_request` parameter now states its provenance and failure mode: the predecessor version comes from `get_review_request` (mismatch fails with `VERSION_CONFLICT`), the target must be the issue's current version and event position (`STALE_REVIEW_TARGET` otherwise), `artifact_ids` are not inherited from the predecessor (unlike `purposes`), and the — unusually — *required* `idempotency_key` replays identical requests and rejects key reuse with `IDEMPOTENCY_CONFLICT`.

### Fixed

- **Stale guidance still pointed at the removed review request tools** — The site's review workflow quick guide told readers to call the removed `create_review_request` and to manually attach a review attempt to a request, a step `claim_issue` has performed automatically since binding became transactional. The guide now matches docs/09: a tool-agnostic request step, `replace_review_request` for refreshing a stale open request, and automatic binding on claim. A domain comment and two test comments still describing the removed tool as merely "deprecated" were corrected too; changelog history intentionally keeps its original wording.

### Internal

- **The integration suite runs in parallel, and CI stops running the unit suite twice** — The CI integration step invoked `go test -tags=integration ./...`, which re-ran every unit test the previous step had just finished (the build tag only adds files, and it defeats test-result caching); it now filters with `-run '^TestIntegration'`, exactly as the coverage job already did. The 63 integration tests — each driving its own server subprocess and spending most of their wall clock waiting on round trips — are now `t.Parallel()`, safe because every test owns an isolated repository, data root, and `:0` port, and lease expiries are backdated through SQL rather than awaited. Locally the package drops from 6.9s to ~2s uninstrumented and from 181s to 19s under `-race`.
- npm package smoke tests spawn `npm` through a shell on Windows, matching how npm must be invoked there.

## [1.3.1] - 2026-08-25

### Fixed

- **Release packaging read the tag from the wrong step** — The release workflow's packaging and upload steps referenced a stale step id for the release tag; they now read the resolve step's output, so artifacts are named and uploaded for the actually resolved tag.

## [1.3.0] - 2026-08-25

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

- **Imported event cursors are remapped into the destination log** — Imported events receive fresh destination IDs, but the four columns that name an event-log position — a review target's `latest_event_id`, a review request's and approval's `target_event_id`, an attempt's `context_event_id_at_start` — were restored verbatim. A source project whose log had run past the destination's therefore left cursors above every ID the destination would ever assign, so the "has anything happened after this position" question each one exists to answer stayed answered "no": an imported review request remained claimable no matter what happened to the reviewed issue afterwards. Import now builds a source-to-destination event ID mapping while replaying `events` and translates all four cursors through it, flooring to the highest destination position at or below each cursor. That rule is what makes the deliberately sparse documents well-defined — archived issues and active attempts export without their events — and it covers version 1 documents unchanged (docs/07 §4.2).

- **Imported review event payloads name destination rows** — Import remapped review target and request row IDs but inserted review-sourced event payloads verbatim, and a review event's request and target are recorded nowhere else (migration 008 folded `review_events` into `issue_events` and dropped those columns). Export promotes them back out of the payload into the typed `review_events` records, whose references are checked for referential closure — so a destination's own re-export was a document that no longer parsed. Import now rewrites the identities a review event's payload contract names (`request_id`, `target_id`, `attempt_id`, `issue_id`) through structured JSON, preserving every other member. Ordinary event payloads stay byte-identical as the frozen audit facts they are, and a reference the document does not carry (a version 1 document has no review entities; a claimed request is excluded while its events are not) is kept as history rather than failing the import (docs/07 §4).

- **A policy-only destination is no longer treated as empty** — Logical import's two empty-destination guards (the preflight check and the in-transaction race-closing check) each carried their own copy of a table list that stopped at the tables existing when interchange was first written. A project holding only workflow-gate state — policies and their audit trail — therefore looked empty to both, and an import merged into it despite the format's no-merge contract. Both now read one shared, authoritative definition covering every table that holds durable project content, including review targets/requests/outcomes/follow-ups, gate snapshots, evidence, approvals, and reservations; a new test reads the migration SQL and fails whenever a table is neither counted nor explicitly exempted, so the list cannot drift again (docs/07 §6).

- **Re-prioritising an issue no longer supersedes its review** — docs/09 promised that a priority-only change does not invalidate a review target, but staleness treated every issue-version mismatch as stale, and every update — priority included — increments `issues.version`. Re-ordering a queue therefore superseded every review waiting in it, at `get`/`list`, at claim, at release, and at completion. A version gap is now accepted exactly when priority-only updates account for all of it, one version step each, so anything else that moved the version still makes the target stale; a priority changed alongside another field, or in the same call as a status or label change, is an ordinary change. Completion no longer carries its own inlined copy of the check either, so a request cannot be fresh to `get_review_request` and stale to `finish_attempt`.

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
