# Execution notes comment

Post exactly one comment per planned issue with `add_comment` and the idempotency key
`exec-notes-<YYYY-MM-DD>-<ISSUE-N>`. The orchestrator reads it from `get_work_context` (recent
comments) and follows it instead of re-deriving the plan. Keep it under ~15 lines; the issue
description and acceptance criteria already carry the problem statement.

## Template

```markdown
## Execution notes (plan <YYYY-MM-DD>, for the <orchestrator model> orchestrator)
Route: <H | S | S→H | M>. <One or two sentences: which slice the orchestrator does, which the executor does.>
Pre-decide: <decision 1 with recommended answer; decision 2 …> | none
Write set: <exact files; mark new files (new)>
Check: <one focused command>; <optional scoped integration run>
Finish: <done | review (reason)>. <Unblocks ISSUE-N …; runs in parallel with ISSUE-M (disjoint write sets) | sequential.>
```

## Example

```markdown
## Execution notes (plan 2026-08-22, for the Sonnet orchestrator)
Route: S→H. Sonnet authors the pure signatures + table tests in internal/domain/attempt_policy.go (EvaluateClaim, FinishTargetStatus, NextClosedAt, ClassifyIssueChanges, BlocksPathExists over an adjacency callback); Haiku rewires sqlite ClaimIssue / FinishAttempt / completionIssueChanges / planPathExists and adds the SQL-vs-domain agreement test for issueClaimableSQLAt.
Pre-decide: none beyond the signatures (all rules already exist in code; this is extraction, not redesign).
Write set: internal/domain/attempt_policy.go (+_test), internal/adapters/sqlite/{attempts,planning,relations}.go, internal/domain/logical_project_import.go, sqlite/issue_list_test.go.
Check: go test ./internal/domain/ ./internal/adapters/sqlite/
Finish: done. Unblocks ISSUE-201 and ISSUE-172 (gate hook point).
```

## Corrections

Comments are append-only. When a note becomes inaccurate (a prerequisite landed differently, a
file moved), post a new comment titled `## Execution notes — correction (<date>)` with only the
changed lines. Do not re-post the full note.
