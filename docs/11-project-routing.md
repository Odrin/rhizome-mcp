# Project routing contract

This document is the canonical contract for shipped project routing in Rhizome MCP.

## 1. API and routing keys

- All project-dependent MCP tools accept an optional nullable canonical `project_ref`.
- `open_project(project_root)` is the projectless entry point. It targets an absolute filesystem root and is used to attach to a specific project.
- `project_ref` is a routing token only. It does not authenticate, authorize, or change storage location; it simply selects the project that the server should serve.
- MCP routing uses ordinary tool arguments. It does not use `Roots`, `_meta`, HTTP headers, or transport-session state to choose a project.

## 2. Serve-time project selection

When the server starts with `serve`, project resolution uses the following precedence:

1. `--project-root <absolute-root>`
2. `RHIZOME_PROJECT_ROOT`
3. Current working directory discovery
4. Shared mode only when current-working-directory discovery returns `PROJECT_NOT_FOUND`

This means a workspace-specific registration should pass an absolute root explicitly, while a generic global registration can keep using bare `serve`.

## 3. Default opening vs. routed opens

The server distinguishes two cases:

- A normal default opening is the ordinary startup path for a server instance that resolves one project root at startup.
- A routed open is an existing-only request-driven open for a known project selected by `project_ref` or `project_root`.

Routed opens are existing-only under one configured data root. They do not create a new project database and they do not run init or migration flows for a missing project.

## 4. Router behavior and lifecycle

- The router has capacity 16 entries, including the default opening.
- The default opening is pinned and is not evicted while it remains the default.
- Idle entries are evicted by LRU when the router reaches capacity.
- Active leases prevent a project from being closed by the router while the lease is still active.
- Shutdown drains in-flight work and then closes the router cleanly.

## 5. Routing isolation

Routing is isolated to the server process that receives the request. Requests do not cross-search projects, and a routed request only targets the project selected by the supplied routing argument. A server process may keep multiple project openings, but the router still enforces its capacity and eviction rules.

## 6. HTTP trust boundary

Project routing is not a network security boundary. The local HTTP transport is loopback-only and does not add authentication; any local caller that knows a valid absolute project root or `project_ref` can target a known project over that loopback endpoint. The transport still enforces loopback binding, Host/Origin checks, and the local-only trust boundary.

## 7. Compatibility and no migration

This behavior is additive and compatible with existing single-project startup flows. No migration is required for existing installations. Existing `.agent-tracker.json`-based workflows continue to work, and clients that previously used bare `serve` remain compatible.

## 8. Examples

Workspace-specific registration:

```json
{
  "mcpServers": {
    "rhizome": {
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
    "rhizome": {
      "command": "rhizome-mcp",
      "args": ["serve"]
    }
  }
}
```

A routed tool call uses ordinary tool arguments, for example:

```json
{
  "name": "get_project",
  "arguments": {
    "project_ref": "01J..."
  }
}
```
