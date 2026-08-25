# Security Policy

## Reporting a vulnerability

If you discover a security vulnerability, please report it responsibly:

1. **Do not** open a public GitHub issue
2. Email the maintainer directly with:
   - Description of the vulnerability
   - Steps to reproduce (if applicable)
   - Potential impact
   - Suggested fix (if you have one)

Contact information is available in the project repository or at the GitHub repository home page (https://github.com/Odrin/rhizome-mcp).

We will acknowledge reports within 48 hours and will work with you to verify and fix the issue before public disclosure.

## Supported versions

Only the latest released minor line receives security updates; older lines
receive none. Prereleases (`vMAJOR.MINOR.PATCH-beta.N`) are supported only
until the stable release that follows them. This policy deliberately names no
specific version: consult the [releases page](https://github.com/Odrin/rhizome-mcp/releases)
for the current line. The `version` field committed in `server.json` is not a
release marker -- `release.yml` overwrites it from the git tag at publish time.

Users are encouraged to upgrade to the latest version promptly.

## Security considerations

### Local-only design

- The HTTP transport is **loopback-only** (127.0.0.1, ::1) and has **no authentication** because it is designed for local use only
- Do not expose the HTTP endpoint to untrusted networks; it is unsafe to do so
- SQLite database files and `.agent-tracker.json` should not be shared across untrusted systems

### Network surfaces

The server binds a network listener in exactly two situations, both
loopback-only and both unauthenticated by design:

- `rhizome-mcp serve --http-address ADDR` -- the MCP HTTP transport. Off by
  default; `serve` speaks stdio unless the flag or `RHIZOME_HTTP_ADDRESS` /
  the deprecated `HTTP_ADDRESS` selects an address, and it prints a stderr
  warning naming the address whenever the environment rather than the flag
  selected it, so an unexpected listener is never silent.
- `rhizome-mcp board --serve [--http-address ADDR]` -- the read-only status
  board, an independent process with its own loopback listener, separate from
  any MCP session. It accepts `GET`/`HEAD` only and never accepts writes.

Both listeners share the same controls:

- Only literal loopback addresses bind: `127.0.0.1` and `[::1]`. Wildcards,
  unspecified addresses, non-loopback IPs, hostnames, and Unix proxy targets
  are rejected before listening.
- The `Host` header must match the bound loopback authority; forwarded host
  headers are ignored. A missing or unparseable `Host` is rejected.
- An `Origin` header, when present, must exactly equal the endpoint's own
  origin; anything else is refused. No `Access-Control-*` response headers are
  ever emitted and credentials are never allowed.
- Request bodies are capped on the MCP transport (a 1 MiB outer limit and an
  8 KiB combined header limit); oversized requests are rejected rather than
  buffered. The board accepts no bodies at all.

Neither listener has authentication, so neither is safe to expose on a LAN,
through a reverse proxy, or through a tunnel. See
[docs/08-local-http-transport.md](docs/08-local-http-transport.md) for the
full MCP transport contract and
[docs/13-status-board.md](docs/13-status-board.md) for the board's routes and
CSP posture.

### No permanent agent identity

- Agents are identified by temporary session leases, not persistent credentials
- This design prevents stale agent lockouts but requires secure session management by clients

### Reservations are cooperative, not enforcement

- Resource reservations coordinate agents that go through this server; they place no lock on the filesystem
- Any process that does not use the reservation tools -- an editor, a script, another tool -- can still write a reserved file
- Reservations are not an authorization boundary: any client that holds an attempt's lease token can reserve and release on that attempt
- See [docs/12-resource-reservations.md](docs/12-resource-reservations.md) for the full guarantee and non-guarantee list

### No user authentication

- The server has no built-in authentication or authorization
- Access control must be enforced at the operating system or network boundary
- The database is a local file; restrict file system permissions appropriately

### Recommended practices

1. Store the database in a user-private directory (e.g., `~/.local/share/rhizome-mcp/`)
2. Use file system permissions to restrict database access (mode 0700 recommended)
3. Keep the SQLite database on a local filesystem (WAL mode requires local storage)
4. Upgrade to the latest version promptly when updates are available

## Release credential ownership

### VS Code Marketplace

- Publisher id: `odrin` (Azure DevOps-backed Visual Studio Marketplace publisher).
- Publishing uses a PAT scoped to **Marketplace → Manage**, stored as the `VSCE_PAT` GitHub Actions secret.
- Rotation: generate a new PAT with the same scope from the Azure DevOps organization's user settings, verify it locally with `npx @vscode/vsce verify-pat odrin`, then update the `VSCE_PAT` repository secret. Revoke the old PAT afterward. There is no fixed rotation schedule; rotate immediately if the token is suspected leaked, and otherwise before its configured expiry.

### Open VSX

- Namespace: `odrin` on [open-vsx.org](https://open-vsx.org/), linked via GitHub sign-in.
- Publishing uses an Open VSX access token, stored as the `OVSX_PAT` GitHub Actions secret.
- Rotation: generate a new token from the open-vsx.org user settings, verify it locally with `npx ovsx verify-pat odrin`, then update the `OVSX_PAT` repository secret. Revoke the old token afterward. Same policy as `VSCE_PAT`: no fixed schedule, rotate immediately if suspected leaked, otherwise before expiry.

### npm (`rhizome-mcp` and `@rhizome-mcp/*`)

- All 7 packages (`rhizome-mcp` plus the six `@rhizome-mcp/<os>-<cpu>` platform packages) are published under the `odrin` npm account, which also owns the `@rhizome-mcp` org/scope.
- Token strategy: **npm Trusted Publishing (OIDC)** — no long-lived npm token is stored in CI. Each package's Trusted Publisher is configured on npmjs.com (package → Settings → Publishing access) to trust GitHub Actions runs from `Odrin/rhizome-mcp`'s `release.yml` workflow with npm/provenance publish permissions. Trusted Publishing can only be configured for a package after its first publish, so each package started as a manually-published `0.0.1` placeholder before its Trusted Publisher was added.
- Rotation: trusted publishing has nothing to rotate (GitHub's OIDC token is short-lived and workflow-scoped). If the publish workflow file is ever renamed or moved, every package's Trusted Publisher entry must be updated to match, or publishing from CI will start failing with an auth error.
