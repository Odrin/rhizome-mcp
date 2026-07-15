# Implementation context for AI coding agents

You are implementing `rhizome-mcp`, a local-first MCP server for task tracking and coordination of autonomous AI coding agents.

## Load context selectively

1. Read the current state and next unit in `docs/07-implementation-plan.md`.
2. Inspect the owning code path and its nearest tests.
3. Read only the specification sections needed for that unit.
4. Read an ADR only when the unit touches its decision.

`SPEC.md` is a lightweight index, not a second copy of the specification. Detailed contracts live only in the modular documents below.

Specification map:

- `docs/01-product-scope.md`: goals, users, deployment, and non-goals.
- `docs/02-domain-model.md`: entities, states, invariants, and lifecycle rules.
- `docs/03-mcp-tools.md`: public tool inputs, outputs, limits, and behavior.
- `docs/04-storage-runtime.md`: SQLite, transactions, migrations, leases, and operations.
- `docs/05-implementation-requirements.md`: architecture, algorithms, errors, and test coverage.
- `docs/06-deferred-and-open.md`: deferred features and confirmed defaults.
- `docs/decisions/`: accepted implementation choices.

## Non-negotiable requirements

- Go, SQLite, native startup, and no Docker requirement.
- One external SQLite database per project; the repository stores only `.agent-tracker.json`.
- `.agent-tracker.json` contains only `version` and `project_id`.
- Primary MCP transport is `stdio`.
- Business logic stays in application/domain layers, outside MCP and CLI adapters.
- `in_progress` is derived from one active renewable lease; lost clients cannot lock issues permanently.
- Writes that combine state, events, projections, or idempotency records are short and atomic.
- Issue mutations use optimistic concurrency and specified mutations support replay-safe idempotency.
- Time is injected, ordering is deterministic, and all large responses are explicitly bounded.
- FTS5 is a rebuildable projection; history is preserved and raw storage errors or lease secrets are not exposed.
- Deferred features remain out of scope.

## Working rule

Implement one coherent vertical slice at a time. Use the smallest focused check during development, then apply the validation tier in the roadmap. Keep roadmap entries current and concise; Git history and tests are the detailed completion record.
