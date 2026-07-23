# CLI reference

The CLI is for human-facing operations such as initialization, inspection, backup, and maintenance. The MCP server itself runs with `rhizome-mcp serve` over stdio for clients by default. For local testing, `serve --http-address 127.0.0.1:0` switches to a loopback-only Streamable HTTP transport at `http://127.0.0.1:<port>/mcp`.

## Global option

Every command accepts the global `--data-root PATH` flag before the subcommand:

```bash
rhizome-mcp --data-root /path/to/data-root init
```

## Commands

### `init`

Initialize the project tracker in the current repository.

```bash
rhizome-mcp init
```

### `serve`

Start the MCP server over stdio by default.

```bash
rhizome-mcp serve
```

To opt into loopback HTTP instead, use a literal loopback IP address and an explicit port:

```bash
rhizome-mcp serve --http-address 127.0.0.1:0
```

The server logs the bound endpoint to stderr. Point local MCP clients at `http://127.0.0.1:<port>/mcp` for the Streamable HTTP endpoint. The transport is loopback-only and does not use authentication. Rejects non-loopback bind addresses, hostnames such as `localhost`, and mismatched Host/Origin headers. Use Ctrl+C or SIGTERM to stop the server. If startup fails or requests return 400/403, verify that the address uses a literal loopback IP and that the client sends the expected Host and Origin values.

### `backup`

Create a safe backup of the project database.

```bash
rhizome-mcp backup --output /path/to/project-backup.db
```

Supported flags:

- `--output PATH` (required)
- `--format table|json`

### `project export`

Export the current project as a version 1 logical interchange document.

```bash
rhizome-mcp project export --output /path/to/export.json
rhizome-mcp project export --output -
```

Supported flags:

- `--output PATH|-` (required)
- `--overwrite` (allow replacing an existing output file)

Use `project export` for JSON interchange with another Rhizome installation. The output is a logical document, not a SQLite backup. Existing files are protected unless `--overwrite` is supplied.

### `project import`

Validate or apply a version 1 logical interchange document.

```bash
rhizome-mcp project import --input /path/to/export.json --dry-run
rhizome-mcp project import --input /path/to/export.json --apply
```

Supported flags:

- `--input PATH|-` (required)
- `--dry-run` (validate without writing)
- `--apply` (apply the validated document into an empty destination)

Run `--dry-run` first to confirm the document is accepted. `--apply` is only allowed for an empty destination project; a non-empty destination returns an overwrite-protection conflict and leaves the destination unchanged. Active attempts are excluded from export and therefore cannot be imported as active history. Validation failures stop before any writes, and a new import must be retried from the last successful dry run. Only version 1 documents are accepted; future format versions are rejected before any mutation.

### `doctor`

Run a lightweight health check.

```bash
rhizome-mcp doctor
rhizome-mcp doctor --full
```

Supported flags:

- `--full`
- `--format table|json`

### `project info`

Show project metadata.

```bash
rhizome-mcp project info --format json
```

Supported flags:

- `--format table|json`

### `issue list`

List issues with optional filters.

```bash
rhizome-mcp issue list --format json --limit 20
```

Supported flags:

- `--format table|json`
- `--limit N`
- `--cursor CURSOR`
- `--type TYPE ...`
- `--status STATUS ...`
- `--effective-status STATUS ...`
- `--priority PRIORITY ...`
- `--include-archived`

### `issue show`

Show one issue by ID.

```bash
rhizome-mcp issue show ISSUE-42 --format json
```

Supported flags:

- `ISSUE-ID` (required positional argument)
- `--format table|json`

### `search`

Search indexed content for issues, comments, decisions, and attempt notes.

```bash
rhizome-mcp search "lease" --format json --limit 20
```

Supported flags:

- `QUERY` (required positional argument)
- `--format table|json`
- `--limit N`
- `--cursor CURSOR`
- `--entity-type TYPE ...`
- `--issue ISSUE-ID`
- `--epic EPIC-ID`
- `--status STATUS ...`
- `--label LABEL ...`
- `--include-archived`
- `--snippet-length N`

### `graph`

Render a graph for an issue. The `mermaid` format is useful for documentation and quick inspection.

```bash
rhizome-mcp graph ISSUE-42 --format mermaid --depth 2
```

Supported flags:

- `ISSUE-ID` (required positional argument)
- `--format table|json|mermaid`
- `--depth N`
- `--max-nodes N`
- `--direction outgoing|incoming|both`
- `--relation-type TYPE ...`
- `--include-hierarchy`
- `--include-terminal`

### `board`

Show a local, read-only status board: issue counts by effective status, currently leased attempts, blocked issues with their reasons, open review requests, and the project's planning graph.

```bash
rhizome-mcp board
rhizome-mcp board --format json
rhizome-mcp board --output board.html
```

Supported flags:

- `--format table|json`
- `--output PATH` (also write a fully self-contained HTML board to this path)

`--format table|json` controls the terminal summary, which is always printed. Adding `--output PATH` additionally writes a static HTML file with the same summary rendered as tables, an inline SVG rendering of the planning graph (plain rectangles and lines, no JavaScript), and the graph's Mermaid source in a collapsible section for pasting into any Mermaid renderer. The HTML file has no external stylesheets, scripts, fonts, or other network dependencies, so it opens correctly straight from `file://` with no network access.

### `maintenance release-attempt`

Force-release an active attempt for recovery or maintenance.

```bash
rhizome-mcp maintenance release-attempt ATTEMPT-ID --format json
```

Supported flags:

- `ATTEMPT-ID` (required positional argument)
- `--format table|json`

### `maintenance rebuild-search-index`

Rebuild the SQLite FTS search index.

```bash
rhizome-mcp maintenance rebuild-search-index --format json
```

Supported flags:

- `--format table|json`

## CLI usage summary

Use the CLI for human reads and maintenance. Use `rhizome-mcp serve` for MCP clients that need the server over stdio.
