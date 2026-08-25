# 12. Resource reservations

Reservations let two agents work the same project at the same time without
editing the same content. An agent that holds an active work attempt can
reserve files, directories, globs, or logical resources; while those
reservations are active, no other attempt can reserve anything whose
resource language overlaps them.

This document is the workflow and guarantee contract. The identity,
normalization, and overlap rules live in [docs/02 §18](02-domain-model.md),
and the tool-by-tool input/output contract in
[docs/03 §11.6-11.9](03-mcp-tools.md).

## 12.1. What a reservation guarantees

- **Exclusivity between attempts.** Two active reservations whose normalized
  resource languages intersect cannot coexist. The loser of a race is told
  which reservation blocked it, which issue and attempt hold it, and when
  that lease expires.
- **All-or-nothing acquisition.** A `resources` set is acquired in one
  transaction. If any member conflicts, or the set overlaps itself, nothing
  is written -- neither the conflicting member nor its innocent neighbours.
  The same holds for `claim_issue` with `resources`: a conflicting claim
  leaves no attempt and no reservation behind.
- **One winner across processes.** Exclusivity is enforced by the shared
  SQLite database, not by in-process state, so it holds between separate
  server processes and separate transports. A racing loser sees the stable
  `RESOURCE_RESERVATION_CONFLICT` domain error, never a raw SQLite busy or
  locked failure.
- **No permanently stuck resource.** Every reservation is owned by a work
  attempt and dies with it: explicit release, attempt completion, failure,
  interruption, force release, issue cancellation, or lease expiry. A
  crashed agent's reservations are released when its lease is swept, and the
  resource becomes reservable again.

## 12.2. What a reservation does not guarantee

- **It does not lock the filesystem.** Reservations coordinate cooperating
  agents through this server. They place no lock on disk: a process that
  never calls the reservation tools -- a human editor, a formatter, a script,
  a second tool -- can still write a reserved file. Reserving a path is a
  claim on *who is supposed to be editing it*, not a write barrier.
- **It is not an authorization boundary.** Any client that can reach the
  server can reserve or release resources on an attempt it holds a lease
  token for. Access control belongs at the operating system boundary (see
  [SECURITY.md](../SECURITY.md)).
- **It says nothing about correctness of the path.** All matching is
  lexical. The server never stats a file, resolves a symlink, or consults
  git, so a reservation on a path that does not exist is perfectly valid and
  a symlinked alias of a reserved path is a different resource.
- **It is exclusive only.** There is no shared or read-only mode in v1
  ([docs/06](06-deferred-and-open.md)).
- **It does not change dependency state.** A conflict does not block an
  issue, add a blocker, or alter the planning graph; it fails one call.

## 12.3. Lifecycle

```text
acquire   claim_issue { resources: [...] }   atomic with the claim itself
          reserve_resources                  added to an attempt already held

inspect   list_resource_reservations         filtered by issue/attempt/kind/active
          get_resource_reservation           one reservation, full view adds the
                                             comparison key and version
          get_work_context                   active_reservation_count, and the
                                             optional resource_reservations and
                                             reservation_conflicts sections

release   release_resources                  explicit
          finish_attempt                     completed | failed | interrupted
          lease expiry                       expired
          rhizome-mcp maintenance release-attempt <id>   force_released
          issue cancellation/archival        blocked while an attempt is active
```

A released reservation is never reactivated. Reserving the same resource
again creates a new reservation row, so history stays append-only and the
board can show what an attempt held and why it lost it.

Reservations are held by **work** attempts only. A review attempt's lease
token is rejected ([docs/02 §18](02-domain-model.md)).

### 12.3.1. Board reporting

The `rhizome-mcp board` command reports active reservations grouped under
their owning attempts. The planning graph excludes finished work (done,
cancelled) from the node budget via `include_terminal=false`, ensuring the
entry-point count reflects claimable work only. When the 100-node graph
budget is exhausted, the board marks the graph as truncated and reports the
retained node count in both table and JSON formats. Truncation never shrinks
the entry-point set: truncated graphs still report all claimable issues as
entry points, even when those issues are not retained in `nodes` due to the
node budget limit.

