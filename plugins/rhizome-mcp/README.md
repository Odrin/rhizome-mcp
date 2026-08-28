# rhizome-mcp Claude Code plugin

Installs the rhizome-mcp MCP server (via `npx`, no binary install needed) and
the two workflow skills (`rhizome-task-workflow`, `rhizome-execution-plan`)
into Claude Code:

```
/plugin marketplace add Odrin/rhizome-mcp
/plugin install rhizome-mcp@rhizome
```

Each repository you want tracked still needs a one-time
`npx rhizome-mcp init` in its root — that writes the single
`.agent-tracker.json` file the server resolves projects through.

The `skills/` directory here is a synchronized copy of the canonical
[`.github/skills/`](../../.github/skills) (kept identical by
`plugin_skills_sync_test.go`; refresh with
`bash scripts/sync-plugin-skills.sh`). The canonical location stays where the
[`skills` CLI](https://skills.sh/) expects it, so
`npx skills add Odrin/rhizome-mcp` keeps working for non-plugin agents.

Full documentation: <https://odrin.github.io/rhizome-mcp/>
