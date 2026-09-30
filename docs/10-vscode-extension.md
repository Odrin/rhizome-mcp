# VS Code extension

The "Rhizome MCP" extension (`editors/vscode/`) is a native VS Code extension that bundles a platform-specific `rhizome-mcp` binary and registers it as an MCP server automatically, so installing from the Marketplace is enough — no separate binary download, no `.vscode/mcp.json` editing. For workspace-specific project routing, the extension can start the server with an absolute project root via `serve --project-root <absolute-root>`; the canonical contract is documented in [docs/11-project-routing.md](docs/11-project-routing.md).

## Installation

Install "Rhizome MCP" (publisher `odrin`) from the [Visual Studio Marketplace](https://marketplace.visualstudio.com/items?itemName=odrin.rhizome-mcp). Run `Rhizome: Initialize Project` from the Command Palette to create `.agent-tracker.json` at the workspace root, then use Copilot's agent mode — the rhizome MCP tools appear automatically.

## Workspace detection

The extension watches each open workspace folder's root for `.agent-tracker.json` (the same marker file `rhizome-mcp init` writes). A folder's MCP server is registered only once that file exists; the extension reacts to it being created or removed without requiring a window reload.

## Status board UI

The extension can also launch a local status board for human inspection. When the extension opens the board, it starts `rhizome-mcp board --serve`, opens the reported loopback URL, keeps the child process alive for extension use, and terminates it again when the board is reopened or the extension is deactivated. The manual `rhizome-mcp board --output` flow remains available as an offline snapshot for cases where the user wants a self-contained HTML file without a running server. See [docs/13-status-board.md](docs/13-status-board.md) for the authoritative contract of routes, response shapes, security posture, and data bounds.

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

- **Beta tag** `vMAJOR.MINOR.PATCH-beta.N`, with `N` from `0` through `998` → Marketplace version `(MAJOR+2).MINOR.(PATCH*1000+N+1)`, published with `vsce package --pre-release`.
- **Stable tag** `vMAJOR.MINOR.PATCH` → Marketplace version `(MAJOR+2).MINOR.((PATCH+1)*1000)`, published without `--pre-release`.

The fixed epoch offset `2` migrates all new tagged versions above the already-published legacy `1.x` history. The public Marketplace history for `odrin.rhizome-mcp`, checked on 2026-09-30, tops out at `1.5.2` and also includes legacy beta versions `1.1.1003`, `1.1.1004`, and `1.1.1005`. New mapped versions never reuse those identifiers. The extension version intentionally differs from the bundled server's product version; the server binary still matches the release tag.

Within a product patch, beta slots `1..999` precede stable. The next patch's `beta.0` is one version higher than the previous stable, and minor/major product increments retain numeric ordering. For example:

| Product tag | Extension version | Channel |
| --- | --- | --- |
| `v1.0.1-beta.2` | `3.0.1003` | Pre-release |
| `v1.0.1-beta.998` | `3.0.1999` | Pre-release |
| `v1.0.1` | `3.0.2000` | Stable |
| `v1.0.2-beta.0` | `3.0.2001` | Pre-release |
| `v1.0.2` | `3.0.3000` | Stable |
| `v1.1.0-beta.0` | `3.1.1` | Pre-release |
| `v1.1.0` | `3.1.1000` | Stable |

The `--pre-release` flag selects the channel; odd/even minor numbering is no longer used. Only canonical decimal tag components are accepted, with no leading zeroes. Numeric components are capped conservatively at `2147483647`, so product major must be at most `2147483645`, minor at most `2147483647`, and patch at most `2147482`; beta ordinals above `998` are rejected. These bounds guarantee that every accepted beta has an in-range stable successor. Invalid or out-of-range tags fail rather than wrap, clamp, or collide. Untagged local packaging retains the manifest version fallback and is not a release-distribution version.

Every tagged release publishes/updates all 8 targets automatically via the `publish-vscode-extension` job in `.github/workflows/release.yml`, which is idempotent (`vsce publish --skip-duplicate`) and also exposed as a `workflow_dispatch` fallback (tag input) for a manual re-publish. The dispatch path is version-safe: it checks out the tagged source code (not `main`), ensuring the extension's TypeScript and the tag's server binary always ship in lockstep.

Historical tags still contain their historical packaging script, so dispatching an old tag does not migrate its mapping. Ship a forward product release containing this policy to move installed extensions to the new epoch. Repeating that same new-policy tag intentionally reuses its own deterministic version; distinct accepted tags map to distinct new identifiers.

## Open VSX

VSCodium, Gitpod, Eclipse Theia, and other VS Code forks install extensions from [Open VSX](https://open-vsx.org/) instead of the Microsoft Marketplace. The `publish-open-vsx` job in `.github/workflows/release.yml` republishes the same 8 VSIXes there (`ovsx publish --skip-duplicate`), isolated the same way as the other `publish-*` jobs. Requires the `odrin` namespace and an `OVSX_PAT` secret (ISSUE-97), both set up and verified against a real release.
