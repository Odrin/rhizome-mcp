<p align="center">
  <img src="site/assets/rhizome-mcp-logo.png" alt="rhizome-mcp logo" width="420">
</p>

<p align="center">
  <a href="https://github.com/Odrin/rhizome-mcp/actions/workflows/ci.yml"><img src="https://github.com/Odrin/rhizome-mcp/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/Odrin/rhizome-mcp/releases"><img src="https://img.shields.io/github/v/release/Odrin/rhizome-mcp?sort=semver" alt="Latest release"></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/Odrin/rhizome-mcp" alt="Go version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/Odrin/rhizome-mcp" alt="License"></a>
</p>

**rhizome-mcp** is a local-first MCP server for task tracking and coordination of autonomous AI coding agents. It gives agents from different products — Claude Code, Codex, GitHub Copilot, VS Code, and any other MCP-compatible client — a shared, durable view of project work: one static Go binary, one SQLite database per project, no accounts, no Docker, no network dependency.

## Why

AI coding agents are concurrent, context-limited, and interruptible. A `TODO.md` or a single chat context doesn't survive that. rhizome-mcp is built around those failure modes:

- **Crash-safe claiming.** Issues are claimed atomically with renewable leases. `in_progress` is never a stored status — it is derived from an active lease, so a vanished agent can't lock an issue forever. When the lease expires, the issue becomes claimable again. A partial unique index guarantees at most one active attempt per issue at the database level.
- **Durable project memory.** Checkpoints with next steps, supersedable decision records, append-only event history, and FTS5 full-text search across issues, comments, decisions, and notes. A fresh session resumes from the last checkpoint instead of re-deriving state.
- **Token-efficient by contract.** Compact list projections (a 100-issue page stays under 64 KB — enforced by an integration test), graph nodes that exclude free-text bodies at the SQL layer, snippet-only search, delta sync via event IDs, and a bounded single-call work-context package.
- **Planning and dependency graphs.** Cycle-checked `blocks` relations, epics, claimable entry-point highlighting, and atomic batch planning (up to 50 issues, 100 relations, and 20 decisions in one all-or-nothing transaction).
- **Review workflow.** Review requests pin an exact issue version and event position; stale targets are superseded automatically, so approving changed code is impossible.
- **Concurrency discipline throughout.** Optimistic versioning on mutations, replay-safe idempotency keys, stable machine-actionable error codes.
- **Human observability without a server.** `rhizome-mcp board` prints live leases, blockers, and the review queue, or writes a self-contained HTML snapshot; the CLI reads everything as tables, JSON, Markdown, or Mermaid.

**Use it when** several agent sessions (or several agent products) work the same repository over time and you need handoffs, parallel work, and recovery after crashes or context limits.

**Skip it if** you need a hosted multi-user tracker with auth, permissions, and a web UI — this is a local single-developer tool by design.

## Quick start

Install a release binary (verifies checksums, installs to `~/.local/bin` by default):

```bash
curl -fsSL https://raw.githubusercontent.com/Odrin/rhizome-mcp/main/scripts/install.sh | sh
```

```powershell
irm https://raw.githubusercontent.com/Odrin/rhizome-mcp/main/scripts/install.ps1 | iex
```

Initialize tracking inside your repository, then register the server with your MCP client:

```bash
rhizome-mcp init
rhizome-mcp connect claude
```

`connect` supports `claude`, `codex`, `vscode`, and `json` (generic config for any other client). Use `--print` for a dry run. The manual equivalent for any MCP client:

```json
{
  "mcpServers": {
    "rhizome": {
      "command": "/absolute/path/to/rhizome-mcp",
      "args": ["serve"]
    }
  }
}
```

Run `serve` with the repository as its working directory. Stdio is the default transport; protocol output goes to stdout, logs to stderr.

That's it — connected agents discover the workflow through the server itself: `get_project` links the `rhizome://guides/agent-workflow`, `rhizome://guides/issue-lifecycle`, and `rhizome://guides/multi-agent-handoff` resources, and repository agents can load the `rhizome-task-workflow` skill from `.github/skills/`.

### Watch what your agents are doing

```bash
rhizome-mcp board                        # status counts, active leases, blockers, review queue
rhizome-mcp board --output board.html    # self-contained HTML snapshot with the planning graph
rhizome-mcp issue list --status ready
rhizome-mcp graph ISSUE-42 --format mermaid
rhizome-mcp doctor --full
```

### Optional: local HTTP transport

```bash
rhizome-mcp serve --http-address 127.0.0.1:0
```

