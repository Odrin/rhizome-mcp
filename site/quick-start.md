# Quick start

Use the steps below to install, initialize, and connect clients to `rhizome-mcp`.

## Install and run

Choose the approach that matches your workflow:

### Zero-install trial via npm

Try `rhizome-mcp` immediately with no separate binary install, no Go toolchain. For any MCP client, the [`rhizome-mcp` npm package](https://www.npmjs.com/package/rhizome-mcp) runs the correct platform binary with no separate download:

```bash
npx rhizome-mcp serve
```

For a workspace-specific MCP registration, prefer an absolute project root:

```bash
npx rhizome-mcp serve --project-root /absolute/path/to/workspace
```

```json
{
  "mcpServers": {
    "rhizome-mcp": {
      "command": "npx",
      "args": ["-y", "rhizome-mcp", "serve", "--project-root", "/absolute/path/to/workspace"]
    }
  }
}
```

See [packages/npm/README.md](https://github.com/Odrin/rhizome-mcp/blob/main/packages/npm/README.md) for platform coverage. Great for quick evaluation.

### VS Code

Install [Rhizome MCP](https://marketplace.visualstudio.com/items?itemName=odrin.rhizome-mcp) (`odrin.rhizome-mcp`) from the Marketplace or [Open VSX](https://open-vsx.org/extension/odrin/rhizome-mcp). The extension bundles the platform binary, registers the MCP server automatically, and adds `Rhizome: Initialize Project` to the Command Palette. No terminal, no `mcp.json` editing needed.

Prefer a standalone binary with a plain `mcp.json` entry? Install the native binary below, then use the one-click link in that section.

### Native binary installer

Download and install a release binary for your platform. The installers verify checksums and install to `~/.local/bin` by default.

On Linux or macOS, choose the installer for your platform:

- [install.sh](https://github.com/Odrin/rhizome-mcp/blob/main/scripts/install.sh)
- [install.ps1](https://github.com/Odrin/rhizome-mcp/blob/main/scripts/install.ps1)

The installers detect your operating system and CPU architecture, download the matching release, verify its SHA-256 checksum, and install `rhizome-mcp` to `~/.local/bin` by default. They also tell you if the installation directory needs to be added to your `PATH`.

On Linux or macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/Odrin/rhizome-mcp/main/scripts/install.sh | sh
```

On Windows PowerShell:

```powershell
Invoke-RestMethod https://raw.githubusercontent.com/Odrin/rhizome-mcp/main/scripts/install.ps1 | Invoke-Expression
```

Set `RHIZOME_VERSION` to install a specific release or `RHIZOME_INSTALL_DIR` to choose a different installation directory before running the installer.

#### Manual installation from release assets

Open the GitHub Releases page at https://github.com/Odrin/rhizome-mcp/releases and choose the archive that matches your OS and CPU architecture (for example `rhizome-mcp_*_linux_amd64.tar.gz`, `rhizome-mcp_*_darwin_arm64.tar.gz`, or `rhizome-mcp_*_windows_amd64.zip`). Download the archive and the adjacent `.sha256` file with the same base name.

Verify the archive before extracting it:

```bash
shasum -a 256 rhizome-mcp_*.tar.gz
```

Compare the output to the contents of the matching `.sha256` file. On Windows PowerShell, use:

```powershell
Get-FileHash -Algorithm SHA256 .\rhizome-mcp_*.zip
```

and compare that value to the `.sha256` file contents.

After the checksum matches, extract the archive and place the resulting `rhizome-mcp` or `rhizome-mcp.exe` binary in a directory that is already on your `PATH`, such as `~/.local/bin` (Linux/macOS) or `%USERPROFILE%\bin` (Windows). Then run:

```bash
rhizome-mcp doctor
```

#### Build from source

As an alternative to the release archive, build from source in the repository:

```bash
CGO_ENABLED=0 go build -o rhizome-mcp .
```

This keeps the installation path explicit without relying on unsupported `go install` instructions or mirrored shell scripts.

### Official MCP Registry

Use `rhizome-mcp` via the official MCP Registry, available in the [Model Context Protocol registry](https://registry.modelcontextprotocol.io) as `io.github.Odrin/rhizome-mcp` for clients that consume the registry.

## Initialize and connect

### Initialize the project

Run the binary from the repository that should be tracked. `init` writes `.agent-tracker.json` into that repository and leaves the SQLite database outside the repo.

```bash
rhizome-mcp init
```

### Connect common clients

Register the server with your MCP client. Automated setup for common clients:

```bash
rhizome-mcp connect claude    # Claude Code
rhizome-mcp connect codex     # Codex
rhizome-mcp connect vscode    # VS Code (if using standalone binary instead of extension)
rhizome-mcp connect json      # Template for any other client
```

Use `--print` for a dry run. `connect` discovers your project's actual root
and pins it with `--project-root`, and detects when it is running through
the `npx rhizome-mcp` wrapper to emit an `npx` command instead of a
resolved path that would go stale in the npx cache. The manual equivalent
for any MCP client, matching `connect`'s own server key:

```json
{
  "mcpServers": {
    "rhizome-mcp": {
      "command": "/absolute/path/to/rhizome-mcp",
      "args": ["serve", "--project-root", "/absolute/path/to/your/repository"]
    }
  }
}
```

or, via `npx`, without installing a binary at all:

```json
{
  "mcpServers": {
    "rhizome-mcp": {
      "command": "npx",
      "args": ["-y", "rhizome-mcp", "serve", "--project-root", "/absolute/path/to/your/repository"]
    }
  }
}
```

### Install the agent workflow skill

For agents that support the open [Agent Skills](https://agentskills.io/) format, install `rhizome-task-workflow` with the npm-distributed [`skills`](https://skills.sh/) CLI:

```bash
npx skills add Odrin/rhizome-mcp --skill rhizome-task-workflow
```

Run the command in a project for a project-scoped installation, or add `--global` to make the skill available across projects. The skill teaches compatible agents how to select, claim, checkpoint, hand off, and finish Rhizome work. It complements the MCP server; it does not install the `rhizome-mcp` binary or configure an MCP connection.

## Verify the setup

```bash
rhizome-mcp doctor
rhizome-mcp project info --format json
```

If you want a deeper health check:

```bash
rhizome-mcp doctor --full
```

To see a local, read-only status board (issue counts, leased attempts, blocked issues, open review requests, and the planning graph), including a self-contained HTML version you can open in a browser:

```bash
rhizome-mcp board
rhizome-mcp board --output board.html
```

See [`board`](./cli.md#board) in the CLI reference for details.

## Optional loopback HTTP transport

The default transport is stdio. For a local HTTP endpoint instead, start the server with a literal loopback IP address:

```bash
rhizome-mcp serve --http-address 127.0.0.1:0
```

The process logs the bound endpoint to stderr. Configure local MCP clients to use `http://127.0.0.1:<port>/mcp` for the Streamable HTTP endpoint. Modern MCP `2026-07-28` clients call `server/discover` and then send direct requests with protocol metadata; legacy `2025-11-25` clients can still use `initialize` and `notifications/initialized` without relying on a transport session. The HTTP transport is loopback-only, has no authentication, and rejects unexpected Host or Origin values. Use literal loopback IPs such as `127.0.0.1` or `[::1]`; hostnames are not supported. For durable audit attribution, create an explicit `agent_session_handle` with `create_agent_session`, pass it to relevant mutating tools, and end it later with `end_agent_session`; transport closure does not end it. Use Ctrl+C or SIGTERM to stop the server. If startup fails or requests return 400/403, verify the configured address, Host header, and Origin header before retrying.
