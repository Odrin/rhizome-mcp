# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
- **Command-line tools** — `init`, `serve`, `doctor`, `backup`, `project info`, `issue list`, `issue show`, `search`, and `graph` commands
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
