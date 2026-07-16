# Multi-agent handoffs

## Giving a handoff

Before stopping:

1. Save an important checkpoint.
2. State completed work, remaining work, risks, blockers, and exact next steps.
3. Attach durable artifacts such as commits, branches, pull requests, files, URLs, or logs.
4. Record durable design choices as decisions, not only attempt notes.
5. Finish with `outcome: interrupted` and `interruption_reason_code: handoff`.

Never transfer the lease token. Keep notes restartable and factual; do not paste raw transcripts.

## Receiving a handoff

Refetch the issue and confirm claimability. Load checkpoint, recent notes, attempt history, artifacts, decisions, relations, and changes since the previous attempt as needed. Verify referenced repository state instead of trusting the summary as proof.

Use `get_changes` or `get_issue_activity` when concurrent work may have invalidated assumptions. Claim a new attempt; never reuse another agent's attempt ID or token.

## Review and failure

An implementation handoff identifies acceptance criteria covered, tests run, unverified behavior, and review entry points. A reviewer verifies independently and finishes with an explicit review outcome.

If work cannot continue, finish truthfully as failed, interrupted, or blocked with a stable reason, concise details, and next steps. Lease expiry is recovery protection, not a handoff mechanism.
