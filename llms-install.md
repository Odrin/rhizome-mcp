# rhizome-mcp — LLM installation guide

This file is written for AI agents (Cline, Claude Code, Codex, and similar) that
install and configure `rhizome-mcp` on a user's machine. Follow it top to
bottom; every step is non-interactive and idempotent.

`rhizome-mcp` is a local-first MCP server for task tracking and coordination of
AI coding agents. One binary, one SQLite database per project, stdio transport
by default. No accounts, no Docker, no network dependency.

## 1. Choose an install method

### Method A — npx (no install, recommended for first use)

Requires Node.js >= 22. Nothing to download separately; the launcher fetches
the platform binary as an npm optional dependency.

Verify availability:

```bash
npx -y rhizome-mcp version
```

### Method B — native binary

macOS / Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/Odrin/rhizome-mcp/main/scripts/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/Odrin/rhizome-mcp/main/scripts/install.ps1 | iex
```

The script verifies SHA-256 checksums and installs to `~/.local/bin` by
default. Verify: `rhizome-mcp version`.

### Method C — VS Code extension

Install `odrin.rhizome-mcp` from the VS Code Marketplace or Open VSX. The
extension bundles the binary and registers the MCP server automatically — if
the user is in VS Code, prefer this and skip section 3.

## 2. Initialize the project (required, once per repository)

Run inside the repository that should be tracked:

```bash
npx -y rhizome-mcp init      # method A
rhizome-mcp init             # method B/C
```

This writes exactly one file, `.agent-tracker.json` (a project id), into the
repository. The SQLite database is created outside the repository in the
platform application-data directory. `init` is safe to re-run; it does not
overwrite an existing project.

## 3. Register the MCP server with the client

### Automated (preferred)

```bash
rhizome-mcp connect claude    # Claude Code
rhizome-mcp connect codex     # Codex
rhizome-mcp connect vscode    # VS Code (standalone binary)
rhizome-mcp connect json      # prints a template for any other client
```

Add `--print` first for a dry run. `connect` discovers the project root and
pins it with `--project-root`, so the config works regardless of the client's
working directory.

### Manual (any MCP client)

npx form (no binary on PATH needed):

```json
{
  "mcpServers": {
    "rhizome-mcp": {
      "command": "npx",
      "args": ["-y", "rhizome-mcp", "serve", "--project-root", "/absolute/path/to/repository"]
    }
  }
}
```

Binary form:

```json
{
  "mcpServers": {
    "rhizome-mcp": {
      "command": "rhizome-mcp",
      "args": ["serve", "--project-root", "/absolute/path/to/repository"]
    }
  }
}
```

Replace `/absolute/path/to/repository` with the absolute path of the
repository initialized in step 2. Stdio is the default transport: protocol on
stdout, logs on stderr.

## 4. Verify the installation

1. `rhizome-mcp doctor` (or `npx -y rhizome-mcp doctor`) inside the repository
   must report no errors.
2. After the MCP client restarts, the server advertises 42 tools; call
   `open_project` with the absolute repository root — it returns a
   `project_ref` and guide links. Pass that `project_ref` to subsequent calls.
3. `rhizome-mcp board` prints status counts for the project.

## 5. First workflow for the agent

Read the `rhizome://guides/agent-workflow` MCP resource. The core loop:
`open_project` → `list_issues`/`get_planning_graph` → `get_work_context` →
`claim_issue` (returns a renewable lease) → work with `save_attempt_note`
checkpoints → `finish_attempt`. Claims expire if not renewed, so a crashed
agent never blocks an issue permanently.

## Troubleshooting

- `node` older than 22: use Method B (native binary) instead of npx.
- Client launches the server from a different directory: always pass
  `--project-root` (the `connect` command does this automatically).
- `PROJECT_NOT_INITIALIZED` errors: run step 2 in the repository root.
- Full docs: <https://odrin.github.io/rhizome-mcp/> and
  [docs/](https://github.com/Odrin/rhizome-mcp/tree/main/docs).
