# Implementation brief for an AI coding agent

You are implementing `rhizome-mcp`, a local-first MCP server for task tracking and coordination of autonomous AI coding agents.

Read these files before planning:

1. `README.md`
2. `docs/01-product-scope.md`
3. `docs/02-domain-model.md`
4. `docs/03-mcp-tools.md`
5. `docs/04-storage-runtime.md`
6. `docs/05-implementation-requirements.md`
7. `docs/06-deferred-and-open.md`

`SPEC.md` contains the same material in one consolidated document.

## Your first task

Before writing production code:

1. Inspect the complete specification.
2. Identify internal dependencies between subsystems.
3. Produce a phased implementation plan.
4. Define the initial Go module structure.
5. Define the first SQLite migrations.
6. Select concrete versions of the MCP SDK and SQLite driver.
7. Identify any specification conflicts or underspecified behavior.
8. Propose tests for concurrency, leases, idempotency and graph traversal.
9. Do not add deferred features unless required by the specification.

## Non-negotiable requirements

- Go and SQLite.
- Native local startup without Docker.
- One SQLite database per project, stored outside the repository.
- `.agent-tracker.json` contains only `version` and `project_id`.
- Primary MCP transport is `stdio`.
- Business logic must be independent from MCP and CLI adapters.
- `in_progress` is computed from an active work attempt.
- One issue can have at most one active attempt.
- Attempts use renewable leases and opaque lease tokens.
- A lost client must not leave an issue permanently locked.
- Batch plan application must be atomic.
- Mutations must support idempotency where specified.
- Issue updates use optimistic concurrency.
- Responses must be bounded and token-efficient.
- Full-text search uses SQLite FTS5.
- No GUI, authentication, custom workflows or binary attachments in the first version.

## Recommended implementation order

Use this only as a dependency guide; produce your own detailed plan.

1. Project bootstrap, configuration resolution and database lifecycle.
2. Schema migrations, SQLite connection configuration and health checks.
3. Domain types, validation and error model.
4. Issue CRUD, labels, events and optimistic concurrency.
5. Relations, cycle detection and graph queries.
6. Agent sessions, attempts, leases, checkpoints and recovery.
7. Comments, decisions and artifacts.
8. Idempotency.
9. Batch plan validation and application.
10. FTS5 search and change feed.
11. MCP adapter and tool schemas.
12. CLI, backup, restore, export helpers and diagnostics.
13. Integration and concurrency tests.

## Expected engineering style

- Prefer explicit domain services over logic in handlers.
- Keep write transactions short.
- Make all ordering deterministic.
- Use injected time through a `Clock` interface.
- Treat FTS as a rebuildable projection.
- Return structured domain errors instead of SQLite errors.
- Preserve history; avoid physical deletion.
- Do not silently truncate responses.
