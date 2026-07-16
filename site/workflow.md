# Workflow guide

Use this routine when you want safe, recoverable work in a shared repository.

## The recommended loop

1. Call `get_project` to learn the current project metadata and capabilities.
2. Review the planning graph with `get_planning_graph`, or use `list_issues` with `is_claimable=true` to find claimable entry points.
3. Call `get_work_context` for the issue you want to act on so you can review blockers, prior results, and pending next steps.
4. Claim the issue with `claim_issue`. That creates an attempt and returns a lease token.
5. While the attempt is active, call `renew_attempt` and `save_attempt_note` as needed. The lease token is required for these attempt calls and must remain private.
6. Finish the attempt once with `finish_attempt` after you have a result summary, verification, and any follow-up steps.

## Optional human inspection

These CLI commands are useful for human inspection or debugging, but they are not substitutes for the MCP claim/work-context APIs above.

```bash
rhizome-mcp project info --format json
rhizome-mcp issue list --format json --limit 20
rhizome-mcp graph ISSUE-42 --format mermaid --depth 2
```

## Status and lease semantics

The workflow uses an effective `in_progress` state rather than a stored `in_progress` flag. An issue becomes effectively in progress only while an active lease exists. When the lease expires, the attempt becomes expired and the issue becomes claimable again if the stored state allows it.

This makes recovery deterministic and prevents issues from becoming permanently stuck.

## Durable decisions versus operational notes

Use durable decisions for product or technical choices that should survive future work. Use comments and attempt notes for temporary operational context.

- Record durable decisions when a design choice should remain visible for later agents.
- Use comments for short collaboration context.
- Use attempt notes and checkpoints for progress, findings, warnings, and handoff details.

## Recovery and handoff

If a session disappears, rely on the lease expiry and the saved checkpoint history instead of guessing. A safe handoff includes:

- the last known issue state;
- the latest checkpoint or attempt note;
- any unresolved blocker or next step;
- the result summary and verification from the last finished attempt.

Never expose lease tokens in logs or chat history. Treat them as temporary proof for the current attempt only.
