# Task execution

## Orient and select

Call `open_project` with the absolute repository root. Retain the returned `project_ref` and pass it to every subsequent project-scoped tool call. Routing is stateless: `open_project` does not select a project for later requests. Omit `project_ref` only when intentionally relying on a server configured with a default project.

Call `get_project` with that `project_ref` when project instructions are needed. Honor project instructions, supported values, limits, and the latest event ID returned by `open_project` or `get_project`.

Use `get_planning_graph` with the retained `project_ref` for dependency-aware selection. Entry points are executable roots; blocking nodes explain stalled work. For a narrow queue, call `list_issues` with `project_ref` and `is_claimable: true`. Use `search` for historical knowledge, not as authoritative current state. Follow cursors when `has_more` is true.

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

Call `finish_attempt` with the retained `project_ref` exactly once when work completes, fails, blocks, or is handed off. Include a concise result, checks actually run, artifacts, and actionable next steps.

- Successful implementation normally targets `review` or `done` according to project policy.
- Failed work includes a stable failure reason and truthful details.
- A handoff uses `outcome: interrupted` and `interruption_reason_code: handoff`.
- Review work includes `approved`, `changes_requested`, or `blocked`.

Inspect relevant concurrent changes before finishing and acknowledge the observed issue version and latest event ID when required. Never leave an attempt active merely because the session is ending.
