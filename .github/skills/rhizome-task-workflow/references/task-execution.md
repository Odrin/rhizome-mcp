# Task execution

## Orient and select

Call `get_project` once per session. Honor project instructions, supported values, limits, and the returned latest event ID.

Use `get_planning_graph` for dependency-aware selection. Entry points are executable roots; blocking nodes explain stalled work. For a narrow queue, call `list_issues` with `is_claimable: true`. Use `search` for historical knowledge, not as authoritative current state. Follow cursors when `has_more` is true.

Select one coherent issue. Do not begin blocked work or duplicate an active attempt.

## Load context

Call `get_work_context` before claiming. Start with its compact default and request only relevant optional sections:

- parent epic and relations for scope;
- decision content and project instructions for constraints;
- recent comments and attempt notes for collaboration;
- attempt history, artifacts, and changes since the previous attempt for recovery.

Use `get_issue_activity` when a chronological audit trail is necessary. If acceptance criteria or constraints conflict, add a comment or record a durable decision rather than guessing.

## Claim and execute

Call `claim_issue` only for claimable `ready` or `review` work. Retain the attempt ID and lease token until the attempt ends; never expose or hand off the token. For long work, call `renew_attempt` before expiry.

During execution:

- save `checkpoint` notes at restartable boundaries;
- use `finding` or `warning` notes for important discoveries;
- include concrete `next_steps`;
- attach commits, branches, pull requests, files, URLs, and verification logs as artifacts;
- use comments for collaboration and decisions for durable architectural or product choices;
- update or archive issues with their current `version`; refetch and reconcile on conflicts;
- validate a multi-issue plan before applying it atomically.

## Finish

Call `finish_attempt` exactly once when work completes, fails, blocks, or is handed off. Include a concise result, checks actually run, artifacts, and actionable next steps.

- Successful implementation normally targets `review` or `done` according to project policy.
- Failed work includes a stable failure reason and truthful details.
- A handoff uses `outcome: interrupted` and `interruption_reason_code: handoff`.
- Review work includes `approved`, `changes_requested`, or `blocked`.

Inspect relevant concurrent changes before finishing and acknowledge the observed issue version and latest event ID when required. Never leave an attempt active merely because the session is ending.
