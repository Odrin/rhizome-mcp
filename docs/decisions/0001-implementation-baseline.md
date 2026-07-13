# ADR 0001: implementation baseline

## Status

Accepted on 2026-07-13.

## Decisions

- Use Go 1.25.0 as the minimum language/toolchain version.
- Pin `github.com/modelcontextprotocol/go-sdk` v1.6.1.
- Pin `modernc.org/sqlite` v1.53.0 and preserve no-CGO builds.
- Pin `github.com/oklog/ulid/v2` v2.1.1 and generate IDs from injected time with cryptographic entropy.
- Use a small custom migration runner with embedded, checksummed, sequential SQL migrations.
- Use standard-library `log/slog` and `flag.FlagSet`; avoid framework dependencies until demonstrated necessary.
- Generate MCP JSON Schema from typed SDK inputs and outputs, then protect it with contract tests.
- Encode pagination cursors as versioned JSON containing the stable sort tuple, serialized with `encoding/json` and base64url without padding.
- Normalize typed idempotent requests before deterministic JSON encoding and SHA-256 hashing. Store the request hash and response atomically with the mutation.
- Generate lease tokens with `crypto/rand`, persist SHA-256 hashes only, and compare hashes with `crypto/subtle`.
- Use `VACUUM INTO` for online backups, preceded by a controlled WAL checkpoint and followed by opening and validating the output database.

## Operational defaults

- Lease duration: 5 minutes; minimum 30 seconds; maximum 1 hour.
- Stale session threshold: 15 minutes.
- Repeated-failure warning: 3 consecutive failed or expired attempts.
- SQLite busy timeout: 5 seconds; full transaction retry delays: 25 ms, 75 ms, and 200 ms.
- Graph depth: 2 by default and 5 maximum.
- Graph nodes: 100 by default and 500 maximum.
- SQLite pool: 4 open and 4 idle connections with no connection lifetime limits.

## Application data locations

- macOS: `~/Library/Application Support/rhizome-mcp`
- Linux: `$XDG_DATA_HOME/rhizome-mcp`, falling back to `~/.local/share/rhizome-mcp`
- Windows: `%LOCALAPPDATA%\\rhizome-mcp`

Each project database is `projects/<project-ulid>/tasks.db` below that directory. Project identity is discovered by searching upward for a strict `.agent-tracker.json` containing only `version` and `project_id`.

## Consequences

The selected MCP SDK and SQLite driver require Go 1.25. The repository will upgrade these dependencies together. SQLite remains the only storage implementation; the domain and application layers remain independent of SQLite, MCP, and CLI adapters. New implementation-time decisions that affect public contracts or invariants require another decision record.