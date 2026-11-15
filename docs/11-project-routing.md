# Project routing contract

This document is the canonical contract for shipped project routing in Rhizome MCP.

## 1. API and routing keys

- All project-dependent MCP tools accept an optional nullable canonical `project_ref`.
- `open_project` is the projectless entry point. It attaches to an existing project using either `project_root` (an absolute filesystem root when client and server share a filesystem) or `project_id` (the canonical ULID from `.agent-tracker.json` when the server runs on another host, such as stdio over SSH). If you already hold a valid `project_ref`, `get_project(project_ref)` is equivalent.
- `project_ref` is a routing token only. It does not authenticate, authorize, or change storage location; it simply selects the project that the server should serve.
- MCP routing uses ordinary tool arguments. It does not use `Roots`, `_meta`, HTTP headers, or transport-session state to choose a project.

## 2. Stateless tool workflow

1. Call `open_project` with `project_root` (when sharing a filesystem) or `project_id` from `.agent-tracker.json` (when the server runs on another host), or call `get_project(project_ref)` if you already have a valid `project_ref`.
2. Retain the returned canonical `project_ref`.
3. Pass that `project_ref` to every subsequent project-scoped tool call, including `get_project`, discovery, context, claim, checkpoint, and finish operations.

Calling `open_project` does not mutate an MCP session or establish an implicit current project. A later request without `project_ref` uses the server's configured default when one exists and otherwise fails with `PROJECT_REQUIRED`. `project_ref` remains stable routing data and may be retained across transport reconnects; lease tokens remain private and attempt-specific.

## 3. Serve-time project selection

When the server starts with `serve`, project resolution uses the following precedence:

1. `--project-root <absolute-root>`
2. `RHIZOME_PROJECT_ROOT`
3. Current working directory discovery
4. Shared mode only when current-working-directory discovery returns `PROJECT_NOT_FOUND`

This means a workspace-specific registration should pass an absolute root explicitly, while a generic global registration can keep using bare `serve`.

## 4. Default opening vs. routed opens

The server distinguishes two cases:

- A normal default opening is the ordinary startup path for a server instance that resolves one project root at startup.
- A routed open is an existing-only request-driven open for a known project selected by `project_ref`, `project_root`, or marker `project_id`.

Routed opens are existing-only under one configured data root. They do not create a new project database and they do not run init or migration flows for a missing project. A stale database is not silently auto-migrated by routed `project_ref` access; instead, the non-mutating open error includes the stored and expected versions and tells the user to run `rhizome-mcp projects migrate --project-id ID` to apply the supported forward migration explicitly. The migration path is projectless and uses the canonical stored project ID, not a repository root or a newly created marker.

## 5. Router behavior and lifecycle

- The router has capacity 16 entries, including the default opening.
- The default opening is pinned and is not evicted while it remains the default.
- Idle entries are evicted by LRU when the router reaches capacity.
- Active leases prevent a project from being closed by the router while the lease is still active.
- Opening by an absolute root records that validated checkout path in the cached entry, including after relocation. Database-only opens do not infer a current checkout root from the historical database origin; project briefs report `root_path: null` until a root is validated.
- Shutdown drains in-flight work and then closes the router cleanly.

## 6. Routing isolation

Routing is isolated to the server process that receives the request. Requests do not cross-search projects, and a routed request only targets the project selected by the supplied routing argument. A server process may keep multiple project openings, but the router still enforces its capacity and eviction rules.

## 7. HTTP trust boundary

Project routing is not a network security boundary. The local HTTP transport is loopback-only and does not add authentication; any local caller that knows a valid absolute project root or `project_ref` can target a known project over that loopback endpoint. The transport still enforces loopback binding, Host/Origin checks, and the local-only trust boundary.

## 8. Compatibility and no migration

This behavior is additive and compatible with existing single-project startup flows. No migration is required for existing installations. Existing `.agent-tracker.json`-based workflows continue to work, and clients that previously used bare `serve` remain compatible.

## 9. Examples

`rhizome-mcp connect TARGET` generates the workspace-specific form below
automatically for `claude`, `codex`, `vscode`, `cursor`, `windsurf`, `zed`,
`gemini`, and `json` alike, discovering the actual project root (walking up
from the current directory, the same resolution `serve` itself falls back to)
unless `--project-root PATH` names an explicit initialized root or `--global`
selects a projectless global registration. When `--global` is set, `connect`
skips project discovery and omits `--project-root` from `args` (note that
`vscode` MCP configuration is workspace-scoped, so `connect vscode --global`
requires `--print`; `windsurf` writes its single global file
`~/.codeium/windsurf/mcp_config.json` and still pins `--project-root` unless
`--global` is passed; `connect zed --global` resolves `%APPDATA%\Zed\settings.json`
on Windows, `$XDG_CONFIG_HOME/zed/settings.json` or the Flatpak path
`~/.var/app/dev.zed.Zed/config/zed/settings.json` when present on Linux, and
`~/.config/zed/settings.json` otherwise). Merging into existing JSON config
files strips JSONC comments (`//`, `/* */`) while preserving token boundaries,
and removes trailing commas so Zed and Gemini settings files parse cleanly.
Malformed JSONC is rejected without modifying the existing file.
`--profile` or `--toolsets` (mutually exclusive) may
also be passed to bake those flags into the emitted `serve` arguments. The
written `command` is this resolved binary's absolute path by default; when
`connect` is itself running through the `npx rhizome-mcp` wrapper it emits
`npx` instead (the wrapper's own resolved path lives in the npx cache and is
not stable across evictions or version bumps), and `connect TARGET --command`
opts into a bare `rhizome-mcp` command name for a config meant to be shared
across machines that have it on `PATH`. An absolute-path config is
machine-specific and should not be treated as portable or checked into a
shared repository as-is.

Workspace-specific registration:

```json
{
  "mcpServers": {
    "rhizome-mcp": {
      "command": "rhizome-mcp",
      "args": ["serve", "--project-root", "/absolute/path/to/workspace"]
    }
  }
}
```

Global registration that does not pin a workspace root:

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

The projectless opening call uses either an absolute root (on a shared filesystem):

```json
{
  "name": "open_project",
  "arguments": {
    "project_root": "/absolute/path/to/workspace"
  }
}
```

or a marker `project_id` from `.agent-tracker.json` when the server runs on another host (e.g. stdio over SSH):

```json
{
  "name": "open_project",
  "arguments": {
    "project_id": "01ARZ3NDEKTSV4RRFFQ69G5FAV"
  }
}
```

Every later routed tool call uses the returned reference, for example:

```json
{
  "name": "get_project",
  "arguments": {
    "project_ref": "01J..."
  }
}
```
