# Agent Guidelines

Use the configured `rhizome-mcp` server as the source of truth for active work and durable decisions.

## Workflow

1. Call `get_project`, then use `get_planning_graph` or `list_issues` to select one claimable issue.
2. Call `get_work_context` before `claim_issue`; load only context sections needed for the task.
3. Claim before editing. Keep the lease token private, renew long attempts, and save restartable checkpoints.
4. Use issue comments for collaboration and decisions for durable choices. Update issues with their current `version`.
5. Call `finish_attempt` on completion, failure, blocking, or handoff. Include truthful verification, artifacts, and next steps.

Never write `in_progress`; it is derived from an active lease. Never maintain the backlog or implementation status in Markdown.

Read `rhizome://guides/agent-workflow` for the full workflow, `rhizome://guides/issue-lifecycle` for state rules, and `rhizome://guides/multi-agent-handoff` for recovery. Use the `rhizome-task-workflow` skill when executing tracked work.

Build and test commands are in [README.md](README.md); implementation contracts are indexed by [SPEC.md](SPEC.md).
