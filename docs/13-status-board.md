# Status board contract

This document is the canonical contract for the shipped status board in Rhizome MCP.

## 1. Scope

The status board is a loopback-only, read-only, single-user local status display. It surfaces live project state including active work attempts (leases), blocked issues and their reasons, pending review requests, and a bounded planning graph for dependency inspection.

The board is not a hosted service, does not support authentication, multi-user access, or remote deployment, and does not accept writes from the browser or any client. It is designed for a single developer running locally to inspect project state during concurrent agent work.

## 2. Output surfaces

The status board data is surfaced through four independent rendering paths, each with its own compatibility guarantee:

- **CLI table** (`rhizome-mcp board`): ASCII table with status counts, active leases, blocked issues, and the review queue. Format is human-friendly and intentionally compact. Future CLI improvements do not break existing consumption of this output.
- **CLI JSON** (`rhizome-mcp board --format json`): Complete board data as structured JSON. The JSON schema is authoritative; table rendering applies domain-specific summarization and does not carry the full scope of the JSON shape. Future schema evolution is additive; clients must tolerate unknown fields.
- **Static HTML snapshot** (`rhizome-mcp board --output PATH`): A self-contained, embeddable HTML file with inline CSS and all data needed for rendering. The snapshot is a discrete artifact; snapshots from different times are independent and do not communicate with any server. The snapshot is suitable for archival, diff, sharing, or embedding in CI reports.
- **Served board** (`rhizome-mcp board --serve`): An independent HTTP process listening at a loopback endpoint, with JSON API routes and an interactive HTML page. The served process lives outside any MCP session or command context and terminates on interrupt.

Each surface independently renders the same board result. None carry backwards compatibility burden for the others; a change to the table format is independent of the JSON schema, which is independent of the HTML rendering.

## 3. Routes and response shapes

All routes are GET/HEAD only. Any other HTTP method returns 405 Method Not Allowed with `Allow: GET, HEAD` header.

- **`GET /`** — HTML board page (interactive, same-origin fetch for updates via API routes)
- **`GET /search`** — HTML search page (issue search and snippet results)
- **`GET /issues/{id}`** — HTML issue-detail page (including attempts, activities, notes, and related issues)
- **`GET /api/board`** — JSON board data (supports ETag / If-None-Match for conditional refresh)
- **`GET /api/search`** — JSON search results. Query parameters: `q` (required; omitting it returns 400), `entity_type`, `limit`, `cursor`, `snippet_length`, `include_archived`. Any other query parameter is rejected.
- **`GET /api/issues/{id}`** — JSON issue detail (supports ETag / If-None-Match). The bare `/api/issues` path reaches the same handler but has no issue to resolve and returns 404.
- Anything else — 404 Not Found

If the underlying board service is unavailable, all routes return 503 Service Unavailable with body `service unavailable`.

## 4. ETag and If-None-Match semantics

ETags on `/api/board` and `/api/issues/{id}` are semantic, derived from the response content itself, not from timestamps or version counters. An unchanged board therefore yields byte-identical ETags and content.

A conditional request with an `If-None-Match` header matching the current ETag returns `304 Not Modified` with no body. This makes polling for board updates safe: if content has not changed, the response carries no data and the client's parse burden is zero.

## 5. HTTP method, Host, Origin and CSP posture

### Method enforcement

All routes accept GET and HEAD only. HEAD requests follow HTTP semantics: response headers are identical to GET, but no body is sent. POST, PUT, DELETE, and all other methods return 405 with the `Allow: GET, HEAD` header.

### Host and Origin validation

The server enforces the local trust boundary as described in [docs/08-local-http-transport.md](docs/08-local-http-transport.md), using the same validation rules as the MCP HTTP transport:

- A missing or unparseable `Host` header returns 400 Bad Request. A bare hostname with no port is unparseable in this sense, so `Host: example.com` returns 400.
- A well-formed `Host` authority that does not match the bound loopback authority returns 421 Misdirected Request — both a wrong port (`127.0.0.1:1`) and a wrong host (`example.com:<bound port>`).
- If an `Origin` header is present and does not exactly equal `scheme://<bound authority>`, the request is rejected with 403 Forbidden.
- If no `Origin` header is present (typical for browser navigation and non-browser MCP clients), the request is allowed.

Forwarded headers (`Forwarded`, `X-Forwarded-*`) are not trusted.

### Content Security Policy

Every response carries:

```
Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; script-src 'unsafe-inline'; font-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'
```

This policy prevents all external loads and blocks form submission from the page. Inline styles and scripts are permitted for the interactive board and search page. Images and fonts must be served by the same origin or inlined as data URIs.

