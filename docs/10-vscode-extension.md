# VS Code extension

The "Rhizome MCP" extension (`editors/vscode/`) is a native VS Code extension that bundles a platform-specific `rhizome-mcp` binary and registers it as an MCP server automatically, so installing from the Marketplace is enough — no separate binary download, no `.vscode/mcp.json` editing.

## Installation

Install "Rhizome MCP" (publisher `odrin`) from the [Visual Studio Marketplace](https://marketplace.visualstudio.com/items?itemName=odrin.rhizome-mcp). Run `Rhizome: Initialize Project` from the Command Palette to create `.agent-tracker.json` at the workspace root, then use Copilot's agent mode — the rhizome MCP tools appear automatically.

## Workspace detection

The extension watches each open workspace folder's root for `.agent-tracker.json` (the same marker file `rhizome-mcp init` writes). A folder's MCP server is registered only once that file exists; the extension reacts to it being created or removed without requiring a window reload.

## Binary resolution

Resolution order, implemented in `editors/vscode/src/binaryResolver.ts`:

1. **`rhizome.serverPath` setting**, if configured. An invalid value (missing file, not a file, not executable) is a hard failure — it never falls through to the next step.
2. **The binary bundled with the extension** at `<extension install dir>/bin/rhizome-mcp[.exe]`, staged there per-platform at packaging time (see Platform coverage below).
3. **`rhizome-mcp` on PATH.**

If none resolve, or the bundled binary exists but fails to execute (e.g. a wrong-architecture or corrupted binary shipped for that target), the extension shows a notification with "Open Settings" and "Install Instructions" actions rather than silently registering a broken server. A binary that spawns successfully but produces unparseable `--version` output is a soft warning, not a failure, since the server is still usable.

## Duplicate guard vs. `connect vscode`

`rhizome-mcp connect vscode` writes `.vscode/mcp.json` directly (a `servers.rhizome-mcp` entry pointing at a manually-installed binary). If that file already registers a `rhizome-mcp` server, the extension detects it and does not contribute a second one — both mechanisms can coexist in the same repository without a duplicate server appearing in the MCP Servers view.

## Platform coverage

One Go binary is built per `GOOS`/`GOARCH` pair and packaged into a VSIX per Marketplace target via `vsce package --target`. The Linux binary is CGO-free and static, so it also serves the Alpine targets as-is:

| Go binary | Marketplace target(s) |
| --- | --- |
| darwin/amd64 | darwin-x64 |
| darwin/arm64 | darwin-arm64 |
| linux/amd64 | linux-x64, alpine-x64 |
| linux/arm64 | linux-arm64, alpine-arm64 |
| windows/amd64 | win32-x64 |
| windows/arm64 | win32-arm64 |

8 VSIX targets, built from 6 Go binaries, published in one `vsce publish` call per release.

## Version and channel policy

The Marketplace requires a plain `major.minor.patch` version with no semver prerelease suffix, but this project's tags look like `v1.0.1-beta.3`. The packaging pipeline (`editors/vscode/scripts/package-platforms.mjs`) maps a tag to a Marketplace version:

- **Beta tag** `vMAJOR.MINOR.PATCH-beta.N` → Marketplace version `MAJOR.(MINOR*2+1).(PATCH*1000+N)`, published with `vsce package --pre-release` (the Marketplace's own documented convention: an odd minor version keeps pre-release builds on a separate update channel from stable).
- **Stable tag** `vMAJOR.MINOR.PATCH` (no `-beta.N`) → Marketplace version `MAJOR.MINOR.PATCH`, published without `--pre-release`. Once a stable version ships, the extension's version locks to the server's own version verbatim.

Every tagged release publishes/updates all 8 targets automatically via the `publish-vscode-extension` job in `.github/workflows/release.yml`, which is idempotent (`vsce publish --skip-duplicate`) and also exposed as a `workflow_dispatch` fallback (tag input) for a manual re-publish.

## Open VSX

VSCodium, Gitpod, Eclipse Theia, and other VS Code forks install extensions from [Open VSX](https://open-vsx.org/) instead of the Microsoft Marketplace. The `publish-open-vsx` job in `.github/workflows/release.yml` republishes the same 8 VSIXes there (`ovsx publish --skip-duplicate`), isolated the same way as the other `publish-*` jobs. It requires the `odrin` namespace and an `OVSX_PAT` secret to exist first — not yet set up, so this job currently fails harmlessly (isolated by `continue-on-error`, same as the other `publish-*` jobs) on every release until that maintainer action is done.
