# Project routing

Rhizome MCP now supports project-aware serving for workspace-specific MCP clients while preserving the simple default `serve` experience.

## When to use project routing

Use `serve --project-root /absolute/path/to/workspace` when you want a server instance to target a specific workspace root. That is the recommended pattern for workspace-specific MCP registrations.

If you do not need to pin a workspace, a plain `serve` command is still the right choice.

## How it works

- The server resolves the project from `--project-root` first, then `RHIZOME_PROJECT_ROOT`, then the current working directory.
- Routed opens are existing-only. They do not create a new project database and they do not run init or migration flows for a missing project.
- Project routing is carried by ordinary tool arguments such as `project_ref`; it is not based on Roots, `_meta`, headers, or transport-session state.

## Examples

Workspace-specific registration:

```bash
rhizome-mcp serve --project-root /absolute/path/to/workspace
```

Global registration:

```bash
rhizome-mcp serve
```

For the full contract, see [docs/11-project-routing.md](../docs/11-project-routing.md).
