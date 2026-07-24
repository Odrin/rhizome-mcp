# rhizome-mcp

Local-first MCP server for task tracking and coordination of autonomous AI
coding agents — one static binary, one SQLite database per project, no
accounts, no Docker, no network dependency.

This npm package is a thin, dependency-free launcher: it resolves and runs
the prebuilt Go binary for your OS/CPU (shipped as an optional dependency,
e.g. `@rhizome-mcp/darwin-arm64`) and forwards argv, stdio, exit code, and
signals straight through.

## Quick start

```bash
npx rhizome-mcp --version
```

```bash
npx rhizome-mcp init
npx rhizome-mcp connect claude
```

Or install it as a dev dependency / globally:

```bash
npm install --global rhizome-mcp
rhizome-mcp serve
```

## Use with an MCP client

Works with any MCP client — VS Code, Claude Code, Claude Desktop, Cursor, or a plain `mcp.json`:

```json
{
  "mcpServers": {
    "rhizome": {
      "command": "npx",
      "args": ["-y", "rhizome-mcp", "serve"]
    }
  }
}
```

## Supported platforms

`darwin-x64`, `darwin-arm64`, `linux-x64`, `linux-arm64`, `win32-x64`,
`win32-arm64`. npm installs only the one matching optional dependency for
your machine.

If installation was run with `--no-optional` or `--ignore-scripts`, or your
platform isn't one of the above, the launcher prints an actionable error
instead of crashing, and points at the curl/PowerShell installer described
in the main repository README as a fallback.

## Full documentation

See the main repository for the full documentation, CLI reference, and MCP
tool catalog: https://github.com/Odrin/rhizome-mcp
