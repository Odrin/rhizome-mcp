# Project routing

Rhizome MCP now supports project-aware serving for workspace-specific MCP clients while preserving the simple default `serve` experience.

## When to use project routing

Use `serve --project-root /absolute/path/to/workspace` when you want a server instance to target a specific workspace root. That is the recommended pattern for workspace-specific MCP registrations.

If you do not need to pin a workspace, a plain `serve` command is still the right choice.

## How it works

- The server resolves the project from `--project-root` first, then `RHIZOME_PROJECT_ROOT`, then the current working directory.
- Routed opens are existing-only. They do not create a new project database and they do not run init or migration flows for a missing project.
- Project routing is carried by ordinary tool arguments such as `project_ref`; it is not based on Roots, `_meta`, headers, or transport-session state.
- Agents call `open_project` with an absolute repository root, retain its returned `project_ref`, and pass the reference to every later project-scoped tool call.
- Calling `open_project` does not establish an implicit current project. Omitting `project_ref` uses a configured default when available and otherwise returns `PROJECT_REQUIRED`.

## Examples

Workspace-specific registration:

```bash
rhizome-mcp serve --project-root /absolute/path/to/workspace
```

Global registration:

```bash
rhizome-mcp serve
```

Stateless tool sequence:

```json
{
	"name": "open_project",
	"arguments": {"project_root": "/absolute/path/to/workspace"}
}
```

Retain `project_ref` from that result and include it in later arguments:

```json
{
	"name": "list_issues",
	"arguments": {
		"project_ref": "01J...",
		"is_claimable": true
	}
}
```

For the full contract, see [docs/11-project-routing.md](../docs/11-project-routing.md).
