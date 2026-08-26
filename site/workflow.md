# Workflow guide

Use this routine when you want safe, recoverable work in a shared repository.

By default, clients should use the stdio transport. If you need a local HTTP endpoint for a nearby client, start `rhizome-mcp serve --http-address 127.0.0.1:0` and point that client at `http://127.0.0.1:<port>/mcp`. That is the MCP transport. For a separate human-facing status board UI, start `rhizome-mcp board --serve --http-address 127.0.0.1:0` and open the reported loopback URL instead; the board mode is a distinct local UI and API, not the MCP transport. For workspace-specific routing, prefer `rhizome-mcp serve --project-root /absolute/path/to/workspace`; bare `serve` remains the right choice for global registrations. Keep the transport loopback-only, do not expose it on a LAN or through a proxy, and do not rely on authentication because the server does not provide it. Use literal loopback IPs such as `127.0.0.1` or `[::1]`; hostname binds are rejected. If you want durable audit attribution for a multi-step workflow, create an explicit `agent_session_handle` with `create_agent_session`, pass it on the relevant mutating calls, and finish with `end_agent_session` when the session ends. Omission remains supported for `NULL` attribution; transport reconnects and HTTP `DELETE` do not end the handle.

## The recommended loop

Start with `open_project` to get a routing token for your project, then select a claimable issue with `get_planning_graph` or `list_issues`. Claim it with `claim_issue`, save restartable checkpoints with `save_attempt_note`, and finish with `finish_attempt` once you have a truthful result and verification. See [the agent workflow guide](https://github.com/Odrin/rhizome-mcp/blob/main/.github/skills/rhizome-task-workflow/references/agent-workflow.md) for the complete workflow.

## Logical interchange and recovery

Use `project export` and `project import` when you need to move a Rhizome project between installations without treating the result as a SQLite backup. Start with a dry run:

```bash
rhizome-mcp project export --output /tmp/source.json
rhizome-mcp project import --input /tmp/source.json --dry-run
```

Apply only to an empty destination project. The import is rejected if the destination already has content, so you should initialize a fresh repository and only then run:

```bash
rhizome-mcp project import --input /tmp/source.json --apply
```

Validation failures leave the destination untouched, so recover by correcting the document or re-exporting from the source repository. Active attempts are intentionally excluded from export, which keeps lease state from being transferred across installations. Terminal attempts, notes, and artifacts are retained where they remain logically meaningful. Version 1 is the only supported format for this workflow; unsupported versions are rejected before any mutation. Keep `backup` for database snapshots and `project export`/`project import` for logical interchange.

## Review workflow quick guide

Use the review workflow when implementation is ready for a reviewer to verify it against a frozen snapshot of the target issue.

1. Request: create a review request that freezes the exact target issue version, event position, and artifact IDs you want to preserve. An open request whose target went stale is refreshed with `replace_review_request`, which supersedes it and opens a successor against the new target in one transaction.
2. Discover: use `get_review_request` or `list_review_requests` to find open or claimable review requests. A claimable request stays visible until it is claimed or superseded.
3. Claim: start a review attempt with `claim_issue` against the review issue; the claim automatically binds the issue's open review request to the new attempt in the same transaction. If the lease expires before completion, the request returns to `open` and can be claimed again.
4. Complete: finish the review attempt with `finish_attempt` using `approved`, `changes_requested`, or `blocked`. `approved` marks the review request approved and the issue `done`; `changes_requested` leaves the issue `ready` and records follow-up work; `blocked` marks the issue `blocked`.
5. Follow-up and re-request: after `changes_requested`, create a follow-up implementation task and then create a fresh review request for the new target version/event. Re-run the discover/claim/complete loop for the new request.

Recovery examples:

- If the session disappears after claim, the request returns to `open` when the lease expires. Re-discover the request and retry the claim step.
- If the implementation changed while the request was claimed, `finish_attempt` raises `STALE_REVIEW_TARGET` and the request becomes superseded. Create a new review request for the new target instead of reusing the stale one.
- If two agents race to claim the same review request, one wins and the other receives `VERSION_CONFLICT` or `ACTIVE_ATTEMPT_EXISTS`; re-discover and retry.

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

If a session disappears, rely on the lease expiry and the saved checkpoint history instead of guessing. See [the multi-agent handoff guide](https://github.com/Odrin/rhizome-mcp/blob/main/.github/skills/rhizome-task-workflow/references/multi-agent-handoff.md) for interruption, recovery, and durable checkpointing practices.

Never expose lease tokens in logs or chat history. Treat them as temporary proof for the current attempt only.
