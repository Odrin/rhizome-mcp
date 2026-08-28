# Issue lifecycle

## Project routing

Call `open_project` with the absolute repository root before using lifecycle tools. Retain its `project_ref` and pass it to every subsequent project-scoped call. Routing is stateless, so an earlier call does not establish an implicit current project; omission is valid only when intentionally using a configured default project.

## Types and hierarchy

Issues are `epic`, `task`, or `bug`. Use parent relationships for decomposition and `blocks` relations for execution order. `related_to` adds context without scheduling semantics; `duplicates` identifies equivalent work.

## Stored statuses

- `open`: known work that is not yet executable.
- `ready`: implementation work may be claimed when dependencies permit.
- `blocked`: explicitly paused; provide a useful blocked reason.
- `review`: completed work awaiting review; review attempts may claim it.
- `done`: accepted terminal work.
- `cancelled`: intentionally abandoned terminal work.

`in_progress` is an effective status derived from an active leased attempt. Never write it as an issue status. If a lease expires, the effective status falls back to the stored state so work cannot remain permanently stuck.

## Readiness and blockers

An issue is claimable only when its stored status permits the requested attempt and unresolved blockers do not prevent execution. Use `get_planning_graph` or `list_issues` with claimability filters instead of inferring readiness from titles or comments.

When adding a `blocks` relation, the source blocks the target. Keep dependency graphs acyclic and use the planning graph to confirm entry points.

## Mutations and concurrency

Issue updates and archival use optimistic concurrency:

1. Read the issue and retain its `version`.
2. Submit that value as `expected_version`.
3. If the version conflicts, refetch and reconcile all intervening changes.

Do not retry a stale patch blindly. Use bounded plan validation plus atomic plan application when creating several related issues, relations, and decisions together.

## Review and completion

Implementation attempts should record verification and artifacts before moving work to review or done. Moving an issue to `review` does not by itself ask anyone to review it: open a review request with `create_review_request`, freezing the issue version and event position the reviewer must verify. `claim_issue` then binds that open request to the review attempt automatically. Use `replace_review_request` only to supersede a request that is still `open`; once a request has resolved, the next one is a fresh `create_review_request`.

Review attempts finish with one explicit outcome and the server maps it to issue state: `approved` marks the review request approved and the issue `done`; `changes_requested` returns the issue to `ready` so follow-up work stays claimable; `blocked` marks the issue `blocked`.

Use comments for transient collaboration. Use decisions for durable choices that future agents must follow. Supersede decisions append-only rather than rewriting history.

## Archival

Archival hides obsolete records from normal lists without deleting history. Archive only with the current version and include archived records explicitly when searching or listing them.