### Other security headers

Every response also sets:

- `X-Content-Type-Options: nosniff` — Prevents MIME type sniffing.
- `Referrer-Policy: no-referrer` — Omits referrer information on all requests.
- `Cache-Control: no-store` — Prevents caching by browsers and intermediaries.

## 6. Bounded collections and truncation reporting

The board bounds four collections so responses stay predictable in size:

- **Blocked issues** — at most 100 entries.
- **Active attempts** — at most 100 entries.
- **Active reservations** — at most 100 entries.
- **Review requests** — at most 100 entries.

Each carries a boolean flag in the response's `truncation` object, surfaced in
the JSON response, the CLI table (as a `truncated` marker row), both HTML views
(as a text note below the table), and the semantic ETag. Each flag means "first
`MaxBoardCollectionLimit` shown; more exist", not a total. The flag is set when
the query that loaded the collection returned more results than the limit.

`attempt_gates` is one row per active attempt, so it shares `active_attempts`'s
flag.

**Active reservations pre-filter semantics:** the `truncation.active_reservations`
flag is set from the reservation page's result *before* `filterReservationsByActiveAttempts`
runs. This is deliberate, to detect when the raw query was truncated. Consequently,
a truncated pre-filter reservation list may show fewer than 100 entries after
filtering if some reservations belong to attempts that are not currently active.
Orphaned reservation rows awaiting expiry sweep can also push a live reservation
out of the visible window.

The planning graph is bounded separately, by a node budget (default 100 nodes,
maximum 500), and it *does* report its cut: the response carries `truncated` and
`truncation_reason` (`"node_limit"`) whenever a node was dropped.

Two properties of the graph's node selection matter to a board consumer:

- Non-terminal work — anything not `done` or `cancelled` — claims the node
  budget first. Terminal work is admitted afterwards, and only where a relation
  edge attaches it to a retained node. The board additionally requests the graph
  with terminal issues excluded outright, so finished work cannot consume the
  budget at all.
- `entry_points` is computed over every claimable issue in the snapshot, not
  over the returned `nodes`. This is deliberate, so that truncation can never
  shrink the set of claimable work a client is shown. The consequence is that
  `entry_points` may name an issue that does not appear in `nodes` — under the
  node budget, and equally when an issue lies outside the traversal depth.

## 7. Workflow gate visibility

The board surfaces workflow-gate state (docs/02 §17) read-only, reusing the
same compact summary `get_work_context` reports so a human reading the board
and an agent reading context see identical state (ISSUE-175).

- **Board pages and JSON** carry one gate-progress row per active attempt
  (`attempt_gates`, joined to `active_attempts` by `attempt_id`): the
  enforcement point the attempt holder will hit, the frozen snapshot
  fingerprint when one supplied the requirements, requirement/satisfied
  counts, and each unmet requirement's key and reason. The collection shares
  the active-attempts bound. The HTML attempt table renders this as a
  `Gates` column — progress as text (`1/2 satisfied`, or `none apply`), with
  unmet requirement keys listed beneath. Issues without an active attempt
  are not evaluated on the board; their summary is on their issue-detail
  page.
- **Issue-detail pages and JSON** always carry the issue's full summary
  (`Gates`): the evaluated enforcement point (an active attempt evaluates
  its frozen snapshot at `complete_work_to_done`, otherwise live policies at
  `claim_work`), progress counts, and one row per unmet requirement with its
  reason and the imperative next action that clears it. A project with no
  matching policies reports `requirement_count` 0 and the page states that
  no gate requirements apply — the no-policy compatibility case.

Gate state participates in the semantic ETags of §4, so a polling client
refreshes when a requirement is satisfied even if nothing else changed. The
served board remains read-only: gate state is displayed, never mutated, from
the browser.

## 8. Process lifetime and stdout contract

The served board is an independent process started by `rhizome-mcp board --serve` and is unrelated to any MCP session context. It holds no session state and terminates cleanly on interrupt (SIGINT/SIGTERM).

On successful listen, exactly one line is written to stdout:

```
http://HOST:PORT/
```

(with trailing slash). The VS Code extension parses this line to extract the server endpoint. Treat it as a stable contract: the format must not change.

## 9. Scope exclusions

The status board explicitly does not include:

- Write operations from the browser or any remote client. The board is read-only.
- Authentication or user accounts. It is local-only and has no credential or permission model.
- Remote or multi-user hosting. It is not designed for deployment on the internet or behind a reverse proxy.
- Cross-origin requests. CORS is not implemented; only same-origin requests (or requests with no Origin header) are accepted.
