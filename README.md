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

## Build and use

Build the single native binary without CGO:

```bash
CGO_ENABLED=0 go build -o rhizome-mcp .
```

Run the regular test suite with:

```bash
go test ./...
```

The real-process MCP smoke and workflow tests are isolated behind the
`integration` build tag. They build a temporary server binary, initialize a
fresh repository and SQLite data root for each test, and communicate with
`serve` over stdio. Most of them live in the dedicated `integration` package;
one test that needs unexported package-main internals stays at the
repository root:

```bash
go test -tags=integration ./...
```

Continuous integration tests run on every push and pull request targeting `main`.
The CI workflow (`.github/workflows/ci.yml`) runs `go vet`, the unit test suite,
and integration tests across ubuntu, macos, and windows platforms.

Run commands from the repository to be tracked. `init` writes only
`.agent-tracker.json` to that repository; the SQLite database is stored in the
platform application-data directory. Use `--data-root PATH` to select an
explicit external data root for every command.

```bash
rhizome-mcp init
rhizome-mcp doctor --full
rhizome-mcp backup --output /safe/location/project-backup.db
rhizome-mcp project info --format json
```

To connect an MCP client, launch `serve` with the repository as its working
directory. Stdio remains the default and is the recommended transport for
clients; protocol output is written only to stdout and logs/errors are written
to stderr.

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

For a local HTTP endpoint instead, run `rhizome-mcp serve --http-address
127.0.0.1:0`. The process logs the bound endpoint to stderr and the
Streamable HTTP endpoint is `http://127.0.0.1:<port>/mcp`. Configure local
clients to use that URL. The HTTP transport is loopback-only, has no
authentication, and rejects Host/Origin values that do not match the
configured local endpoint. Use literal loopback IPs such as `127.0.0.1` or
`[::1]`; hostname binds like `localhost` are not supported by the current
implementation.

The CLI also provides `issue list`, `issue show`, `search`, `graph`, and
maintenance commands. Run the binary without arguments to print complete
usage.

## Version information

Display the binary version with `rhizome-mcp version`, `rhizome-mcp --version`, or `rhizome-mcp -v`:

```bash
rhizome-mcp version
# Output: rhizome-mcp v1.2.3 (commit abc1234, built 2024-01-01T12:00:00Z)
```

Version information is resolved with the following precedence (highest to lowest):
1. `VERSION` environment variable — if set, this value overrides all other sources
2. Version injected at build time via `-X main.version` (used in release binaries)
3. Git VCS information embedded by Go in local builds
4. Fallback to `"dev"` if no other version is available

Release binaries built by the GitHub Actions workflow inject the semantic version tag, commit hash, and build timestamp. Local builds (plain `go build .`) report the git revision or `"dev"` if not building from a git checkout.

## Release automation and installers

GitHub Releases are built by `.github/workflows/release.yml` on both published
and prereleased release events. The workflow:

- builds CGO-disabled binaries for linux/amd64, linux/arm64, darwin/amd64,
  darwin/arm64, and windows/amd64;
- creates predictable asset names: `rhizome-mcp_<version>_<os>_<arch>.<ext>`;
- publishes each archive and `<archive>.sha256` checksum to the release;
- installs `svu` and derives the next semantic version (`svu next`) as part of
  the release pipeline context.

Required repository setting: workflow runs need permission to write release
assets (`contents: write`, granted in workflow permissions).

Installers published from release assets:

```bash
curl -fsSL https://raw.githubusercontent.com/Odrin/rhizome-mcp/main/scripts/install.sh | sh
```

```powershell
irm https://raw.githubusercontent.com/Odrin/rhizome-mcp/main/scripts/install.ps1 | iex
```

Installers verify archive checksums and install to a user-local directory by
default (`~/.local/bin`), then report whether that directory is already on
PATH.

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

- [Implementation context for AI agents](AGENT_BRIEF.md)
- [Product goals and scope](docs/01-product-scope.md)
- [Domain model](docs/02-domain-model.md)
- [MCP tools](docs/03-mcp-tools.md)
- [Storage and runtime](docs/04-storage-runtime.md)
- [Implementation requirements](docs/05-implementation-requirements.md)
- [Deferred features and non-goals](docs/06-deferred-and-open.md)
- [Logical project interchange format](docs/07-logical-interchange.md)
- [Local HTTP transport contract](docs/08-local-http-transport.md)
- [Review workflow contract](docs/09-review-workflow.md)
- [Specification index and reading guide](SPEC.md)

The nine modular files (docs/01 through docs/09) are the canonical specification. `SPEC.md` is a lightweight index so contract text has one source of truth. Agents should load only the sections relevant to the current MCP issue.

Connected MCP clients receive compact initialize guidance. `get_project` links
the full `rhizome://guides/agent-workflow`,
`rhizome://guides/issue-lifecycle`, and
`rhizome://guides/multi-agent-handoff` resources. Repository agents can also
load the `rhizome-task-workflow` skill in `.github/skills/`.

## Repository task tracking

The configured `rhizome-mcp` project is the source of truth for this repository's backlog and implementation history. Contributors and agents should:

- inspect MCP issues and the planning graph before selecting work;
- use work context and active decisions before implementation;
- create and update work through MCP issues, labels, hierarchy, and relations;
- record durable architectural or product choices with MCP decision tools;
- claim executable work and store checkpoints or handoffs in attempts and attempt notes;
- use MCP search and changes to recover historical context.

Markdown remains appropriate for product and technical specifications, but not for the active task backlog or implementation-status tracking.

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

The first-version MVP is complete. Active follow-up work and historical milestones are tracked in the configured `rhizome-mcp` project.
