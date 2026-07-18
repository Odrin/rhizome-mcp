# Workflow guide

Use this routine when you want safe, recoverable work in a shared repository.

By default, clients should use the stdio transport. If you need a local HTTP endpoint for a nearby client, start `rhizome-mcp serve --http-address 127.0.0.1:0` and point that client at `http://127.0.0.1:<port>/mcp`. Keep the transport loopback-only, do not expose it on a LAN or through a proxy, and do not rely on authentication because the server does not provide it. Use literal loopback IPs such as `127.0.0.1` or `[::1]`; hostname binds such as `localhost` are rejected.

## The recommended loop

1. Call `get_project` to learn the current project metadata and capabilities.
2. Review the planning graph with `get_planning_graph`, or use `list_issues` with `is_claimable=true` to find claimable entry points.
3. Call `get_work_context` for the issue you want to act on so you can review blockers, prior results, and pending next steps.
4. Claim the issue with `claim_issue`. That creates an attempt and returns a lease token.
5. While the attempt is active, call `renew_attempt` and `save_attempt_note` as needed. The lease token is required for these attempt calls and must remain private.
6. Finish the attempt once with `finish_attempt` after you have a result summary, verification, and any follow-up steps.

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

1. Request: create a review request with `create_review_request` and the exact target issue version, event position, and artifact IDs you want to preserve.
2. Discover: use `get_review_request` or `list_review_requests` to find open or claimable review requests. A claimable request stays visible until it is claimed or superseded.
3. Claim: create a review attempt with `claim_issue` against the review issue, then attach that active review attempt to the review request. If the lease expires before completion, the request returns to `open` and can be claimed again.
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

If a session disappears, rely on the lease expiry and the saved checkpoint history instead of guessing. A safe handoff includes:

- the last known issue state;
- the latest checkpoint or attempt note;
- any unresolved blocker or next step;
- the result summary and verification from the last finished attempt.

Never expose lease tokens in logs or chat history. Treat them as temporary proof for the current attempt only.
