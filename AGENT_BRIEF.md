# Implementation context for AI coding agents

You are implementing `rhizome-mcp`, a local-first MCP server for task tracking and coordination of autonomous AI coding agents.

## Load context selectively

1. Inspect the configured MCP project's issues and planning graph.
2. Select a claimable issue and load its work context, relations, and relevant decisions.
3. Inspect the owning code path and its nearest tests.
4. Read only the specification sections needed for that issue.

`SPEC.md` is a lightweight index, not a second copy of the specification. Detailed contracts live only in the modular documents below.

Specification map:

- `docs/01-product-scope.md`: goals, users, deployment, and non-goals.
- `docs/02-domain-model.md`: entities, states, invariants, and lifecycle rules.
- `docs/03-mcp-tools.md`: public tool inputs, outputs, limits, and behavior.
- `docs/04-storage-runtime.md`: SQLite, transactions, migrations, leases, and operations.
- `docs/05-implementation-requirements.md`: architecture, algorithms, errors, and test coverage.
- `docs/06-deferred-and-open.md`: deferred features and confirmed defaults.
- MCP decisions: accepted implementation and product choices.

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

Implement one coherent vertical slice at a time. Claim executable work through MCP, save checkpoints and handoffs as attempt notes, and finish the attempt with a concise result and verification summary. Keep issue status, blockers, relations, and decisions current through MCP. Use focused checks during development and repository-wide validation when shared contracts, schemas, migrations, or cross-layer wiring change.

Do not maintain the active backlog or durable decisions in Markdown. Markdown documentation remains the source for product and technical specification only.
