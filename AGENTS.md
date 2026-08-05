# Agent Guidelines

Use the configured `rhizome-mcp` server as the source of truth for active work and durable decisions.

## Workflow

1. Call `open_project` with the absolute repository root, retain its `project_ref`, and pass that reference to every subsequent project-scoped tool call.
2. Use `get_planning_graph` or `list_issues` with the `project_ref` to select one claimable issue.
3. Call `get_work_context` with the `project_ref` before `claim_issue`; load only context sections needed for the task.
4. Claim before editing. Keep the lease token private, renew long attempts, and save restartable checkpoints.
5. Use issue comments for collaboration and decisions for durable choices. Update issues with their current `version`.
6. Call `finish_attempt` with the `project_ref` on completion, failure, blocking, or handoff. Include truthful verification, artifacts, and next steps.

Never write `in_progress`; it is derived from an active lease. Never maintain the backlog or implementation status in Markdown.

Read `rhizome://guides/agent-workflow` for the full workflow, `rhizome://guides/issue-lifecycle` for state rules, and `rhizome://guides/multi-agent-handoff` for recovery. Use the `rhizome-task-workflow` skill when executing tracked work.

Build and test commands are in [README.md](README.md); implementation contracts are indexed by [SPEC.md](SPEC.md).
