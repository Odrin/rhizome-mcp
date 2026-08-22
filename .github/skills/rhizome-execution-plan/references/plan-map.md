# Plan map (printed output)

The map is for the maintainer. It must be short enough to read in one screen per wave and must
not contain anything the orchestrator needs — that lives in the Execution notes. Print it in a
fenced markdown block.

## Template

```markdown
# <project> execution plan v<N> — <YYYY-MM-DD>
The tracker is the plan. This page is a map for the maintainer; the orchestrator never loads it.

## How a session runs (<orchestrator model>)
1. open_project → list_issues {status: ready, is_claimable: true} → take the highest priority (ties: lowest ISSUE number).
2. get_work_context (compact + recent comments) → read the "Execution notes" comment → claim_issue.
3. Follow the note's route (H = brief <executor model>; S = do it yourself; S→H = decide, record_decision, brief,
   then delegate now or save the brief as a checkpoint note and finish interrupted/handoff for the next session).
4. Review diff-stat + in-scope hunks, run the note's focused check, one scoped integration run, commit,
   finish_attempt (target per note). End the session.
Parallel pairs only where the note says so.

## Remaining work in the order the graph will yield it
Wave <letter> — <theme> (<claimable now | unlocks as … lands>)
  <ISSUE> <priority>  <short title> ............ <route>  [∥ <ISSUE>]  → unblocks <IDs> | ← <blocker IDs>
  …
Tail / housekeeping
  <epics to close or decompose, items in review awaiting the maintainer>
On hold: <IDs>.

## Release checkpoints
- After <items>: tag <version> — <what the release carries, breaking notes>.

## Guardrails
- Executor never authors migrations, transaction boundaries, or public schemas.
- Back up the project DB before any session that adds a migration; doctor --full after.
- A brief that needs a 3rd correction round → stop, re-scope, record the cost inefficiency.
- Full integration suite once per wave, not per item.
```

## Conventions

- ⚠ marks items that finish to `review` (migration / public contract).
- `∥` marks an approved parallel pair.
- Group by wave in dependency order; within a wave list in priority order — the same order
  `list_issues is_claimable:true` will return.
- If the plan is a refresh, start with a one-line "closed since last plan: …" summary.
