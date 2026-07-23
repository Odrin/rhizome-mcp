# Contributing to rhizome-mcp

Thank you for your interest in contributing. This document explains how to build, test, and submit changes.

## Build

Build the native binary without CGO:

```bash
CGO_ENABLED=0 go build -o rhizome-mcp .
```

No external dependencies, Docker, or services are required. The binary is self-contained and portable.

## Test

Run the full test suite:

```bash
go test ./...
```

The real-process MCP workflow tests are isolated behind the `integration` build tag. These build a temporary server binary, initialize a fresh database, and test the full MCP lifecycle:

```bash
go test -tags=integration .
```

Both test commands should pass before submitting a pull request.

## Code style

- Follow the conventions documented in [AGENTS.md](AGENTS.md) and [AGENT_BRIEF.md](AGENT_BRIEF.md)
- Business logic belongs in application/domain layers; keep adapters (MCP, CLI, storage) thin
- Keep writes short and atomic; use optimistic concurrency for mutations
- Preserve all history; avoid deleting issues or events
- Inject time and dependencies; keep functions deterministic and testable

## Repository tracking

This repository uses `rhizome-mcp` itself for issue tracking and backlog management. Before starting work:

1. Check the configured MCP project for related issues and planning graphs
2. Claim an executable issue through MCP before editing
3. Use attempt notes to save checkpoints and handoffs
4. Call `finish_attempt` on completion with verification summary
5. Update issue status, blockers, and relations through MCP (not Markdown)

See [AGENTS.md](AGENTS.md) for the full workflow.

## Submitting changes

1. Create a feature branch from `main`
2. Make your changes and ensure tests pass
3. Keep commits focused and descriptive
4. Open a pull request with a clear summary of changes and testing performed
5. Link to the related MCP issue(s) in the PR description

## Documentation

- Specification changes go in the modular docs/ files (01-09), not README.md
- Product and technical decisions are recorded as MCP decisions (durable, versioned)
- Active backlog and implementation status live in MCP issues, not Markdown

See [SPEC.md](SPEC.md) for the specification index and [docs/06-deferred-and-open.md](docs/06-deferred-and-open.md) for known trade-offs and future work.
