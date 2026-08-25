# Agent workflow

## 1. Orient

Call `open_project` with the absolute repository root. Retain the returned `project_ref` and pass it to every subsequent project-scoped tool call, including `get_project`. Project routing is stateless: `open_project` does not select a project for later requests. Omit `project_ref` only when intentionally relying on a server configured with a default project.

Call `get_project` with that `project_ref` when project instructions are needed. Use the metadata returned by `open_project` or `get_project` for limits, supported values, the latest event ID, and guide links. Read only the guide needed for the current operation.

Audit attribution is opt-in. Call `create_agent_session` once to obtain an `agent_session_handle`, pass it to the mutating tools used during the workflow, and call `end_agent_session` when the workflow ends. Omitting the handle is fully supported and records `NULL` attribution. The handle is independent of the transport: reconnecting, or an HTTP `DELETE`, does not end it.

## 2. Find work

- Use `get_planning_graph` for dependency-aware selection. Entry points are executable roots; blocking nodes explain stalled work.
- Use `list_issues` with `is_claimable: true` for a narrow ready queue.
- Use `search` for historical knowledge, not as the authoritative current state.
- Follow cursors or event IDs when a result says more data exists.

Pass the retained `project_ref` on discovery calls. Select one coherent issue. Do not begin blocked work or duplicate an active attempt.

## 3. Load context

Call `get_work_context` before claiming. Start with the default compact context, then request only needed sections such as parent epic, relations, recent comments, decision content, attempt history, artifacts, project instructions, or changes since the previous attempt. Use `get_issue_activity` for a chronological audit trail.

Treat active decisions and acceptance criteria as durable constraints. If requirements are missing or contradictory, add a comment or record a decision instead of guessing.

## 4. Claim before execution

Call `claim_issue` only for a claimable `ready` or `review` issue. Keep the returned attempt ID and lease token private and available until the attempt ends. Effective `in_progress` is derived from this active lease; it is not a stored issue status.

For long work, call `renew_attempt` before expiry. A lost or expired lease must not be treated as ownership.

When other agents may work the same project concurrently, reserve the resources the attempt will edit: pass `resources` to `claim_issue` to acquire them atomically with the claim, or call `reserve_resources` later. Acquisition is all-or-nothing; a conflict fails the whole call with `RESOURCE_RESERVATION_CONFLICT` naming the reservation, its owning issue and attempt, and that lease's expiry. Diagnose a candidate set before claiming with `get_work_context`'s `desired_resources`. Reservations coordinate cooperating agents; they do not lock the filesystem against processes that bypass this server. They are released by `release_resources`, by `finish_attempt`, and by lease expiry, so a reserved resource is never permanently stuck.

## 5. Execute durably

- Use `save_attempt_note` for restartable checkpoints, important findings, warnings, and concrete next steps.
- Attach useful artifacts such as commits, branches, pull requests, files, URLs, and logs.
- Use comments for collaboration and decisions for durable architectural or product choices.
- Use `update_issue` and `archive_issue` with the current issue version. On a version conflict, refetch, reconcile, and retry; never overwrite concurrent changes blindly.
- Validate multi-issue plans before applying them atomically.

## 6. Finish every attempt

Call `finish_attempt` with the retained `project_ref` exactly once when work completes, fails, becomes blocked, or is handed off. Include a concise result, verification actually performed, artifacts, and actionable next steps.

- Completed implementation normally targets `review` or `done` according to project policy.
- Failed work records a failure reason and truthful details.
- Handoffs use `outcome: interrupted` with `interruption_reason_code: handoff`.
- Review attempts set `review_outcome`: `approved` marks the review request approved and the issue `done`; `changes_requested` returns the issue to `ready` and records follow-up work; `blocked` marks the issue `blocked`.
- If relevant changes happened after the claim, inspect them and acknowledge the issue version and latest event ID.

Never leave an attempt active merely because the agent is stopping.