The bound endpoint is logged to stderr; the Streamable HTTP endpoint is `http://127.0.0.1:<port>/mcp`. Loopback-only, no authentication, strict Host/Origin validation — use literal loopback IPs (`127.0.0.1`, `[::1]`), not hostname binds. Not safe to expose beyond the local machine.

## How it works

`init` writes exactly one file into the repository:

```json
{
  "version": 1,
  "project_id": "01J..."
}
```

stored as `.agent-tracker.json`. The SQLite database lives outside the repository in the platform application-data directory, resolved through `project_id`:

```text
<application-data>/rhizome-mcp/projects/<project-id>/tasks.db
```

Use `--data-root PATH` to select an explicit data root for any command. Nothing else touches your repository, and the database is never committed to Git.

**Design principle:** an issue must never remain permanently stuck in `in_progress`. Effective status is computed from stored status plus the presence of an active leased attempt; if the agent disappears and the lease expires, the attempt becomes `expired` and the issue is available again when its stored state permits it.

**Core constraints (by design):** Go, SQLite (`modernc.org/sqlite`, pure Go, CGO-free), stdio as the primary transport, one database per project, no web UI, no authentication, minimal CLI. Deferred features are listed in [docs/06](docs/06-deferred-and-open.md).

## CLI reference

| Command | Purpose |
| --- | --- |
| `init` | Create `.agent-tracker.json` and the project database |
| `serve` | Run the MCP server (stdio; `--http-address` for local HTTP) |
| `connect TARGET [--print]` | Register the server with an MCP client (`claude`, `codex`, `vscode`, `json`) |
| `board [--output PATH]` | Status board: counts, leases, blockers, review queue; optional HTML snapshot |
| `issue list` / `issue show ISSUE-ID` | Inspect issues with filters |
| `search QUERY` | Full-text search across issues, comments, decisions, notes |
| `graph ISSUE-ID` | Dependency graph as table, JSON, or Mermaid |
| `project info` / `project export` | Project metadata; logical JSON export |
| `backup --output PATH` | WAL-safe online backup |
| `doctor [--full]` | Integrity, schema, and invariant checks |
| `maintenance release-attempt` / `rebuild-search-index` | Administrative recovery |

Run `rhizome-mcp` without arguments for complete usage, `rhizome-mcp version` for build information.

## MCP surface

The server exposes 31 tools covering the full lifecycle: project discovery, issue CRUD with labels and relations, planning and dependency graphs, batch plan validation/apply, comments and decisions, claim/renew/checkpoint/finish work attempts, work-context assembly, review requests, full-text search, delta changes, and logical project export/import. The complete contract is in [docs/03-mcp-tools.md](docs/03-mcp-tools.md).

## Documentation

The nine modular files under `docs/` are the canonical specification; [SPEC.md](SPEC.md) is the index. Agents should load only the sections relevant to their current task ([AGENT_BRIEF.md](AGENT_BRIEF.md) explains how).

1. [Product goals and scope](docs/01-product-scope.md)
2. [Domain model](docs/02-domain-model.md)
3. [MCP tools](docs/03-mcp-tools.md)
4. [Storage and runtime](docs/04-storage-runtime.md)
5. [Implementation requirements](docs/05-implementation-requirements.md)
6. [Deferred features and non-goals](docs/06-deferred-and-open.md)
7. [Logical interchange format](docs/07-logical-interchange.md)
8. [Local HTTP transport contract](docs/08-local-http-transport.md)
9. [Review workflow contract](docs/09-review-workflow.md)

Guides for humans (quick start, workflow, CLI) live in [site/](site/) and are published via GitHub Pages. Release history is in the [CHANGELOG](CHANGELOG.md).

## Development

Build and test (no CGO, no external services):

```bash
CGO_ENABLED=0 go build -o rhizome-mcp .
go test ./...
go test -tags=integration ./...
```

The integration tag runs real-process MCP smoke and workflow tests: they build a temporary server binary, initialize a fresh repository and SQLite data root per test, and speak to `serve` over stdio. Most live in the dedicated `integration` package; one test that needs unexported package-main internals stays at the repository root.

CI runs `go vet`, unit, and integration tests on Ubuntu, macOS, and Windows for every push and pull request targeting `main`. Releases (`.github/workflows/release.yml`) publish CGO-free binaries with SHA-256 checksums for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, and windows/amd64; release binaries embed the version, commit, and build timestamp (local builds report git VCS info or `dev`, and the `VERSION` environment variable overrides both).

This repository tracks its own backlog in rhizome-mcp: work is selected, claimed, and finished through the MCP server, and durable choices are recorded as decisions. Markdown holds specification only, not task status. See [AGENTS.md](AGENTS.md) and [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[Apache-2.0](LICENSE). Security policy: [SECURITY.md](SECURITY.md).
