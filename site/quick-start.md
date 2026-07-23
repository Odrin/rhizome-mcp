# Quick start

Use the steps below to install, initialize, and connect clients to `rhizome-mcp`.

## Install from the repository scripts

Choose the installer for your platform:

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

## Manual installation from release assets or source

For a manual install, open the GitHub Releases page at https://github.com/Odrin/rhizome-mcp/releases and choose the archive that matches your OS and CPU architecture (for example `rhizome-mcp_*_linux_amd64.tar.gz`, `rhizome-mcp_*_darwin_arm64.tar.gz`, or `rhizome-mcp_*_windows_amd64.zip`). Download the archive and the adjacent `.sha256` file with the same base name.

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

As an alternative to the release archive, build from source in the repository:

```bash
CGO_ENABLED=0 go build -o rhizome-mcp .
```

This keeps the installation path explicit without relying on unsupported `go install` instructions or mirrored shell scripts.

## Initialize the project

Run the binary from the repository that should be tracked. `init` writes `.agent-tracker.json` into that repository and leaves the SQLite database outside the repo.

```bash
rhizome-mcp init
```

## Optional loopback HTTP transport

The default transport is stdio. For a local HTTP endpoint instead, start the server with a literal loopback IP address:

```bash
rhizome-mcp serve --http-address 127.0.0.1:0
```

The process logs the bound endpoint to stderr. Configure local MCP clients to use `http://127.0.0.1:<port>/mcp` for the Streamable HTTP endpoint. The HTTP transport is loopback-only, has no authentication, and rejects unexpected Host or Origin values. Hostnames such as `localhost` are not supported by the current implementation, so use literal loopback IPs such as `127.0.0.1` or `[::1]`. Use Ctrl+C or SIGTERM to stop the server. If startup fails or requests return 400/403, verify the configured address, Host header, and Origin header before retrying.

## Connect common clients

### Claude Code

Use the automated setup command:

```bash
rhizome-mcp connect claude
```

This merges the configuration into a project-local `.mcp.json` file with `mcpServers.rhizome-mcp`. For a dry run, add `--print`:

```bash
rhizome-mcp connect claude --print
```

Alternatively, manually run:

```bash
claude mcp add --transport stdio rhizome-mcp -- rhizome-mcp serve
```

### VS Code

Use the automated setup command:

```bash
rhizome-mcp connect vscode
```

This merges the configuration into `.vscode/mcp.json` with `servers.rhizome-mcp`. For a dry run, add `--print`:

```bash
rhizome-mcp connect vscode --print
```

Alternatively, manually add this to `.vscode/mcp.json`:

```json
{
  "servers": {
    "rhizome-mcp": {
      "type": "stdio",
      "command": "rhizome-mcp",
      "args": ["serve"]
    }
  },
  "inputs": []
}
```

### Codex

Use the automated setup command:

```bash
rhizome-mcp connect codex
```

If `codex` is not found on PATH, or you prefer a dry run, add `--print`:

```bash
rhizome-mcp connect codex --print
```

This prints a TOML snippet for manual addition to the Codex config. Alternatively, manually run:

```bash
codex mcp add rhizome-mcp -- rhizome-mcp serve
```

### Other MCP clients

Use the generic JSON target to see a template:

```bash
rhizome-mcp connect json
```

### Antigravity

Open the MCP Servers area in the Antigravity product UI and use the raw configuration view if your release exposes it. The UI labels and location vary by release, so use the product-specific path that appears in your version. Use the in-product flow for Antigravity rather than assuming a fixed filesystem location.

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
