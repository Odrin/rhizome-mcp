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

| Version | Status | Security fixes |
| --- | --- | --- |
| 1.0.x | Current | Yes |

Only the latest minor release (1.0.x) receives security updates. Users are encouraged to upgrade to the latest version promptly.

## Security considerations

### Local-only design

- The HTTP transport is **loopback-only** (127.0.0.1, ::1) and has **no authentication** because it is designed for local use only
- Do not expose the HTTP endpoint to untrusted networks; it is unsafe to do so
- SQLite database files and `.agent-tracker.json` should not be shared across untrusted systems

### No permanent agent identity

- Agents are identified by temporary session leases, not persistent credentials
- This design prevents stale agent lockouts but requires secure session management by clients

### No user authentication

- Version 1.0.0 has no built-in authentication or authorization
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
