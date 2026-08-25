# Routing and cost rules

## Role resolution (harness-neutral)

"Orchestrator" and "executor" are roles, not agent names. Notes may say "Sonnet", "Haiku", or
"Rhizome Implementer" as shorthand; resolve them in whatever harness is running:

1. A subagent type named `Rhizome Implementer` exists (GitHub Copilot with this repo's
   `.github/agents/`): delegate executor briefs to it.
2. Otherwise, a generic subagent mechanism exists (e.g. Claude Code's Agent tool): spawn a
   general-purpose subagent on the cheapest capable model (Haiku-class), give it the brief as its
   entire task, and require the standard report (Summary / Files changed / Tests / Deviations).
3. Otherwise (no subagents at all): the orchestrator implements the brief itself, exactly as
   written, and records `Cost inefficiencies: no executor available` in the finish summary.

The routing rules below are about *who decides*, not which product runs the code: H means "fully
specified, needs no decisions", and stays H even when case 3 forces the orchestrator to type it in.

## Routes

| Route | Meaning | Use when |
|---|---|---|
| **H** | Executor-only brief (Haiku-class) | Write scope is bounded, contracts are settled, success is judged by stated acceptance criteria and a focused test. Typical: wire an existing helper into N call sites, add a table test, rename/move code, docs edits, YAML. |
| **S** | Orchestrator implements directly (Sonnet-class) | The edit needs an unresolved public-API, domain, storage, transaction, ordering, or security decision; or discovery and editing cannot be separated (migration over live data, choke-point refactor across many paths, race tests). Decision-only items (record_decision + doc wording) are also S. |
| **S→H** | Orchestrator decides and designs, executor implements | Most refactors: the orchestrator writes the signatures, templates, SQL, or schema and records decisions; the executor fills in bodies, call sites, and tests. Say in the note exactly which slice is which. |
| **M** | Maintainer action | Secrets, accounts, external publishing, sign-off on `review` items, committing work that already sits uncommitted in the tree. Items routed M get a short note saying exactly what the maintainer does and what it unblocks; they are listed in the map's Tail, never in a wave. |

Classify as S only when a concrete unresolved decision exists. If none exists, delegate. A second
failed executor brief on the same item escalates the remaining slice to S; do not write a third
brief.

## What every note must pin down

- **Pre-decide**: the decisions the orchestrator settles before briefing, with a recommended answer.
  These become `record_decision` entries, not brief text, so later sessions can find them.
- **Write set**: exact files (and new files) the executor may modify. Read set is implied by symbols
  named in the note.
- **Check**: one focused command (`go test ./pkg/ -run Name`); integration scoped with `-run` when
  the item touches a transport.
- **Finish**: `done` by default; `review` for items that add a migration or change a public tool /
  interchange / CLI contract so a human signs off.
- **Edges**: what this item unblocks and what must land first, so the tracker edges can be checked
  against the note.

## Cost rules encoded in the notes

- Symbols, not files: name functions/lines; include a template only where the executor would
  otherwise face a design choice.
- Scout before reading: for files over ~400 lines, the orchestrator sends a read-only scout brief
  ("list symbols and line ranges touching X, ≤40 lines") instead of reading the file.
- Test tiering: executor runs the focused check; orchestrator runs the package suite on acceptance;
  full unit suite once per item; integration per wave.
- Never rerun a green command unless code changed; never paste diffs or full logs into context
  (`git diff --stat` + in-scope hunks, `go test … | tail`).
- Parallel pairs only with disjoint write sets and independent checks; default is one item at a time.

## Priority as ordering

Among claimable items the orchestrator picks the highest priority, then the lowest issue number.
Assign priorities to produce the intended order:

- `critical`: the item(s) that unblock the most downstream work, or a correctness fix in a
  non-negotiable invariant. Several blocked criticals are fine: only claimable ones compete, and
  the sequence number breaks ties among equals.
- `high`: enabling refactors and specs that later features consume.
- `medium`: independent bugs and improvements.
- `low`: fillers (docs reconciliation, cleanups) that are correct to do whenever nothing else is
  claimable.

Edges express *must precede*; priority expresses *prefer first*. Do not use priority to fake a
dependency — add the edge.
