# rhizome-mcp specification

The canonical specification is split into focused documents so agents can load only the context required for one task:

1. [Product goals and scope](docs/01-product-scope.md)
2. [Domain model](docs/02-domain-model.md)
3. [MCP tools](docs/03-mcp-tools.md)
4. [Storage and runtime](docs/04-storage-runtime.md)
5. [Implementation requirements](docs/05-implementation-requirements.md)
6. [Deferred features and open decisions](docs/06-deferred-and-open.md)

Use [the implementation roadmap](docs/07-implementation-plan.md) for current status, remaining order, and exit gates. Accepted implementation choices live in [decision records](docs/decisions/).

## Agent reading rule

Start with the roadmap's current unit, then read the owning code and tests plus only the relevant specification sections. [AGENT_BRIEF.md](AGENT_BRIEF.md) provides a compact context map and stable project invariants.

This file intentionally does not duplicate the modular specification. Keeping one canonical copy prevents drift and avoids loading the full contract for bounded work.