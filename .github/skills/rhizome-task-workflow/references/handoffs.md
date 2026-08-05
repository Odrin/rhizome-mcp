# Multi-agent handoffs

## Project routing

Each agent calls `open_project` with the absolute repository root, retains the returned `project_ref`, and passes it to every subsequent project-scoped call. Project selection is not stored in MCP transport or session state. The `project_ref` is a routing token, not a lease token or secret.

## Giving a handoff

Before stopping:

1. Save an important checkpoint.
2. State completed work, remaining work, risks, blockers, and exact next steps.
3. Attach durable artifacts such as commits, branches, pull requests, files, URLs, or logs.
4. Record durable design choices as decisions, not only attempt notes.
5. Finish with `outcome: interrupted` and `interruption_reason_code: handoff`.

Never transfer the lease token. Keep notes restartable and factual; do not paste raw transcripts.

## Receiving a handoff

Call `open_project` for the target repository and retain its `project_ref`. Refetch the issue with that reference and confirm claimability. Pass the same reference to `get_work_context` when loading checkpoint, recent notes, attempt history, artifacts, decisions, relations, and changes since the previous attempt. Verify referenced repository state instead of trusting the summary as proof.

Use `get_changes` or `get_issue_activity` with the `project_ref` when concurrent work may have invalidated assumptions. Claim a new attempt with the reference; never reuse another agent's attempt ID or token.

## Review and failure

An implementation handoff identifies acceptance criteria covered, tests run, unverified behavior, and review entry points. A reviewer verifies independently and finishes with an explicit review outcome.

If work cannot continue, finish truthfully as failed, interrupted, or blocked with a stable reason, concise details, and next steps. Lease expiry is recovery protection, not a handoff mechanism.
