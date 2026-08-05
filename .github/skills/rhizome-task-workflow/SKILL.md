---
name: rhizome-task-workflow
description: "Use when selecting, claiming, executing, reviewing, checkpointing, handing off, or finishing tasks tracked by the rhizome-mcp MCP server."
argument-hint: "Describe the task, issue, or outcome to coordinate."
---

# Rhizome Task Workflow

1. Call `open_project` with the absolute repository root. Retain its `project_ref` and pass it to every later project-scoped tool call.
2. Find claimable work with `get_planning_graph` or `list_issues` using that `project_ref`.
3. Read `get_work_context`, then claim exactly one issue before editing.
4. Renew long leases and save restartable checkpoints with artifacts.
5. Finish every attempt with its truthful outcome, verification, and next steps.

Use:

- [Task execution](./references/task-execution.md) for the complete workflow.
- [Issue lifecycle](./references/issue-lifecycle.md) for statuses, blockers, relations, and concurrency.
- [Handoffs](./references/handoffs.md) for interruption, recovery, and review.

The equivalent server guides are linked in the metadata returned by `open_project` and `get_project`.