## 12.4. Choosing a resource set

- Reserve the narrowest resource that covers the write set. A directory or
  a `**` glob is convenient and blocks a lot of other agents.
- Path comparison folds ASCII `A-Z` to `a-z` per segment, so
  `Internal/Config.go` and `internal/config.go` are the same resource. Every
  non-ASCII byte is compared exactly.
- Logical names are compared **exactly** -- `schema:migrations` and
  `schema:MIGRATIONS` are two different resources -- while the namespace is
  restricted to `[a-z][a-z0-9.-]{0,63}`. Pick one spelling per project and
  keep it.
- The glob grammar is deliberately small: whole-segment literals, a
  whole-segment `*`, and at most one trailing `**`. Embedded wildcards
  (`*.go`), `?`, character classes, and braces are validation errors, not
  patterns.
- One call may carry at most 50 resources; a path is at most 4096 runes.
- Use `get_work_context` with `desired_resources` to diagnose a candidate
  set *before* claiming. It reports conflicts without acquiring anything.

## 12.5. The two-agent scenario

The flow below is what the cross-process integration test
`integration/reservation_race_test.go` executes; it is the reference
sequence for conflict, expiry recovery, and reacquisition.

**1. Agent A claims and reserves atomically.**

```json
{ "name": "claim_issue", "arguments": {
  "issue_id": "ISSUE-42", "lease_seconds": 300,
  "resources": [{ "kind": "file", "path": "internal/reservation/expiry.go" }] } }
```

The response carries the attempt, the lease token, and the acquired
`reservations`. Agent A now owns both the issue and the file.

**2. Agent B is refused, atomically.**

Agent B -- a different process, possibly a different transport -- claims its
own issue and asks for the same file:

```json
{ "code": "RESOURCE_RESERVATION_CONFLICT",
  "message": "requested resources conflict with active reservations",
  "details": [{ "field": "resources.0", "code": "RESOURCE_RESERVATION_CONFLICT",
    "message": "requested file \"internal/reservation/expiry.go\" conflicts with active reservation 01J... (file \"internal/reservation/expiry.go\") held by issue=01A... attempt=01B..., lease expires ..." }] }
```

Nothing was written: agent B has no attempt on its issue and no reservation.
It can pick different work, narrow its resource set, or wait for the lease
expiry the conflict detail reports.

**3. Agent A dies.**

The process is killed without a clean shutdown. Its attempt row stays active
and its reservation stays held until the lease lapses -- deliberately, so a
briefly disconnected agent does not lose its work to a transient failure.

**4. The lease expires and the resource frees itself.**

Expiry is lazy plus swept: every `claim_issue` on that issue expires its own
stale attempts first, and the server also sweeps expired attempts
periodically. Either path releases agent A's reservations with
`release_reason: "expired"` and marks the attempt `expired`.

**5. Agent B reacquires.**

Agent B claims the same issue with the same `resources` and succeeds in one
call: a new attempt, a new lease token, a new active reservation on the same
file. Agent A's reservation remains in history, released as `expired`. No
resource is left permanently stuck, and no partial state was ever visible.

## 12.6. Recovery checklist

- **Conflict on claim.** Nothing was acquired. Read the conflict detail: it
  names the owning issue, attempt, and lease expiry.
- **Conflict on `reserve_resources`.** The attempt keeps everything it
  already held; the new set was not acquired, not even partially.
- **Lost lease.** An expired lease is not ownership. Reservations are gone;
  re-claim and re-reserve rather than continuing to write.
- **Stale reservation from a dead agent.** Wait for the lease to lapse, or
  force release the owning attempt with
  `rhizome-mcp maintenance release-attempt <attempt-id>`. Both release the
  reservation with an auditable reason (`expired`, `force_released`).
