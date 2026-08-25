# Multi-agent handoff

## Project routing

Each agent calls `open_project` with the absolute repository root, retains the returned `project_ref`, and passes it to every subsequent project-scoped call. Project selection is not stored in MCP transport or session state. The `project_ref` is a routing token, not a lease token or secret.

## Before handing off

Preserve enough state for another agent to continue without reconstructing your session:

1. Save an important checkpoint with `save_attempt_note`.
2. State what changed, what remains, current risks or blockers, and exact next steps.
3. Attach durable artifacts: commit, branch, pull request, relevant file, URL, or verification log.
4. Record durable design choices with `record_decision`; do not bury them only in a checkpoint.
5. Finish the attempt as `interrupted` with reason `handoff`. Never transfer or publish the lease token.

Keep notes factual and restartable. Avoid raw transcripts, speculative status, and duplicated repository documentation.

## Receiving a handoff

1. Call `open_project` for the target repository and retain its `project_ref`.
2. Refetch the issue with that `project_ref` and confirm it is claimable.
3. Call `get_work_context` with the `project_ref` and request checkpoint, recent attempt notes, attempt history, artifacts, decisions, relations, and changes since the previous attempt as needed.
4. Inspect referenced artifacts and verify the repository state; do not trust a summary as proof.
5. Use `get_changes` from the prior context event ID or `get_issue_activity` when concurrent work may have changed assumptions.
6. Claim a new attempt with the `project_ref`. Never reuse another agent's attempt ID or lease token.

## Concurrent changes

Leases prevent duplicate active ownership of an issue, not edits elsewhere in the project. Before finishing, check relevant changes and reconcile newer issue versions, decisions, blockers, and artifacts. If the server requires acknowledgement, send the observed issue version and latest event ID.

## Review handoff

An implementation handoff should identify acceptance criteria covered, tests run, unverified behavior, and review entry points. A reviewer independently verifies artifacts and finishes its review attempt with an explicit review outcome. A review attempt finishes with one explicit review outcome, which the server maps to issue state: `approved` marks the review request approved and the issue `done`; `changes_requested` returns the issue to `ready`; `blocked` marks the issue `blocked`. A `changes_requested` outcome must name the failing acceptance criterion and concrete next steps.

## Failure and recovery

If work cannot continue, finish the attempt truthfully as failed, interrupted, or blocked. Include a stable reason code, concise details, and next steps. Do not leave an active attempt as an implicit handoff; lease expiry is recovery protection, not a workflow.
