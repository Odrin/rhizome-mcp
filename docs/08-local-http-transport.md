# Local HTTP transport contract

## Transport

Rhizome uses the pinned `github.com/modelcontextprotocol/go-sdk` v1.7.0
Streamable HTTP transport at `POST /mcp`. The server supports both MCP
protocol eras over that endpoint:

- Modern `2026-07-28` is stateless. A client calls `server/discover` first and
  then sends direct requests with the requested protocol metadata. No
  `initialize`/`initialized` exchange and no `Mcp-Session-Id` header are
  required.
- Legacy `2025-11-25` remains accepted. A client can still send
  `initialize` followed by `notifications/initialized` and then ordinary calls
  without relying on a persistent transport session.

`GET /mcp` and `DELETE /mcp` are SDK/protocol transport operations only. They
are not durable Rhizome agent-session lifecycle APIs.

### Compact request-flow examples

Modern `2026-07-28` flow:

```bash
curl -X POST http://127.0.0.1:PORT/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'Mcp-Protocol-Version: 2026-07-28' \
  -d '{"jsonrpc":"2.0","id":"1","method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}'
```

Then send ordinary requests to the same endpoint with the same protocol metadata.

Legacy `2025-11-25` flow:

```bash
curl -X POST http://127.0.0.1:PORT/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'Mcp-Protocol-Version: 2025-11-25' \
  -d '{"jsonrpc":"2.0","id":"1","method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"example","version":"1.0.0"}}}'
```

```bash
curl -X POST http://127.0.0.1:PORT/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'Mcp-Protocol-Version: 2025-11-25' \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}'
```

After that, ordinary tool calls continue without any persistent transport
session header.

## Explicit agent-session handles

Durable audit attribution is separate from MCP transport lifecycle. Create an
explicit handle with `create_agent_session`, pass `agent_session_handle` on the
relevant mutating calls, and end it later with `end_agent_session` when the
work is complete. Omitting the handle remains supported and yields `NULL`
attribution.

A handle is an opaque bearer string, not a transport credential. Connection
IDs, HTTP `DELETE`, process shutdown, and transport closure never end a
Rhizome agent-session handle. If a client loses the handle, it must create a
fresh one; the transport does not recover or replay that value for the server.

## Binding and configuration

HTTP is opt-in through `rhizome-mcp serve --http-address HOST:PORT`, or
through the `RHIZOME_HTTP_ADDRESS` environment variable (the unprefixed
`HTTP_ADDRESS` name is a deprecated fallback for one release; docs/04 §17
and docs/03 §5.1 describe the full namespaced-vs-legacy precedence). Project
routing is independent of the transport: a workspace-specific server can
still be started with `serve --project-root <absolute-root>`, while global
registrations remain compatible with bare `serve`.
The default HTTP address is `127.0.0.1:0`; port zero selects an ephemeral
port and the selected endpoint is logged on stderr. An explicit
`--http-address` flag always wins over the environment. Stdio has no HTTP
listener unless the flag or one of these environment variables selects one
-- and because an inherited environment variable can select HTTP transport
without an explicit flag on the command line, `serve` prints a stderr
warning naming the address whenever the environment (not the flag) is what
selected it, so an unexpected HTTP listener is never silent.

Only literal loopback addresses are valid: `127.0.0.1` and `[::1]`. Wildcards,
unspecified addresses, non-loopback IPs, hostnames, and Unix proxy targets are
rejected before listening.

## Local trust boundary

HTTP has no authentication because it is local-only. It is not safe to expose
on a LAN, through a reverse proxy, or through a tunnel.

- Host must be the configured loopback authority; forwarded host headers are
  ignored.
- Origins are denied by default. Requests with an Origin header must exactly
  match the configured endpoint origin; credentials are never allowed.
- The server never emits CORS response headers (no `Access-Control-*`
  headers, no preflight handling). This is not permissive CORS scoped to the
  local origin — it is the absence of CORS: only same-origin requests (or
  requests with no Origin header at all, e.g. from non-browser MCP clients)
  are accepted; every other origin is rejected outright at the Origin check
  above, not granted narrowed cross-origin access.
- The server does not trust `Forwarded` or `X-Forwarded-*` headers.
- DNS rebinding is mitigated by literal bind validation plus Host and Origin
  checks.

## Operational limits

The implementation uses a 1 MiB outer request body limit and an 8 KiB combined
header limit. Logs record request method, path, status, and duration along with
safe MCP protocol/method/tool fields; they never log handles, lease tokens, or
parameter values.

Shutdown stops new accepts, cancels/drains requests within the configured
timeout, and closes listener resources. Startup and bind failures are fatal
and are reported before any ready endpoint is logged.
