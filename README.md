# rhizome-mcp

`rhizome-mcp` is a local-first MCP server for task tracking and coordination of autonomous AI coding agents.

The server gives agents a shared, structured view of project work:

- issues, epics, bugs, comments, decisions, labels and relations;
- dependency and planning graphs;
- atomic task claiming with renewable leases;
- checkpoints and recovery after interrupted agent sessions;
- full-text search and delta-based change tracking;
- compact, token-efficient context retrieval.

The project is designed for sequential and parallel work by different agent products, including GitHub Copilot, Codex, Claude Code, Antigravity and similar MCP-compatible clients.

## Core constraints

- Language: Go
- Database: SQLite
- Deployment: local native binary, no Docker required
- Primary MCP transport: `stdio`
- One SQLite database per project
- Databases are stored outside project repositories
- The repository contains only `.agent-tracker.json`
- No web UI in the first version
- Minimal CLI for initialization, diagnostics and maintenance
- No authentication in the first version

## Documentation

- [Current implementation roadmap](docs/07-implementation-plan.md)
- [Implementation context for AI agents](AGENT_BRIEF.md)
- [Product goals and scope](docs/01-product-scope.md)
- [Domain model](docs/02-domain-model.md)
- [MCP tools](docs/03-mcp-tools.md)
- [Storage and runtime](docs/04-storage-runtime.md)
- [Implementation requirements](docs/05-implementation-requirements.md)
- [Deferred features and non-goals](docs/06-deferred-and-open.md)
- [Specification index and reading guide](SPEC.md)
- [Accepted implementation baseline](docs/decisions/0001-implementation-baseline.md)

The six modular files are the canonical specification. `SPEC.md` is a lightweight index so contract text has one source of truth. Agents should load only the sections relevant to the current roadmap unit.

## Repository identity

A project repository contains:

```json
{
  "version": 1,
  "project_id": "01J..."
}
```

in:

```text
.agent-tracker.json
```

The project database is resolved through `project_id` and stored in the application data directory outside the repository.

## Primary design principle

An issue must never remain permanently stuck in `in_progress`.

`in_progress` is therefore not a stored issue status. It is an effective status derived from the existence of an active leased work attempt. If the agent disappears and the lease expires, the attempt becomes `expired` and the issue becomes available again when its stored state permits it.

## Status

Active development toward the first release. The [implementation roadmap](docs/07-implementation-plan.md) is the single source for the current phase, next unit, and exit gates.
