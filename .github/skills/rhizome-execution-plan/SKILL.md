---
name: rhizome-execution-plan
description: "Generate or refresh a tracker-backed execution plan for a rhizome-mcp project: read live issue state, order the remaining work, route each item to an orchestrator model or a cheaper executor model, and encode that order into the tracker (execution-note comments, blocks relations, priorities, ready statuses) so an orchestrator never has to hold a plan in context. Use this whenever someone asks for an execution plan, roadmap, sequencing, wave plan, 'what should the agents do next', or to re-plan after issues were closed, even if they do not say 'plan' explicitly."
argument-hint: "Optional: scope (epic IDs or 'all'), orchestrator/executor models, items to exclude or put on hold."
---

# Rhizome execution plan

The tracker is the plan. The deliverable is (1) tracker state that makes the next unit of work
discoverable with one `list_issues` call and (2) a short printed map for the maintainer. Never
produce a long document an orchestrator is expected to load: a long plan is exactly what overflows
an orchestrator's context window.

## Why this shape

An orchestrator (Sonnet-class) that carries a multi-issue plan re-reads it every turn and runs out
of context mid-wave. rhizome-mcp already has the primitives to hold the plan instead: `blocks`
relations gate work, `priority` orders claimable work, `is_claimable` derives from both, and
`get_work_context` returns an issue's comments. So the plan is encoded as data, and the
orchestrator's whole instruction collapses to "take the highest-priority claimable issue and follow
its Execution notes comment".

## Workflow

1. **Snapshot live state** (never plan from memory or from an older plan; issues close between
   runs). From the repository root run
   `python3 .github/skills/rhizome-execution-plan/scripts/plan_snapshot.py --notes`
   (requires the `rhizome-mcp` binary on PATH, built from the current checkout — a stale binary
   fails with "database schema is newer"; rebuild with `CGO_ENABLED=0 go build -o rhizome-mcp .`).
   It prints: previous plan dates and what closed since; every non-terminal issue grouped by epic
   with blockers and claimability; live `blocks` edges; the claimable pick order; items in
   `review`; items lacking an Execution notes comment; and uncommitted working-tree changes
   (uncommitted work often belongs to an open issue and changes its route — e.g. to M).
   The `--notes` dump shows each existing note's header and a hit-windowed excerpt; when the
   body matters (re-planning), read it in full with the MCP `get_work_context` tool (recent
   comments) — the read-only CLI cannot return whole comments.
2. **Scope.** Honor exclusions the maintainer named (on-hold epics stay out; mark their open
   children `blocked` with a reason that says "on hold"). Epics are containers: plan their
   children, close or decompose the epic at the end. Items in `review` are maintainer sign-off
   (route M): the orchestrator's loop filters `status: ready`, so they never enter the pick order;
   give them a short M note and list them in the map's Tail section.
3. **Classify each remaining item** using `references/routing.md`: route (H / S / S→H / M), the
   pre-decisions the orchestrator must settle before briefing, exact write set, focused check
   command, and finish target (`done`, or `review` for migrations and public contract changes).
   Read the owning code only as far as needed to name symbols; the note must cite real files.
4. **Order.** Respect existing `blocks` edges. Add an edge for every technical dependency the
   notes reveal (a refactor that creates the seam a later feature needs, a conformance harness
   new tools must pass, a spec that must precede its implementation). Keep the graph acyclic.
   Then assign priorities so that, among claimable items, priority order equals the intended
   order: `critical` for the item that unblocks the most, `high` for enabling work, `medium` for
   independent fixes, `low` for fillers (docs, cleanups) the orchestrator picks only when nothing
   else is claimable. Priority only matters among *claimable* items — a blocked `critical` issue
   is harmless because its edges gate it, so there is no need to demote and re-promote.
5. **Encode into the tracker** over MCP (`add_comment`, `manage_issue_relation`, `update_issue`):
   one "Execution notes" comment per item using `references/execution-note.md`; the new edges;
   priorities; and `status: ready` for every planned task — including currently blocked ones,
   because `is_claimable` already accounts for unresolved blockers and a `ready`+blocked issue
   becomes claimable the moment its blockers close with no manual flip. Use idempotency keys
   (`exec-notes-<date>-<ISSUE>`, `plan-edge-<src>-<dst>`) so a rerun is safe.
6. **Verify.** Re-run the snapshot: the claimable pick order must read as the plan's first wave,
   and every planned item must show a note. If the board's planning graph shows zero entry
   points on a mature project, that is the graph node cap filling with terminal issues, not an
   empty queue — `list_issues is_claimable:true` is the authoritative view.
7. **Print the map** using `references/plan-map.md` — one line per item, grouped by wave, with
   route and blockers, plus release checkpoints and guardrails. Keep it under ~80 lines; the
   detail lives in the notes.

## Routing and cost rules (summary; details in `references/routing.md`)

- Executor (Haiku-class) gets fully specified briefs: exact files, symbols, acceptance criteria,
  commands. It never authors migrations, transaction boundaries, or public schema decisions.
- Orchestrator (Sonnet-class) does the slice that requires a decision, then hands the mechanical
  remainder down (S→H). Decision-only items (record a decision, amend a doc) are S.
- Pair items for parallel execution only when write sets are disjoint and checks are independent;
  say so explicitly in both notes.
- Migrations are serialized through the orchestrator and numbered in plan order; every
  data-rewriting migration needs a populated-database test.
- Each note names one focused check; the full integration suite runs once per wave, not per item.

## Re-planning

When asked to refresh: snapshot, drop terminal items, keep existing notes that are still accurate
(append a dated correction comment instead of rewriting history), add notes for new issues, and
re-rank. Report what closed since the previous plan before printing the new map.
