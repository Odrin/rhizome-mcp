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
