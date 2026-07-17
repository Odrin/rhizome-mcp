# Local HTTP transport contract

## Transport

Rhizome uses the pinned `github.com/modelcontextprotocol/go-sdk` v1.6.1
Streamable HTTP transport at `POST /mcp`. The server also accepts the
transport's `GET /mcp` SSE stream and `DELETE /mcp` session termination
requests as defined by the SDK. Legacy MCP SSE endpoints are not supported.

Stdio remains the default and is unchanged. HTTP tool results and structured
domain errors must be identical to stdio.

## Binding and configuration

HTTP is opt-in through `rhizome-mcp serve --http-address HOST:PORT`.
The default HTTP address is `127.0.0.1:0`; port zero selects an ephemeral
port and the selected endpoint is logged on stderr. Explicit configuration
takes precedence over defaults. Stdio has no HTTP listener unless this option
is present.

Only literal loopback addresses are valid: `127.0.0.0/8`, `::1`, and
`localhost` after resolution exclusively to those addresses. Wildcards,
unspecified addresses, non-loopback IPs, hostnames resolving to any
non-loopback address, and Unix proxy targets are rejected before listening.

## Local trust boundary

HTTP has no authentication because it is local-only. It is not safe to expose
on a LAN, through a reverse proxy, or through a tunnel.

- Host must be the configured loopback authority; forwarded host headers are
  ignored.
- Origins are denied by default. Requests with an Origin header must exactly
  match the configured endpoint origin; credentials are never allowed.
- CORS is not a general browser API: only the required MCP request methods and
  headers are permitted for the same local origin.
- The server does not trust `Forwarded` or `X-Forwarded-*` headers.
- DNS rebinding is mitigated by literal bind validation plus Host and Origin
  checks.

## Operational limits

The initial implementation uses a 10 second read-header timeout, 30 second
read/write request timeouts, 60 second idle timeout, a 1 MiB request body
limit, and an 8 KiB combined header limit. Limits are configuration values
only when constrained to equal-or-safer bounds. Access logs include request
and session correlation IDs but never request payloads, lease tokens, or
artifact metadata.

Shutdown stops new accepts, cancels/drains requests within the configured
timeout, and closes listener resources. Startup and bind failures are fatal
and are reported before any ready endpoint is logged.
