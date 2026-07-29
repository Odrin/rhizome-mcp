# Show HN: Rhizome MCP - a local-first task tracker built for coding agents

I built [rhizome-mcp](https://github.com/Odrin/rhizome-mcp), a local-first MCP server for coordinating multiple AI coding agents on one repository. It is a single Go binary backed by SQLite, with no accounts, Docker, or remote service.

The project came from a problem I kept running into: coding agents can write code and run tests, but their shared execution state is usually a `TODO.md`, a tracker designed for people, or one chat window. That works until two sessions choose the same task, an agent crashes while marked "in progress," or a context limit erases forty minutes of useful investigation.

rhizome-mcp treats agents as unreliable workers. Tasks are claimed through renewable leases, `in_progress` is derived rather than stored, and agents leave restartable checkpoints for whichever session picks up the work next.

This is not intended to replace GitHub Issues, Linear, or Jira for a team. It is a local execution layer for one developer running several agent sessions or products against the same codebase.

## Why ordinary assignment breaks down

Coding agents fail in three routine ways:

1. **They disappear.** The process exits, the laptop sleeps, or the API call times out.
2. **They lose context.** A long task eventually pushes its own starting state out of the context window.
3. **They collide.** Two sessions independently decide that the same issue is the next useful thing to do.

Most trackers model possession as durable state: assign a ticket to a worker and store `in_progress` until that worker changes it. That assumes the worker will come back to clean up. A dead agent will not.

The alternative is to make possession temporary and self-releasing.

## A claim creates a leased work attempt

An issue is never permanently assigned to an agent. A successful `claim_issue` call creates one **work attempt** and returns a lease:

```json
{
  "attempt": {
    "id": "01KY7M1XVRE9SJ5H9JTNRRG5XG",
    "issue_id": "01KY7M1QMCGG3TEJ4WRSH3QVQN",
    "status": "active",
    "lease_expires_at": "2026-07-23T14:08:47Z"
  },
  "lease_token": "OwM1IEE3YD8XUqBwKKl6_D_IJxi9KKGq9f10a0NfxtU"
}
```

The agent renews the lease while it works. Every mutation on the attempt requires the attempt ID and raw lease token; only a hash of the token is stored. If the heartbeats stop, the lease expires and a later claim can create a fresh attempt.

The database, rather than agent etiquette, enforces exclusivity:

```sql
CREATE UNIQUE INDEX idx_one_active_attempt_per_issue
ON work_attempts(issue_id)
WHERE status = 'active';
```

When two agents race to claim the same issue, one insert succeeds and the other loses at the constraint. Before a claim, the same write transaction materializes any lapsed active attempt as `expired`, so an old row does not hold the unique slot forever.

The lifecycle is deliberately small:

```text
claim -------------------------------> active
active + heartbeat ------------------> active
active + successful finish ----------> completed
active + failed finish --------------> failed
active + deliberate handoff ---------> interrupted
active + no heartbeat ---------------> expired
```

`expired` is the only terminal state reached without cooperation from the worker. That is the useful property.

## `in_progress` is a view, not a fact

The key design decision is that an issue cannot store `in_progress`.

Stored issue statuses are `open`, `ready`, `blocked`, `review`, `done`, and `cancelled`. Read operations compute an effective status from the issue and its lease:

```text
stored status: ready
    + no live attempt  -> effective status: ready
    + live attempt     -> effective status: in_progress
    + lease expires    -> effective status: ready
```

Expiry never rewrites the issue. It only ends possession. If the issue was otherwise ready and unblocked, it becomes claimable again automatically.

This removes an entire repair workflow. There is no stale `in_progress` value for a sweeper, admin, or future agent to notice and reset.

Expiry is materialized both lazily on paths that care about claimability and by a background sweep that records expired attempts in history. Correctness does not depend on the sweep running at exactly the right moment.

## Checkpoints recover the work, not just the slot

Releasing an abandoned task is only half of recovery. The next agent also needs the state accumulated by the previous one.

During an attempt, an agent can write progress notes, findings, warnings, and checkpoints. A checkpoint is a compact handoff:

```json
{
  "kind": "checkpoint",
  "content": "Repository layer is implemented and tested. Claim transaction is stubbed.",
  "next_steps": [
    "Implement the claim transaction with BEGIN IMMEDIATE",
    "Add the one-active-attempt concurrency test"
  ],
  "important": true
}
```

`get_work_context` assembles one bounded package with the issue, blockers, relevant decisions, the latest checkpoint, and the previous attempt's result and next steps. It prefers the latest restartable summary over replaying an unbounded transcript.

That supports crashes, but also deliberate handoffs. An agent approaching its context limit can finish as `interrupted`, record `context_limit` as the reason, and leave a precise starting point for another session.

A typical recovery looks like this:

```text
14:00  Agent A claims ISSUE-42
14:05  Agent A checkpoints its repository analysis
14:08  Agent A crashes and stops renewing
14:13  The lease expires
14:14  Agent B claims ISSUE-42 and receives the checkpoint
14:30  Agent B completes the issue
```

No one reassigns the issue, and no stale status has to be repaired.

## The rest of the concurrency model

Leases prevent duplicate active work, but they do not prevent every race.

Meaningful issue updates require an `expected_version`. If another agent changed the issue after it was read, the update fails with `VERSION_CONFLICT` instead of overwriting newer state.

Mutations can also carry an idempotency key. Repeating the same key and payload returns the original result; reusing the key for a different payload is rejected. This matters because agents retry after timeouts and ambiguous responses.

Review requests pin an exact issue version and event position. If the target changes, the request becomes stale rather than approving a moving target.

## Context size is part of the API contract

Agent tools have a constraint that ordinary application APIs can often ignore: every returned field competes for model context.

rhizome-mcp therefore uses compact projections and bounded responses:

- `list_issues` omits descriptions and acceptance criteria. In the project's fixture, 100 issues are about 46 KB compact versus 582 KB with full bodies; an integration test keeps the compact response under 64 KB.
- Graph queries exclude free-text bodies at the SQL layer and enforce a node limit.
- Search returns bounded snippets rather than documents.
- `get_changes` provides delta sync from a monotonic event ID.
- `get_work_context` returns the curated package an agent needs to start one issue.

The API makes the cheap operation the default. An agent can fetch a full body when it has chosen the one item that matters.

## Why SQLite

The target workload is one developer's local fleet of agents, not a distributed team across regions. It needs atomic, crash-safe writes much more than it needs a database service.

Claiming an issue creates the attempt, captures the issue version and event position, and appends an event in one transaction. SQLite gives the project a simple place to enforce those invariants:

```sql
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;
PRAGMA foreign_keys = ON;
PRAGMA synchronous = NORMAL;
```

WAL allows readers to continue during writes. Known-write operations use `BEGIN IMMEDIATE`, transactions stay short, and genuine `SQLITE_BUSY` or `SQLITE_LOCKED` failures retry the whole transaction with bounded backoff. The driver is pure Go (`modernc.org/sqlite`), so releases remain static, CGO-free binaries.

The repository contains only a small project pointer. The database lives in the platform application-data directory and is never committed.

## Scope and tradeoffs

rhizome-mcp currently exposes 32 MCP tools for issues, dependency graphs, comments, decisions, attempts, reviews, search, context assembly, and logical import/export. Stdio is the primary transport; an optional loopback-only HTTP transport is available for local clients.

The narrow scope is intentional:

- It has no accounts, permissions, hosted sync, or remote collaboration.
- SQLite permits one writer at a time, so write transactions must remain short.
- MCP clients differ in how reliably they follow a claim/checkpoint/finish workflow; the server can enforce invariants, but not force a client to call the right tool.
- A hosted tracker may still be the source of truth for product planning and human collaboration.

Compared with a `TODO.md`, this adds atomic claims and crash recovery. Compared with a hosted tracker wrapper, it adds leases and attempt history but gives up shared remote access. Compared with a memory or knowledge-graph server, it models execution rather than general facts. These systems can be complementary.

## Dogfooding it

The rhizome-mcp backlog itself lives in rhizome-mcp. Agents used it to implement the SQLite concurrency layer, review workflow, publishing pipelines, and VS Code extension.

That has exercised both clean handoffs and less flattering paths: context-limit interruptions, expired leases, concurrent claims, process crashes, stale review targets, and retries after ambiguous failures. The integration suite includes real multi-process claim races and crash/restart tests against one shared SQLite data root.

Dogfooding also exposed bugs that a design document did not: a bundled-binary resolution path that reported success instead of failing, validation errors naming the wrong field, and a board view trying to open a temporary file blocked by editor trust rules.

## Try it

For a zero-install trial:

```bash
npx rhizome-mcp serve
```

Or install the binary and connect a client:

```bash
curl -fsSL https://raw.githubusercontent.com/Odrin/rhizome-mcp/main/scripts/install.sh | sh
rhizome-mcp init
rhizome-mcp connect claude   # or: codex | vscode | json
```

There is also a [VS Code extension](https://marketplace.visualstudio.com/items?itemName=odrin.rhizome-mcp) that bundles the binary and registers the MCP server.

Source, documentation, and the full tool contract are at [github.com/Odrin/rhizome-mcp](https://github.com/Odrin/rhizome-mcp). The project is Apache-2.0.

I would especially value reports from people running multiple Copilot, Claude Code, Codex, or other MCP sessions against one repository. I am curious where clients fail to follow the workflow, which coordination failures the lease model misses, and whether the context responses are compact enough in larger real projects.