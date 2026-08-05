# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
