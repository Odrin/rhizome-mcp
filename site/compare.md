<!--
Editor rules for this page (keep them; they are part of the page contract):
1. Compare on guarantees under failure, never on feature nouns. Local-first,
   SQLite, MCP-native, issue graphs, and full-text search are table stakes
   shared by a dozen tools; they prove nothing.
2. Every cell is a falsifiable mechanism claim. "No documented equivalent"
   means: not present in that project's README/docs at the pinned version —
   it is a statement about their documentation, not their roadmap.
3. Every rhizome-mcp "yes" links to the spec section or the integration test
   that enforces it. Every competitor claim is checked against their docs at
   the pinned release listed under the matrix.
4. Never characterize a competitor's code quality, community, or drama.
   Mechanism statements only.
5. Re-verify the matrix on every rhizome-mcp minor release and update the
   "Last verified" line. A stale date is a bug; file an issue.
-->

# How rhizome-mcp compares

This page compares rhizome-mcp with other local task trackers built for AI
coding agents. It deliberately does not compare feature lists — issue graphs,
SQLite storage, and MCP support are common to most tools in this category.
Instead it asks what each tool **guarantees when things go wrong**, because
coding agents crash, lose context, and collide as a matter of routine. All
claims are pinned to specific versions and linked to each project's own
documentation; if anything here is stale or unfair,
[open an issue](https://github.com/Odrin/rhizome-mcp/issues).

## Five questions to ask any agent task tracker

**1. What happens when an agent dies mid-task?**
Agent processes exit, laptops sleep, API calls time out. If possession of a
task is stored as a status, a dead agent holds that task until a human
notices. The question is whether possession is temporary and self-releasing.

**2. Can two sessions claim the same task?**
Parallel sessions independently pick the "next obvious task". If claiming is
not atomic — and enforced below the application layer — both proceed, and the
collision surfaces later as conflicting edits.

**3. Can a review approve code that changed after the review was requested?**
If a review request points at "the task" rather than an exact version of it,
an approval can land on content the reviewer never saw. The question is
whether stale approval is structurally impossible or merely unlikely.

**4. What does a 100-issue board cost in context tokens?**
Every response the tracker returns is paid for out of the agent's context
window. The question is whether response sizes are bounded by a tested
contract or just happen to be small today.

**5. Can an interrupted attempt be resumed by a different session?**
Context limits end sessions mid-task. The question is whether the next
session resumes structured state — what was done, what remains, how to
verify — or re-derives everything from scratch.

## Guarantee matrix

| Guarantee | rhizome-mcp | beads | Kata | Guild | Backlog.md |
| --- | --- | --- | --- | --- | --- |
| Task frees itself after a crash | **Yes** — claims are renewable expiring leases; `in_progress` is [derived, never stored](https://github.com/Odrin/rhizome-mcp/blob/main/docs/02-domain-model.md#33-stored-status-semantics); exercised by [crash_restart_test.go](https://github.com/Odrin/rhizome-mcp/blob/main/integration/crash_restart_test.go) | No — claims do not expire | No — claims do not expire | No — claims do not expire | No claim mechanism |
| Double-claim prevented at the storage layer | **Yes** — [one active attempt per issue](https://github.com/Odrin/rhizome-mcp/blob/main/docs/02-domain-model.md#51-ownership) via a partial unique index; raced in [claim_race_test.go](https://github.com/Odrin/rhizome-mcp/blob/main/integration/claim_race_test.go) | Atomic claim | Atomic claim | Atomic claim | No — files merge in git |
| Stale review approval impossible | **Yes** — review requests pin issue version + event position and [supersede automatically on drift](https://github.com/Odrin/rhizome-mcp/blob/main/docs/09-review-workflow.md#staleness-and-concurrency) | No documented equivalent | No documented equivalent | No documented equivalent | Human review checkpoints (convention) |
| Response sizes bounded by a tested contract | **Yes** — [byte budgets per tool](https://github.com/Odrin/rhizome-mcp/blob/main/docs/03-mcp-tools.md#3-response-budgets-and-client-guidance) asserted by [response_budgets_test.go](https://github.com/Odrin/rhizome-mcp/blob/main/integration/response_budgets_test.go) (100 issues ≤ 64 KiB) | Compaction of old issues (different mechanism; no tested bound) | No documented equivalent | No documented equivalent | No documented equivalent |
| Interrupted attempt resumable by another session | **Yes** — [attempts](https://github.com/Odrin/rhizome-mcp/blob/main/docs/02-domain-model.md#5-workattempt) are first-class, with checkpoints and structured finish states | No documented equivalent | No documented equivalent | Handoff brief (free-form note) | Free-form notes in the task file |
| Mutex over non-issue resources (ports, migrations, deploy slots) | **Yes** — [leased resource reservations](https://github.com/Odrin/rhizome-mcp/blob/main/docs/12-resource-reservations.md), raced in [reservation_race_test.go](https://github.com/Odrin/rhizome-mcp/blob/main/integration/reservation_race_test.go) | No documented equivalent | No documented equivalent | Tracks files a quest touches (informational) | No documented equivalent |
| Quality gates evaluated server-side against submitted evidence | **Yes** — [workflow gates](https://github.com/Odrin/rhizome-mcp/blob/main/docs/09-review-workflow.md#relationship-to-workflow-gates) with per-attempt frozen requirement snapshots | No documented equivalent | Evidence attached to close (not policy-evaluated) | No documented equivalent | Acceptance criteria + DoD checklists (convention) |

**Last verified: 2026-08-28** against
[beads v1.2.2](https://github.com/gastownhall/beads/tree/v1.2.2),
[Kata v0.16.0](https://github.com/kenn-io/kata/tree/v0.16.0),
[Guild v0.3.2](https://github.com/mathomhaus/guild/tree/v0.3.2), and
[Backlog.md v1.50.1](https://github.com/MrLesk/Backlog.md/tree/v1.50.1),
using each project's README and documentation at that version.
"No documented equivalent" is a claim about documentation at that pin, not
about anyone's roadmap.

## rhizome-mcp vs beads

[beads](https://github.com/gastownhall/beads) is the most widely adopted
tracker in this category. It stores issues in a version-controlled database
(Dolt), which gives it something rhizome-mcp deliberately does not have:
issue state that syncs across machines and clones the way source code does.
It also has a large third-party ecosystem — viewers, TUIs, and editor
integrations — plus dependency-aware ready-work detection and semantic
compaction of old issues.

The mechanism difference: beads runs a background database service and
integrates with your repository through git hooks; rhizome-mcp is a single
process that puts exactly one JSON file in your repository, with the SQLite
database outside it. And beads' atomic claims are held until released —
there is no lease expiry, no derived `in_progress`, no version-pinned review
flow, and no server-evaluated gates.

**Choose beads if** you want issue state distributed across machines through
git and a rich ecosystem of companion tools. **Choose rhizome-mcp if** you
want single-machine multi-agent coordination with crash-safety guarantees
and nothing running in the background.

## rhizome-mcp vs Kata

[Kata](https://github.com/kenn-io/kata) is the closest technical relative:
a local-first Go binary with SQLite storage and native MCP over stdio and
HTTP. Its packaging and human UX are excellent — Homebrew/deb/rpm packages,
a self-update command, a TUI and a browser UI — and it documents a
backward-compatibility guarantee and a scale-out path (daemon mode,
Postgres) that rhizome-mcp does not attempt. Closing work can carry an
explicit completion claim with evidence and a commit SHA.

The mechanism difference is the agent-lifecycle layer: Kata's claims do not
expire, attempts are not modeled (so an interrupted session leaves no
structured resumable state), reviews are not version-pinned, and evidence
accompanies a close rather than being evaluated against a policy the server
enforces. rhizome-mcp has no TUI and no team-server story — by design.

**Choose Kata if** you want a polished tracker humans and agents share, with
first-class packaging and a path to a team deployment. **Choose rhizome-mcp
if** agents are the primary users and you need leases, resumable attempts,
and enforced review/gate integrity under concurrency.

## rhizome-mcp vs Guild

[Guild](https://github.com/mathomhaus/guild) is also a single Go binary with
embedded SQLite and a first-class MCP server. Its strengths are knowledge
retrieval and onboarding: hybrid BM25-plus-vector search over a typed
knowledge archive with staleness lifecycles, cross-project queries, and a
one-call session bootstrap that returns principles, the last handoff brief,
and the top claimable task together. rhizome-mcp's search is FTS5 only.

The mechanism difference: Guild's quests are claimed atomically but held
until cleared — if the claiming agent vanishes, the quest stays taken. Its
handoff brief is a free-form note rather than a resumable attempt with
structured finish states, and there is no review workflow, no gates, no
resource reservations, and no optimistic versioning on writes.

**Choose Guild if** semantic knowledge retrieval and a lightweight
claim-and-go flow matter most. **Choose rhizome-mcp if** lease expiry,
structured recovery, and review integrity matter most.

## rhizome-mcp vs Backlog.md

[Backlog.md](https://github.com/MrLesk/Backlog.md) takes the opposite
architectural bet: every task is a Markdown file committed to the
repository. That means zero infrastructure, tasks reviewable in pull
requests like any other file, a polished kanban TUI and web board, and a
disciplined human-review model (review the spec, the plan, then the code).
It is the strongest expression of "your tracker is just files in git".

The mechanism difference follows from the storage: files have no atomic
claim, no lease, and no storage-level concurrency control — two parallel
sessions editing task state produce git merge conflicts rather than one
claim succeeding and one failing fast. Attempt state is whatever prose a
session leaves in the file.

**Choose Backlog.md if** you run one agent session at a time and want tasks
to live in git as reviewable files. **Choose rhizome-mcp if** you run
concurrent sessions and need claims, leases, and recovery to be enforced
rather than conventional.

## Other tools

[Taskmaster](https://github.com/eyaltoledano/claude-task-master) solves a
different problem — decomposing a PRD into ordered tasks — rather than
coordinating concurrent agents over time.
[Foremerge](https://github.com/naw103/foremerge) is early but converging on
similar ideas (advisory claims, verification-gated acceptance); it is too
young to compare fairly against a pinned version.
[Vibe Kanban](https://github.com/BloopAI/vibe-kanban) has announced it is
sunsetting, and
[Shrimp Task Manager](https://github.com/cjo4m06/mcp-shrimp-task-manager)
has been inactive for over a year. Beyond these, at least a dozen projects
share the "local-first, SQLite, MCP built in" description — which is exactly
why this page compares guarantees instead.
