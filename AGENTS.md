# Agent Guidelines

Use the configured `rhizome-mcp` server as the source of truth for active work and durable decisions.

## Workflow

Claim tracked work through the rhizome-mcp server before editing. Keep the returned lease token private and available until the attempt completes, renewing before expiry and saving restartable checkpoints. Finish every attempt with its truthful outcome, verification actually performed, and next steps.

See [the agent workflow guide](.github/skills/rhizome-task-workflow/references/agent-workflow.md) for the complete select/claim/execute/finish workflow.

Never write `in_progress`; it is derived from an active lease. Never maintain the backlog or implementation status in Markdown.

Read `rhizome://guides/agent-workflow` for the full workflow, `rhizome://guides/issue-lifecycle` for state rules, and `rhizome://guides/multi-agent-handoff` for recovery. Use the `rhizome-task-workflow` skill when executing tracked work.

Build and test commands are in [README.md](README.md); implementation contracts are indexed by [SPEC.md](SPEC.md).
