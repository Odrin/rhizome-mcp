# Quick start

Use the steps below to install, initialize, and connect clients to `rhizome-mcp`.

## Install from the repository scripts

The repository maintains one installer source for each platform:

- [install.sh](https://github.com/Odrin/rhizome-mcp/blob/main/scripts/install.sh)
- [install.ps1](https://github.com/Odrin/rhizome-mcp/blob/main/scripts/install.ps1)

The site links to those repository files instead of copying them into the static site because there is one maintained source, the raw URLs are fetched by the terminal rather than by Docsify, and the scripts verify release checksums before they install anything. If you want a safer inspect-then-run flow, download and inspect the script first:

```bash
curl -fsSL https://raw.githubusercontent.com/Odrin/rhizome-mcp/main/scripts/install.sh -o /tmp/install.sh
sed -n '1,220p' /tmp/install.sh
sh /tmp/install.sh
```

On Windows PowerShell:

```powershell
Invoke-WebRequest -Uri https://raw.githubusercontent.com/Odrin/rhizome-mcp/main/scripts/install.ps1 -OutFile $env:TEMP\install.ps1
Get-Content $env:TEMP\install.ps1
powershell -ExecutionPolicy Bypass -File $env:TEMP\install.ps1
```

The scripts do not need mirroring into the static site. They are the maintained installation source, and the terminal fetches are the right place to inspect and run them.

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

## Connect common clients

### VS Code

The workspace configuration already uses the standard stdio contract:

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

### Claude Code

```bash
claude mcp add --transport stdio rhizome-mcp -- rhizome-mcp serve
```

The Claude client uses an `.mcp.json` file with `mcpServers`.

### Codex

```bash
codex mcp add rhizome-mcp -- rhizome-mcp serve
```

The Codex config uses TOML with `[mcp_servers.rhizome-mcp]`.

### Antigravity

Open the MCP Servers area in the Antigravity product UI and use the raw configuration view if your release exposes it. The UI labels and location vary by release, so use the product-specific path that appears in your version. The standard JSON entry is:

```json
{
  "mcpServers": {
    "rhizome-mcp": {
      "command": "rhizome-mcp",
      "args": ["serve"]
    }
  }
}
```

Use the in-product flow for Antigravity rather than assuming a fixed filesystem location.

## Verify the setup

```bash
rhizome-mcp doctor
rhizome-mcp project info --format json
```

If you want a deeper health check:

```bash
rhizome-mcp doctor --full
```
