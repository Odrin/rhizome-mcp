# MCP tool catalog

## 1. Protocol conventions

MCP messages use JSON-RPC 2.0.

Tool inputs and outputs are JSON objects validated by JSON Schema.

Tools return:

- `structuredContent` as the authoritative result;
- an optional short text summary;
- short `next_actions` on workflow-sensitive results;
- no full duplication of large JSON results in text.

The initialize response contains compact baseline workflow instructions. Full
guidance is available through these static Markdown resources:

- `rhizome://guides/agent-workflow`;
- `rhizome://guides/issue-lifecycle`;
- `rhizome://guides/multi-agent-handoff`.

All IDs accepted as `issue_id` may be either:

- internal ULID;
- display ID such as `ISSUE-42`.

Other entity IDs use internal ULIDs only.

## 2. Common response conventions

Potentially large results include:

```text
has_more
next_cursor
truncated
truncation_reason
```

Collections use cursor-based pagination.

Default collection limit: `20`.

Maximum ordinary collection limit: `100`.

Errors use:

```json
{
  "code": "ISSUE_BLOCKED",
  "message": "Issue cannot be claimed while blockers are unresolved.",
  "details": {},
  "retryable": false
}
```

### 2.1. Explicit agent-session attribution

Durable attribution is opt-in and transport-neutral. It never derives from an
SDK `ServerSession`, `Mcp-Session-Id`, HTTP connection, or `initialize` request.
All tool input schemas except `create_agent_session` and `end_agent_session`
include this optional property:

```json
{
  "agent_session_handle": null
}
```

When present, `agent_session_handle` is a non-empty opaque bearer string with a
maximum of 256 ASCII characters. It is not an entity ID, is returned only by
`create_agent_session`, and must not appear in tool output, events, errors, or
logs. Supplying `null` is equivalent to omitting it.

The adapter resolves a supplied handle through the application service before a
mutating operation opens its business transaction. It passes the resolved
durable `session_id` to the existing domain command; domain and SQLite layers
continue to store only that nullable ULID in attempts and audit records. A
read-only tool may validate a supplied handle but never updates
`last_seen_at`. A mutating tool atomically validates the active session, writes
its ordinary domain changes and audit records with the resolved `session_id`,
and advances `last_seen_at`; an error leaves all of these writes unchanged.

Handle errors are stable, non-retryable structured errors:

| Condition | Error code | Detail |
| --- | --- | --- |
| malformed or overlong handle | `INVALID_ARGUMENT` | `field: agent_session_handle`, `code: INVALID_HANDLE` |
| well-formed but unknown handle | `SESSION_NOT_FOUND` | `field: agent_session_handle`, `code: NOT_FOUND` |
| ended handle | `SESSION_NOT_ACTIVE` | `field: agent_session_handle`, `code: ENDED` |

Clients that omit the property remain fully functional and their resulting
attempts, entities, and events have NULL session attribution. A handle provides
audit attribution only; it neither authenticates the caller nor replaces an
attempt's `lease_token`.

### 2.2. Stateless project routing

`open_project` is the only projectless tool. Call it with an absolute
`project_root`, retain the returned canonical `project_ref`, and pass that value
to every subsequent project-scoped tool call. Calling `open_project` does not
select a project in MCP transport or session state.

Every other tool input schema includes an optional nullable `project_ref`.
Omission is supported only when the server was started with a configured default
project. Portable agent workflows pass the reference explicitly, including on
`get_project`, attempt lifecycle calls, and `finish_attempt`. A `project_ref` is
a routing token, not authentication or authorization.

For readability, later input snippets focus on tool-specific fields. Unless the
snippet is for `open_project`, add the retained value:

```json
{
  "project_ref": "01ARZ3NDEKTSV4RRFFQ69G5FAV"
}
```

## 3. Response budgets and client guidance

The default MCP responses in this catalog are intentionally bounded projections. The acceptance gate in the integration suite serializes the actual `structuredContent` payload and asserts the byte budgets below so clients can rely on the defaults staying compact even when issue bodies and other free-text fields are very large.

The following defaults are covered by the integration gate and should be treated as the documented baselines for normal client use:

| Tool | Default fields or delivery | Budget |
| --- | --- | ---: |
| `get_issue` | standard: `id`, `display_id`, `sequence_no`, `type`, `title`, `status`, `priority`, `parent_issue_id`, `blocked_reason`, `version`, timestamps, `labels`; no bodies | 32 KiB |
| `get_issue_graph` | `root_issue_id`, bounded `nodes`, `edges`, `summary`, `entry_points`, truncation fields | 32 KiB for the deterministic fixture used in the integration test |
| `get_planning_graph` | bounded `nodes`, `edges`, `entry_points`, `blocking_nodes`, `summary`, `warnings`, `truncated` | 32 KiB for the deterministic fixture used in the integration test |
| `manage_issue_relation` | `changed`, relation fields, `affected_issues`, `latest_event_id` | 32 KiB |
| `create_issue` | compact: `id`, `display_id`, `sequence_no`, `type`, `status`, `priority`, `version` | 32 KiB |
| `update_issue` | compact `issue` (`id`, `display_id`, `status`, `version`) and `changed_fields` | 32 KiB |
| `archive_issue` | compact: `id`, `display_id`, `status`, `version` | 32 KiB |
| `claim_issue` | compact `issue`, compact `attempt`, `lease_token` | 32 KiB |
| `renew_attempt` | `lease_expires_at`, `server_time`, `next_actions` | 32 KiB |
| `save_attempt_note` | `attempt_note`, `artifacts`, `next_actions` | 64 KiB |
| `finish_attempt` | compact `attempt`, compact `issue`, `warnings`, `latest_event_id`, compact `artifacts`, `next_actions` | 128 KiB |
| `get_work_context` | `issue`, `blockers`, `decisions`, `reviews`, attempt/checkpoint summaries, optional-section placeholders, warnings and truncation fields | 256 KiB |
| `get_issue_activity` | `items`, `next_cursor`, `has_more` | 32 KiB |
| `get_changes` | `events`, `latest_event_id`, `has_more`, `next_event_id` | 128 KiB |
| `validate_issue_plan` | `valid`, `errors`, `warnings`, `plan_fingerprint`, `normalization_changed`; no `normalized_plan` | 32 KiB |
| `export_project` | artifact: `format`, `version`, `exported_at`, `byte_count`, `sha256`, `artifact_uri` | 32 KiB |

Existing list/graph budgets remain authoritative for the larger maximum-node cases: `list_issues` stays within 64 KiB for 100 compact items, and graph results stay within 96 KiB for 100-node graphs. Those are maximum-node proofs and should not be treated as the default response size for ordinary calls.

Explicit opt-in modes are intentionally larger and are not part of the default gate:

- `get_issue` with `view: "full"` includes the full free-text bodies and full label metadata; it is larger than the standard default when bodies are present.
- `validate_issue_plan` with `include_normalized_plan: true` includes the normalized plan and is larger than the compact default.
- `export_project` with `delivery: "inline"` returns the full logical project document and is not used in the default budget gate; it can exceed the bounded artifact acknowledgement and is only appropriate when the caller needs the document itself.
- Optional sections in `get_work_context` are opt-in and bounded by their own per-section limits; the default response intentionally includes the primary issue's full description and acceptance criteria, which is why its budget is larger than the other compact defaults.

Client guidance:

- Call `open_project` once to get a canonical `project_ref`, then reuse that reference for subsequent project-scoped calls.
- Use `get_project` only for metadata or instruction refresh; avoid treating it as a full state snapshot or a substitute for targeted reads.
- Filter and list first (`list_issues`, `get_planning_graph`, `get_issue_graph`) before requesting detail calls for specific issues.
- Request explicit `full`, normalized-plan, or `inline` modes only when the caller genuinely needs the larger payload; the default responses are designed to stay compact and predictable.

Audited baselines from prior review work are informative but are not current default-response guarantees: a 20-node planning graph was observed at approximately 26 KiB, and a pre-ISSUE-154 inline export was observed at approximately 981 KiB.

## 3.1. Tool inventory

The catalog exposes 35 full tools, 31 agent tools, 5 migration tools, and 16 read-only tools:

1. `create_agent_session`
2. `end_agent_session`
3. `open_project`
4. `get_project`
5. `export_project`
6. `validate_import`
7. `apply_import`
8. `list_labels`
9. `create_issue`
10. `update_issue`
11. `get_issue`
12. `list_issues`
13. `archive_issue`
14. `create_review_request` (deprecated — see section 7.6)
15. `get_review_request`
16. `list_review_requests`
17. `cancel_review_request`
18. `supersede_review_request` (deprecated — see section 7.6)
19. `replace_review_request`
20. `manage_issue_relation`
21. `get_issue_graph`
22. `get_planning_graph`
23. `validate_issue_plan`
24. `apply_issue_plan`
25. `add_comment`
26. `record_decision`
27. `list_decisions`
28. `get_issue_activity`
29. `claim_issue`
30. `renew_attempt`
31. `save_attempt_note`
32. `finish_attempt`
33. `get_work_context`
34. `search`
35. `get_changes`

### 3.1. `create_agent_session`

Creates a durable attribution session independently of MCP transport lifecycle.

Input:

```json
{
  "client_name": "GitHub Copilot",
  "client_version": "1.2.3",
  "agent_label": null,
  "model": null,
  "instance_key": null
}
```

`client_name` is required and the remaining metadata fields are optional,
non-blank strings of at most 256 runes. The tool is mutating,
non-idempotent, and returns `session` metadata plus
`agent_session_handle`. The handle is shown only in this response and must be
retained by the client; it cannot be recovered later.

### 3.2. `end_agent_session`

Ends one explicitly created session.

Input:

```json
{
  "agent_session_handle": "opaque bearer handle"
}
```

The tool validates the handle and sets `ended_at` and `last_seen_at` in one
short write transaction. Repeating it with the same still-recognized handle is
successful and returns the original ended session unchanged; it does not update
timestamps. Unknown and malformed handles use the errors in section 2.1.
Ending a session does not terminate active attempts or invalidate their lease
tokens; later attempt operations can omit attribution or use another active
handle.

### 3.3. Required implementation boundary

The implementation of this contract is bounded as follows:

- **Domain:** retain `AgentSession.ID` as the internal ULID, add only
  handle-resolution inputs and stable handle validation details; preserve the
  nullable `session_id` fields on existing mutation commands.
- **Application:** generate a cryptographically random handle, hash it before
  persistence, create, resolve, touch, and end sessions through one service;
  resolve-and-touch for a mutating call must run in the same SQLite write
  transaction as its business mutation.
- **MCP adapter:** add the two lifecycle tools and the optional common input
  property to every existing tool schema; resolve the property before passing
  command inputs. Remove `InitializedHandler`, `ServerSession.ID()`,
  `Mcp-Session-Id`, `connectionSessions`, `sdkSessionIDs`, and HTTP/stdio close
  lifecycle attribution.
- **SQLite:** migrate `agent_sessions` with a non-null unique `handle_hash`,
  use indexed hash lookup, and provide atomic active-handle resolution plus
  touch for existing mutation transactions. Existing nullable attribution
  foreign keys and historical rows remain unchanged.
- **Tests:** cover creation, single-return handle redaction, omitted handles,
  malformed/unknown/ended errors, idempotent end, read-only no-touch, atomic
  mutation attribution, concurrent use/end races, reconnect and process-restart
  recovery, and both MCP protocol eras over stdio and HTTP.
- **Documentation:** update examples and operational guidance after the runtime
  implementation lands; do not describe HTTP `DELETE` or SDK session closure as
  durable session termination.

---

## 4. Tool annotations

Every tool returned by `tools/list` carries an explicit
[MCP tool annotation](https://modelcontextprotocol.io/specification) set:
`readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`. These are
assigned in code through one required `toolHints(...)` argument on every
registration call in `internal/adapters/mcp/adapter.go`, so a newly added tool
that omits this argument fails to compile — the classification cannot be
silently skipped.

**Annotations are advisory client guidance, not an authorization boundary.**
A client may use them to decide whether to warn a user or skip a confirmation
prompt, but the server always re-validates every request server-side
regardless of what a client inferred from these hints. Nothing here weakens
or replaces domain-level validation, optimistic concurrency, or lease checks.

**`readOnlyHint: true` means the invocation performs no durable write at
all, including transport-level bookkeeping.** Every tool call also carries
MCP session lifecycle tracking (`agent_sessions.last_seen_at`), which is
itself a durable SQLite write. Rather than special-case that write per
tool, `internal/adapters/mcp/adapter.go`'s registration wrapper
(`touchSessionForMutatingTool`) derives the decision from the same
`readOnlyHint` used everywhere else in this section: a tool registered as
read-only never touches `last_seen_at` as a side effect of being called; a
mutating tool still touches it on every call, so session activity tracking
stays correlated with actual writes rather than with every call including
reads. This is enforced structurally for every tool, not only for `get_project` —
see `TestReadOnlyToolsDoNotDurablyTouchAgentSession` in the adapter test
suite and its stdio/HTTP counterparts,
`TestIntegrationStdioReadOnlyToolsDoNotTouchAgentSession` and
`TestIntegrationHTTPReadOnlyToolsDoNotTouchAgentSession`, in `integration/`.

`openWorldHint` is `false` for every tool: Rhizome only reads and writes its
own local SQLite project database. No tool fetches a URL or otherwise reaches
into an external system on the server's behalf (artifact `uri` values are
stored as opaque strings, never dereferenced).

`idempotentHint` is `true` only where repeating the exact same call arguments
is *guaranteed* to produce no additional effect beyond the first call — not
merely because a tool happens to accept an optional `idempotency_key`. Two
independent patterns earn a `true` here:

- **Mandatory-key replay** — `apply_issue_plan` and `replace_review_request`
  require `idempotency_key` on every call (it is a required schema field,
  not optional) and the repository replays the original result for a
  repeated key. Same arguments necessarily means the same key, so the
  guarantee holds unconditionally.
- **Fail-safe-on-retry gating** — a mutation guarded by a precondition that
  the first successful call itself invalidates: optimistic-concurrency
  `expected_version` (`update_issue`, `archive_issue`,
  `cancel_review_request`, `supersede_review_request`,
  `replace_review_request`'s predecessor), a claimability check
  (`claim_issue`), an active-lease check (`finish_attempt`), or a storage
  constraint (`manage_issue_relation`'s unique `(source, target, type)` index
  on add, not-found on remove; `apply_import`'s empty-destination
  requirement). After the first call, the precondition no longer holds, so a
  bare repeat with identical arguments fails without any further write —
  analogous to the MCP specification's own `delete_file` example.
  `replace_review_request` satisfies both patterns at once: the mandatory
  key gives replay, and the predecessor's `expected_version` additionally
  fails safe if a caller races without noticing the key collision.

Tools that only ever append or insert with no such gate and no mandatory key
(`create_issue`, `create_review_request`, `add_comment`, `record_decision`,
`save_attempt_note`, `renew_attempt`) are `idempotentHint: false`: a bare
repeat creates a second issue, comment, decision, or note, or (for
`renew_attempt`) pushes the lease expiry further out again. An optional
`idempotency_key` on these tools changes behavior only when the caller
actually supplies one — it is not part of the unconditional invocation
contract, so it does not change the hint.

`destructiveHint` follows the guidance's own examples — overwrite, archive,
cancel, supersede, bulk-apply, or otherwise destroying prior effective state —
rather than the tool's read/write split alone:

- `update_issue` can overwrite title, description, status, and
  `blocked_reason`.
- `archive_issue`, `cancel_review_request`, and `supersede_review_request`
  each end the prior lifecycle state of their target.
- `manage_issue_relation` can remove an existing relation (`action: "remove"`).
- `apply_import` and `apply_issue_plan` are bulk-apply operations with a wide
  blast radius even though individual writes are additive.
- `finish_attempt` can transition an issue's status (including to `blocked`,
  overwriting a prior `blocked_reason`) as part of ending the lease.
- `record_decision` can flip an existing decision's `status` to `superseded`
  in the same transaction when `supersedes_id` is supplied.
- `create_review_request` is **not** destructive: it only records a
  `supersedes_id` link and never closes the predecessor itself — that split
  responsibility is exactly what `replace_review_request` replaces (see
  section 7.6 for the deprecation policy).
- `replace_review_request` is destructive: a successful call ends the
  predecessor's lifecycle (superseded) as part of creating its successor,
  in the same transaction.

### 4.1. Annotation matrix

| Tool | readOnly | destructive | idempotent | openWorld |
| --- | --- | --- | --- | --- |
| `open_project` | ✓ | | ✓ | |
| `get_project` | ✓ | | ✓ | |
| `export_project` | | | ✓ | |
| `validate_import` | ✓ | | ✓ | |
| `apply_import` | | ✓ | ✓ | |
| `list_labels` | ✓ | | ✓ | |
| `create_issue` | | | | |
| `update_issue` | | ✓ | ✓ | |
| `get_issue` | ✓ | | ✓ | |
| `list_issues` | ✓ | | ✓ | |
| `archive_issue` | | ✓ | ✓ | |
| `create_review_request` | | | | |
| `get_review_request` | ✓ | | ✓ | |
| `list_review_requests` | ✓ | | ✓ | |
| `cancel_review_request` | | ✓ | ✓ | |
| `supersede_review_request` | | ✓ | ✓ | |
| `replace_review_request` | | ✓ | ✓ | |
| `manage_issue_relation` | | ✓ | ✓ | |
| `get_issue_graph` | ✓ | | ✓ | |
| `get_planning_graph` | ✓ | | ✓ | |
| `validate_issue_plan` | ✓ | | ✓ | |
| `apply_issue_plan` | | ✓ | ✓ | |
| `add_comment` | | | | |
| `record_decision` | | ✓ | | |
| `list_decisions` | ✓ | | ✓ | |
| `get_issue_activity` | ✓ | | ✓ | |
| `claim_issue` | | | ✓ | |
| `renew_attempt` | | | | |
| `save_attempt_note` | | | | |
| `finish_attempt` | | ✓ | ✓ | |
| `get_work_context` | ✓ | | ✓ | |
| `search` | ✓ | | ✓ | |
| `get_changes` | ✓ | | ✓ | |

A blank cell means the hint is `false`. `openWorldHint` is `false` for every
tool, per the local-first rationale above.

---

## 5. Tool exposure profiles

A server instance can advertise a reduced tool catalog by selecting a
**tool profile** at startup — an exposure and prompt-size control for
narrowing what a given deployment shows a client. It is not an
authorization boundary: every tool that is advertised still enforces its
own domain-level validation exactly as if every tool were advertised.
Nothing about profile filtering weakens optimistic concurrency, lease
checks, or any other server-side rule.

### 5.1. Selecting a profile

Configure the profile before starting the server, through either path:

```bash
rhizome-mcp serve --profile agent
rhizome-mcp serve --http-address 127.0.0.1:0 --profile read-only
```

```bash
TOOL_PROFILE=migration rhizome-mcp serve
```

The `--profile` CLI flag takes precedence over the `TOOL_PROFILE`
environment variable, matching the existing `--http-address` /
`HTTP_ADDRESS` precedence. Leaving both unset keeps the default, `full`,
so an unconfigured server is unchanged from every prior release: the
complete existing tool catalog remains backward compatible.

An unrecognized profile name fails server startup immediately, before any
transport opens, with a structured error naming the invalid value and
every valid name:

```json
{
  "code": "INVALID_ARGUMENT",
  "message": "unsupported tool profile \"read-write\" (valid profiles: full, agent, read-only, migration)",
  "details": [{"field": "tool_profile", "code": "INVALID_ENUM"}],
  "retryable": false
}
```

`get_project`'s `tool_profile` field always reports the profile actually
in effect, so a client that expects a tool and doesn't see it in
`tools/list` can immediately tell whether a profile — not a bug — is the
reason.

### 5.2. Profile membership matrix

Every registered tool declares exactly one capability group in
`internal/adapters/mcp/adapter.go` (`registerTool(server, group, ...)`);
this is a required argument, so a newly added tool cannot be registered
without an explicit group decision. `full`, `agent`, and `migration` are
defined as which groups they include. `read-only` is defined differently
and deliberately: `toolProfileIncludes` checks `readOnlyHint` *before* any
group-based rule, including the `core` group's own "always advertised"
bypass — so a hypothetical future mutating core tool could never enter
the read-only profile just by being core. `read-only` membership is
derived directly from each tool's own `readOnlyHint` (section 4.1), not
from a separately maintained list, so it can never drift from the
annotation matrix.

| Group | Tools | In `agent`? | In `migration`? |
| --- | --- | --- | --- |
| core | `open_project`, `get_project` | always | always |
| migration | `export_project`, `validate_import`, `apply_import` | no | yes |
| sync | `get_changes` | no | no |
| issues | `list_labels`, `create_issue`, `update_issue`, `get_issue`, `list_issues`, `archive_issue`, `manage_issue_relation`, `get_issue_graph`, `get_planning_graph` | yes | no |
| planning | `validate_issue_plan`, `apply_issue_plan` | yes | no |
| review | `create_review_request`, `get_review_request`, `list_review_requests`, `cancel_review_request`, `supersede_review_request`, `replace_review_request` | yes | no |
| knowledge | `add_comment`, `record_decision`, `list_decisions`, `get_issue_activity`, `search` | yes | no |
| lifecycle | `claim_issue`, `renew_attempt`, `save_attempt_note`, `finish_attempt`, `get_work_context` | yes | no |

- **`full`** (default): every group, all 35 tools.
- **`agent`** (31 tools): every group except `migration` and `sync` — the
  complete ordinary issue discovery, planning, review, knowledge, and
  leased work lifecycle workflow, without bulk project transfer or
  incremental synchronization.
- **`read-only`**: exactly the tools with `readOnlyHint: true` in the
  section 4.1 matrix, spanning every group (including read operations
  inside `migration` and `sync`, e.g. `validate_import`,
  `get_changes` — reading is safe regardless of which group a tool
  otherwise belongs to). No tool this profile advertises can be
  classified as mutating; that invariant is enforced by
  `TestToolProfileReadOnlyContainsOnlyReadOnlyHintedTools` and
  `TestToolProfileReadOnlyIgnoresGroupCoreBypassForMutatingTool`. Every
  tool this profile advertises is also verified to perform zero durable
  writes, including MCP session bookkeeping — see section 4's
  `readOnlyHint` note above.
- **`migration`** (5 tools): `core` + `migration` —
  `open_project`, `get_project`, `export_project`, `validate_import`,
  `apply_import`. The
  minimal project opening/metadata/export/validate/apply transfer workflow,
  nothing else.

`tools/list` output is lexically ordered by the SDK regardless of profile
or registration order, so it stays deterministic across all four
profiles.

### 5.3. Disabled tools are absent and uncallable

A tool excluded by the active profile is never registered with the
underlying MCP server at all: it is both absent from `tools/list` and
fails as an unknown tool if a client calls it directly by name — there is
no hidden registration path that leaves it reachable. Stdio and local
HTTP transports are filtered identically, since both are built from the
same server composition and the same active profile
(`internal/adapters/mcp.Options.ToolProfile`).

---

## 6. Project and discovery

### 6.1. `open_project`

Purpose:

Resolve an existing project from an absolute repository root and return its
canonical `project_ref`, metadata, server capabilities, and guide links. This
read-only call does not establish state for later requests.

Input:

```json
{
  "project_root": "/absolute/path/to/repository"
}
```

Output:

The same metadata envelope as `get_project`, including `project_ref`. Retain
that reference and pass it to every later project-scoped tool call.

### 6.2. `get_project`

Purpose:

Return metadata and server capabilities for the project selected by
`project_ref`, or for the configured default when the reference is omitted.

Input:

```json
{
  "project_ref": "01ARZ3NDEKTSV4RRFFQ69G5FAV",
  "include_instructions": false
}
```

Output:

```text
project_ref
project
session
app_version
schema_version
config_version
tool_profile
limits
supported_issue_types
supported_statuses
supported_relation_types
supported_priorities
latest_event_id
guides
next_actions
```

The project instructions are returned only when requested. `guides` links the
three workflow resources advertised by the server.

### 6.3. `export_project`

Purpose:

Export the project selected by `project_ref` as the version 1 logical
interchange document.

Input:

```json
{
  "project_ref": "01ARZ3NDEKTSV4RRFFQ69G5FAV",
  "delivery": "artifact"
}
```

Output:

`artifact` is the default delivery. Its structured content is a bounded
acknowledgement containing `format`, `version`, `exported_at`, `byte_count`,
`sha256`, and an opaque `artifact_uri`. The URI names an owner-only file in the
server's managed export directory and is valid only to a server using that same
directory; clients must treat it as a short-lived local capability rather than a
portable file path.

Pass `delivery: "inline"` only when a caller needs the document in the response.
Inline delivery returns the full logical project document, but fails with
`LIMIT_EXCEEDED` when it exceeds 64 KiB. The default artifact file is retained
for at most 24 hours and its digest is verified whenever it is read. The artifact delivery path performs a durable write by creating a file and pruning files older than 24h in the server's managed export directory, so this tool is not part of the `read-only` profile despite its idempotent and non-destructive annotations.

### 6.4. `validate_import`

Purpose:

Validate a version 1 logical project interchange document without mutating storage and return a deterministic dry-run summary.

Input:

```json
{
  "project_ref": "01ARZ3NDEKTSV4RRFFQ69G5FAV",
  "source_uri": "rhizome-export://sha256/<sha256>/<opaque-file>"
}
```

Supply exactly one of `document` or `source_uri`. `document` is for a portable
JSON payload; `source_uri` is for a managed export artifact and rejects foreign
paths, symlinks, traversal, oversized files, and digest mismatches. A source URI
cannot be used by a separately configured server. Use explicit inline export or
an external transfer mechanism when moving a document between installations.

Output:

The structured content is the dry-run summary containing deterministic counts, zero writes, and sorted conflicts. The tool does not duplicate the full document payload in text.

### 6.5. `apply_import`

Purpose:

Apply a validated version 1 logical project interchange document into an empty destination and return a deterministic apply result with created counts, zero conflicts on success, and the latest destination event ID.

Input:

```json
{
  "project_ref": "01ARZ3NDEKTSV4RRFFQ69G5FAV",
  "source_uri": "rhizome-export://sha256/<sha256>/<opaque-file>"
}
```

As with `validate_import`, supply exactly one of `document` or `source_uri`.

Output:

The structured content is the apply result containing deterministic counts, sorted conflicts, and the latest event ID. The tool does not duplicate the full document payload in text.

### 6.6. `list_labels`

Input:

```json
{
  "project_ref": "01ARZ3NDEKTSV4RRFFQ69G5FAV",
  "query": null,
  "limit": 50,
  "cursor": null
}
```

Output:

```text
items
next_cursor
has_more
```

Deterministic ordering:

```text
normalized_name ASC
```

---

## 7. Issue operations

### 7.1. `create_issue`

Input:

```json
{
  "type": "task",
  "title": "Implement atomic claim",
  "description": null,
  "acceptance_criteria": null,
  "status": "open",
  "priority": "medium",
  "parent_issue_id": null,
  "blocked_reason": null,
  "labels": [],
  "create_missing_labels": true,
  "idempotency_key": null,
  "view": "compact"
}
```

Rules:

- `type`, `title` are required.
- `status` defaults to `open`.
- `priority` defaults to `medium`.
- `blocked_reason` is required when status is `blocked`.
- Parent constraints are validated.
- `idempotency_key` is optional. When supplied, it must be a non-blank string up to 128 runes. Reusing the same key with the same normalized request replays the original issue response; reusing it with a different request returns `IDEMPOTENCY_CONFLICT`.

`view` supports exactly `compact` and `full`. It defaults to `compact` when omitted. Explicit `view: "full"` preserves the legacy complete issue response for callers that need the full record.

Compact output (`view: "compact"`, default):

```json
{
  "id": "01ARZ3NDEKTSV4RRFFQ69G5FAV",
  "display_id": "ISSUE-42",
  "sequence_no": 42,
  "type": "task",
  "status": "open",
  "priority": "medium",
  "version": 1
}
```

Compact responses omit issue bodies, labels, timestamps, and other non-essential issue metadata. The full response is the legacy complete issue payload.

Migration guidance: callers that previously relied on the full issue record should pass `view: "full"`; callers that only need the compact identifiers and status fields can keep the default.

### 7.2. `update_issue`

Patch semantics:

- absent field: leave unchanged;
- `null`: clear a nullable field;
- empty string: an explicit value if allowed.

Input:

```json
{
  "issue_id": "ISSUE-42",
  "expected_version": 7,
  "changes": {
    "title": "Implement atomic issue claim",
    "description": null,
    "acceptance_criteria": null,
    "type": "task",
    "priority": "high",
    "status": "ready",
    "parent_issue_id": null,
    "blocked_reason": null,
    "labels": ["database", "concurrency"]
  },
  "create_missing_labels": true,
  "idempotency_key": null,
  "view": "compact"
}
```

Only changed fields should be present.

`idempotency_key` is optional. When supplied, it must be a non-blank string up
to 128 runes. Reusing the same key with the same normalized request (`issue_id`,
`expected_version`, `changes`, and `create_missing_labels`) replays the original
patch response, including after `expected_version` has since moved on from a
later, unrelated update. Reusing the key with a different normalized request
returns `IDEMPOTENCY_CONFLICT`.

`view` supports exactly `compact` and `full`. It defaults to `compact` when omitted. Explicit `view: "full"` preserves the legacy complete issue response with the full issue payload plus the changed field list.

Compact output (`view: "compact"`, default):

```json
{
  "issue": {
    "id": "01ARZ3NDEKTSV4RRFFQ69G5FAV",
    "display_id": "ISSUE-42",
    "status": "ready",
    "version": 8
  },
  "changed_fields": ["status", "priority", "version"]
}
```

Compact responses omit issue bodies, labels, timestamps, and other non-essential issue metadata. Migration guidance is the same as for create: callers that need the legacy full issue payload should pass `view: "full"`.

### 7.3. `get_issue`

Input:

```json
{
  "issue_id": "ISSUE-42",
  "view": "standard"
}
```

Views:

```text
compact
standard
full
```

Default:

```text
view = standard
```

Output:

```text
issue projection
```

Projection matrix:

```text
compact
- id
- display_id
- sequence_no
- type
- title
- version
- updated_at

standard (default when view is omitted)
- all compact fields
- status
- priority
- parent_issue_id
- blocked_reason
- created_at
- closed_at
- archived_at
- labels (bounded reference: id, name)
- description and acceptance_criteria are absent even when null

full
- exactly the existing full issue payload
- includes description and acceptance_criteria
- includes full label metadata records
```

`full` is the explicit form for free-text bodies and full label metadata. Compact and standard intentionally omit those fields to keep the response compact and predictable.

### 7.4. `list_issues`

Input filters:

```json
{
  "types": [],
  "statuses": [],
  "effective_statuses": [],
  "priorities": [],
  "labels": [],
  "parent_issue_id": null,
  "is_blocked": null,
  "is_claimable": null,
  "include_archived": false,
  "limit": 20,
  "cursor": null,
  "view": "compact"
}
```

Output:

```text
items
next_cursor
has_more
```

Deterministic ordering:

```text
priority DESC
is_claimable DESC
sequence_no ASC
```

`view` accepts exactly two values, `compact` and `full`. `view` defaults to
`compact` (including when the field is omitted entirely). Unknown values
(anything other than `compact` or `full`) are rejected as an unsupported
field with a structured validation error. `full` still honors the same
`limit`/cursor pagination bounds as `compact` — it is not a way to bypass
paging.

**`compact` (default) field set** — identifiers, title, classification, and
computed status/claimability fields only. No free-text issue bodies:

```text
id
display_id
sequence_no
type
title
status
effective_status
priority
is_blocked
is_claimable
unresolved_blocker_count
labels
updated_at
```

**`full` field set** — the complete issue record plus every computed field,
byte-identical to the pre-1.0 default response shape:

```text
id
display_id
sequence_no
type
title
description
acceptance_criteria
status
priority
parent_issue_id
blocked_reason
version
created_at
updated_at
closed_at
archived_at
labels
effective_status
unresolved_blocker_count
is_blocked
is_claimable
active_attempt_id
```

`full` adds `description`, `acceptance_criteria`, `parent_issue_id`,
`blocked_reason`, `version`, `created_at`, `closed_at`, `archived_at`, and
`active_attempt_id` on top of every `compact` field; nothing in `compact` is
ever different from its `full` value, and `full` is never missing a field
`compact` has.

**Migration note.** Before this change, `compact` was the only projection and
it silently returned every field listed above (including full `description`
and `acceptance_criteria` bodies) for every item — a project with a real
backlog could produce a response of tens to hundreds of kilobytes from a
single default `list_issues` call. If an existing client relied on full issue
bodies (or on `parent_issue_id`, `blocked_reason`, `version`, `created_at`,
`closed_at`, `archived_at`, or `active_attempt_id`) being present in
`list_issues` items, pass `view: "full"` to get that exact shape back
unchanged; no other input changes are required. Clients that only ever used
the fields now in the `compact` set need no changes at all.

**Response budget.** A 100-issue `list_issues` call in the default (`compact`)
view stays under **64 KB** of structured-content JSON regardless of how large
each issue's `description`/`acceptance_criteria` bodies are, because those
bodies are never present in the compact projection. This is enforced by an
integration test (`TestIntegrationListIssuesCompactViewStaysWithinByteBudget`
in `integration/list_issues_test.go`) that creates 100 issues with multi-kilobyte
description and acceptance-criteria bodies and asserts the default response
stays within budget; measured response size for that fixture is approximately
46 KB. The equivalent `view: "full"` call over the same 100 issues measures
approximately 582 KB in the same test — illustrating why `full` is opt-in.

### 7.5. `archive_issue`

Input:

```json
{
  "issue_id": "ISSUE-42",
  "expected_version": 9,
  "idempotency_key": null,
  "view": "compact"
}
```

Rules:

- active attempts prevent archiving;
- related data remains intact;
- archived issues are hidden by default.

`idempotency_key` is optional. When supplied, it must be a non-blank string up
to 128 runes. Reusing the same key with the same normalized request (`issue_id`
and `expected_version`) replays the original archive response, including after
the issue has already been archived by that same call. Reusing the key with a
different normalized request returns `IDEMPOTENCY_CONFLICT`.

`view` supports exactly `compact` and `full`. It defaults to `compact` when omitted. Explicit `view: "full"` preserves the legacy complete issue response for callers that need the full archived issue record.

Compact output (`view: "compact"`, default):

```json
{
  "id": "01ARZ3NDEKTSV4RRFFQ69G5FAV",
  "display_id": "ISSUE-42",
  "status": "ready",
  "version": 10
}
```

Compact responses omit issue bodies, labels, timestamps, and other non-essential issue metadata. Migration guidance is the same as for create: callers that need the full issue payload should pass `view: "full"`.

### 7.6. Review requests

Review requests bind review work to an issue version, event position, and
optional artifact set. A review request is claimable only while its status is
`open`.

#### `create_review_request`

**Deprecated.** `supersedes_id` only records a predecessor link; it never
closes that predecessor. Coordinating creation with a separate
`supersede_review_request` call leaves the review lifecycle in a partial
state after a failure or concurrency conflict between the two calls. Prefer
`replace_review_request` (below), which does both atomically. Retained as a
compatibility alias for one release; `supersedes_id` retains its current
(non-closing) semantics for as long as the alias exists.

Input:

```json
{
  "issue_id": "ISSUE-42",
  "target_issue_version": 9,
  "target_event_id": 1842,
  "artifact_ids": [],
  "supersedes_id": null
}
```

`issue_id`, `target_issue_version`, and `target_event_id` are required.
`artifact_ids` may contain at most 20 IDs. Creating another review request for
the same target returns `REVIEW_ALREADY_EXISTS`.

#### `replace_review_request`

Atomically supersedes a predecessor review request and creates its open
successor in one SQLite transaction: no partial state is observable between
"predecessor closed" and "successor created." The predecessor determines the
issue scope, so there is no separate `issue_id` field.

Input:

```json
{
  "predecessor_request_id": "01ARZ3NDEKTSV4RRFFQ69G5FAV",
  "predecessor_expected_version": 1,
  "target_issue_version": 10,
  "target_event_id": 1900,
  "artifact_ids": [],
  "idempotency_key": "replace-2026-07-24-01"
}
```

`predecessor_request_id`, `predecessor_expected_version`, `target_issue_version`,
`target_event_id`, and `idempotency_key` are required. Unlike every other
review-request tool, `idempotency_key` here is mandatory, not optional: this
operation does not hold the predecessor's attempt lease token, so replaying a
retried call safely (rather than risking a second successor from a client-side
retry) depends on the key.

Output:

```text
predecessor
successor
latest_event_id
```

`predecessor` and `successor` are each a full review request record (see the
shared field list below). `successor.supersedes_id` always points back to
`predecessor.id`, and `predecessor.status` is always `superseded`.

Failure modes, all structured and side-effect-free (zero writes):

- Stale `predecessor_expected_version` → `VERSION_CONFLICT` (retryable).
- Predecessor is currently `claimed` → `REVIEW_REQUEST_CLAIMED`. This
  operation does not carry the attempt's lease token, so it cannot detach or
  orphan an active review attempt; the lease holder must `finish_attempt` or
  otherwise interrupt its attempt first, which naturally returns the
  predecessor to `open` for the review requester to try again — or a client
  can resolve the review outcome and create a fresh request instead.
- Predecessor is any other terminal status (`approved`, `changes_requested`,
  `blocked`, `cancelled`, `superseded`) → `REVIEW_REQUEST_NOT_REPLACEABLE`.
- The successor's target already has an unrelated active request →
  `REVIEW_ALREADY_EXISTS`.
- Reusing `idempotency_key` with a different normalized request →
  `IDEMPOTENCY_CONFLICT`. Reusing it with the same request replays the
  original `predecessor`/`successor`/`latest_event_id` without any new
  writes or events.

#### `get_review_request`

Input:

```json
{
  "review_request_id": "01ARZ3NDEKTSV4RRFFQ69G5FAV"
}
```

#### `list_review_requests`

Input:

```json
{
  "status": "open",
  "claimable": true,
  "limit": 20,
  "cursor": null
}
```

`status` and `claimable` are optional filters. Supported statuses are:

```text
open
claimed
approved
changes_requested
blocked
cancelled
superseded
```

Output:

```text
items
next_cursor
has_more
```

#### `cancel_review_request` and `supersede_review_request`

**`supersede_review_request` is deprecated** for the same reason as
`create_review_request.supersedes_id` above: it closes a request without
creating or identifying a replacement, so a client must coordinate a second
`create_review_request` call itself. Prefer `replace_review_request`.
`cancel_review_request` is not deprecated — cancelling with no successor
remains a distinct, legitimate operation with no atomicity problem to fix.

Both operations require the request ID and its current version:

```json
{
  "review_request_id": "01ARZ3NDEKTSV4RRFFQ69G5FAV",
  "expected_version": 1
}
```

They apply only to open or claimed review requests and return the updated
review request.

Every review-request tool — including each of `replace_review_request`'s
`predecessor` and `successor` fields — returns a review request with:

```text
id
issue_id
target_issue_version
target_event_id
artifact_ids
status
supersedes_id
active_attempt_id
claimable
version
created_at
resolved_at
```

---

## 8. Relations and graphs

### 8.1. `manage_issue_relation`

Input:

```json
{
  "action": "add",
  "source_issue_id": "ISSUE-12",
  "target_issue_id": "ISSUE-42",
  "relation_type": "blocks",
  "idempotency_key": null
}
```

Actions:

```text
add
remove
```

Types:

```text
blocks
related_to
duplicates
```

Rules:

- relation identity is the canonical tuple;
- no relation ID is required for removal;
- cycles in `blocks` are rejected;
- symmetric `related_to` is canonicalized.

`idempotency_key` is optional. When supplied, it must be a non-blank string up
to 128 runes. Reusing the same key with the same normalized request (`action`,
`source_issue_id`, `target_issue_id`, and `relation_type`) replays the original
mutation response. Reusing the key with a different normalized request returns
`IDEMPOTENCY_CONFLICT`.

Output:

```text
relation
affected_issues
```

`affected_issues` is a bounded acknowledgement projection with exactly these
fields:

```text
id
display_id
version
status
effective_status
unresolved_blocker_count
is_blocked
is_claimable
```

It omits bodies, labels, timestamps, parent/block reason, and active attempt
IDs.

### 8.2. `get_issue_graph`

Input:

```json
{
  "root_issue_id": "ISSUE-42",
  "depth": 2,
  "direction": "both",
  "relation_types": ["blocks", "related_to"],
  "include_hierarchy": true,
  "include_terminal": true,
  "max_nodes": 100,
  "view": "compact"
}
```

Limits:

```text
depth default 2, maximum 5
max_nodes default 100, maximum 500
```

Output:

```text
root_issue_id
nodes
edges
summary
entry_points
truncated
truncation_reason
```

Graph format uses normalized `nodes` and `edges`, not recursive trees.

Epic hierarchy is represented as a derived `contains` edge.

**Response budget.** Each graph node is a bounded projection with exactly these
fields:

```text
id
display_id
sequence_no
type
title
status
effective_status
priority
unresolved_blocker_count
is_blocked
is_claimable
```

Graph nodes omit `description`, `acceptance_criteria`, labels,
`parent_issue_id`, `blocked_reason`, timestamps, and `active_attempt_id`.
The response envelope remains bounded by `max_nodes` (default 100, maximum
500), and the `structuredContent` payload is kept under the 96 KiB MCP cap for
100-node graph results.

### 8.3. `get_planning_graph`

Input:

```json
{
  "root_issue_id": null,
  "depth": 3,
  "max_nodes": 100,
  "include_review": true,
  "include_related": false
}
```

Behavior:

- includes epic hierarchy;
- includes blockers;
- excludes archived issues;
- highlights claimable entry points;
- includes active attempt summaries;
- excludes full descriptions.

Output:

```text
nodes
edges
entry_points
blocking_nodes
summary
warnings
truncated
```

**Response budget.** Shares the same bounded node projection and the same
100-node `structuredContent` 96 KiB budget documented in section 8.2 for
`get_issue_graph` — see that section's response budget note.

---

## 9. Batch planning

### 9.1. `validate_issue_plan`

Dry-run only.

Input:

```json
{
  "issues": [],
  "relations": [],
  "decisions": [],
  "include_normalized_plan": false
}
```

New entities may define local refs:

```json
{
  "ref": "storage-layer",
  "type": "task",
  "title": "Implement storage layer"
}
```

Validation includes:

- enum values;
- field limits;
- parent constraints;
- local refs;
- relation duplicates;
- `blocks` cycles;
- batch limits.

Output:

```text
valid
errors
warnings
summary
plan_fingerprint
normalization_changed
normalized_plan (only when requested)
next_actions
```

`include_normalized_plan` defaults to `false`. The compact default returns
diagnostics, counts, whether normalization changed the submitted plan, and a
stable lowercase SHA-256 fingerprint of the normalized plan's deterministic
JSON encoding. Set `include_normalized_plan: true` when a caller needs the full
normalized plan, including before passing that exact result to
`apply_issue_plan`. The fingerprint is identical for an input and its already
normalized equivalent.

Errors are deterministically sorted by:

```text
entity index
field path
error code
```

### 9.2. `apply_issue_plan`

Input is the validated plan plus:

```json
{
  "idempotency_key": "plan-storage-v1"
}
```

Limits:

```text
50 new issues
100 relations
50 label assignments
20 decisions
```

Behavior:

- performs the same validation again;
- executes in one transaction;
- rolls back completely on any error;
- assigns issue numbers atomically.

Output:

```text
created_issues by local ref
created_relations
created_decisions
latest_event_id
```

---

## 10. Communication and durable knowledge

### 10.1. `add_comment`

Implemented as append-only issue communication. The issue must exist and must
not be archived. When the MCP connection has a durable session, the created
comment and its `comment_added` event use that session for attribution;
otherwise both attributions are NULL. The operation writes one compact event
payload containing only the comment ID and returns the created comment.

Input:

```json
{
  "issue_id": "ISSUE-42",
  "content": "The claim transaction must also create the event.",
  "idempotency_key": null
}
```

Output:

```text
comment
```

`idempotency_key` is optional. When supplied, it must be a non-blank string up
to 128 runes. Reusing the same key with the same normalized request (`issue_id`
and `content`) replays the original comment response. Reusing the key with a
different normalized request returns `IDEMPOTENCY_CONFLICT`.

### 10.2. `record_decision`

Input:

```json
{
  "issue_id": "ISSUE-42",
  "title": "Use renewable leases",
  "summary": "Active attempts use short renewable leases.",
  "content": "Full reasoning in Markdown.",
  "status": "active",
  "supersedes_id": null
}
```

Output:

```text
decision
superseded_decision_id
```

Decisions are append-only records and may be project-level or issue-level.
Supplying `supersedes_id` atomically creates an active replacement and marks
one active predecessor superseded; the predecessor must have the same scope.
The standalone operation writes one compact, session-attributed
`decision_recorded` event.

`record_decision` does not accept `idempotency_key`: the field is not part of
its published input schema. Unlike the other mutations in this catalog,
`supersedes_id` makes one call responsible for two conditional writes (marking
a predecessor superseded and inserting its replacement); replaying that
combination safely would require storing and re-validating the predecessor's
state as part of the cached response, which is disproportionate to the value
for an append-only decision log. Retry `record_decision` by first checking
`list_decisions` for a decision already recorded with the intended content.

### 10.3. `list_decisions`

Lists project-level decisions when `issue_id` is omitted, or decisions scoped
to one issue when it is supplied. Results use deterministic cursor pagination.

### 10.4. `get_issue_activity`

Input:

```json
{
  "issue_id": "ISSUE-42",
  "types": [
    "comments",
    "decisions",
    "reviews",
    "attempts",
    "attempt_notes",
    "events",
    "artifacts"
  ],
  "limit": 20,
  "cursor": null,
  "order": "newest_first"
}
```

Output:

```text
items
next_cursor
has_more
```

Every item contains `entity_type`, `entity_id`, `issue_id`, `occurred_at`, and
exactly one matching typed payload among `comment`, `decision`, `review`,
`attempt`, `attempt_note`, `event`, and `artifact`. The envelope owns entity
identity, issue scope, and occurrence time. Typed payloads omit their repeated
entity ID, issue ID, and occurrence timestamp while preserving their other
category-specific fields.

The `types` input is optional; when omitted or empty, all categories are
returned. Supported categories are exactly `comments`, `decisions`, `reviews`,
`attempts`, `attempt_notes`, `events`, and `artifacts`. The default limit is
`20`, the maximum limit is `100`, and only `newest_first` ordering is supported.

Pagination uses an opaque, versioned cursor; invalid cursors fail with
structured invalid-argument errors. The response includes `items`,
`next_cursor`, and `has_more`. Attempts do not expose lease tokens
or lease hashes. Event payloads preserve durable activity metadata. Results are
returned from one consistent read snapshot and are ordered deterministically by
`occurred_at` descending, then a fixed category rank, then source ID. Global or
null-scope decisions and events are excluded from issue activity; full
issue-owned event history, including issue creation, remains included.

**Response budget.** Item count is bounded by `limit` (default `20`, maximum
`100`); this bound is enforced, not just documented. Each item's own
free-text field is bounded at write time, not by activity itself: comment
content up to 50,000 runes (`add_comment`), decision content up to 100,000
runes (`record_decision`), and attempt/attempt-note content up to 50,000
runes each (`finish_attempt` / `save_attempt_note`). A page of `limit` items
that are all near their per-item maximum is a real, if unusual, worst case —
for size-sensitive callers, narrow `types` to the categories you need and
prefer the default `limit` over the maximum.

---

## 11. Agent work lifecycle

### 11.1. `claim_issue`

Input:

```json
{
  "issue_id": "ISSUE-42",
  "lease_seconds": null,
  "idempotency_key": null,
  "view": "compact"
}
```

Behavior:

- checks claimability;
- determines `work` or `review`;
- creates attempt atomically;
- records issue version and event ID;
- creates an opaque lease token;
- accepts an optional `idempotency_key` that replays the original claim response for the same normalized request and returns `IDEMPOTENCY_CONFLICT` for a different request with the same key.

`view` supports exactly `compact` and `full`. It defaults to `compact` when omitted. Explicit `view: "full"` preserves the legacy complete claim response with the full issue projection, the full attempt payload, and the same workflow context fields.

Compact output (`view: "compact"`, default):

```json
{
  "issue": {
    "id": "01ARZ3NDEKTSV4RRFFQ69G5FAV",
    "status": "ready",
    "version": 8
  },
  "attempt": {
    "id": "01J0ABCDEF1234567890",
    "kind": "work",
    "lease_expires_at": "2026-08-05T12:34:56Z"
  },
  "lease_token": "opaque-token"
}
```

The `lease_token` field appears only in claim results, including idempotent claim replay responses. Compact claim responses omit issue bodies, labels, timestamps, attempt history, and other non-essential metadata. They also never introduce lease tokens outside claim results. Migration guidance is the same as for the other mutations: callers that need the legacy full claim payload should pass `view: "full"`.

### 11.2. `renew_attempt`

Input:

```json
{
  "attempt_id": "01J...",
  "lease_token": "opaque-token",
  "lease_seconds": null
}
```

Output:

```text
lease_expires_at
server_time
```

No content-heavy audit event is written for every heartbeat.

### 11.3. `save_attempt_note`

Input:

```json
{
  "attempt_id": "01J...",
  "lease_token": "opaque-token",
  "kind": "checkpoint",
  "content": "Repository layer is implemented.",
  "next_steps": [
    "Implement claim transaction",
    "Add concurrency tests"
  ],
  "important": true,
  "artifacts": [],
  "idempotency_key": null
}
```

Kinds:

```text
progress
finding
warning
checkpoint
```

`idempotency_key` is optional. When supplied, it must be a non-blank string up
to 128 runes. Reusing the same key with the same normalized request (the
lease-token proof, `kind`, `content`, `next_steps`, `important`, and
`artifacts`) replays the original note response without creating another note,
event, or artifact set. Reusing the key with a different normalized request
returns `IDEMPOTENCY_CONFLICT`.

Output:

```text
attempt_note
artifacts
```

### 11.4. `finish_attempt`

Common input:

```text
attempt_id
lease_token
outcome
result_summary
next_steps
verification
artifacts
acknowledged_changes
idempotency_key
```

`idempotency_key` is optional for `finish_attempt`. When supplied, the
normalized request (including the lease-token proof and caller artifact fields,
but excluding the transient MCP session and generated artifact values) is
hashed and stored with the final response in the same SQLite transaction.
Retrying the same key with the same normalized request replays that exact
response, including after reconnect or database reopen, without creating
another event or artifact set. Reusing the key with a different normalized
request returns `IDEMPOTENCY_CONFLICT`; a request without a key retains the
ordinary non-idempotent finish behavior.

Work outcomes:

```text
completed
failed
interrupted
```

Work completion also supplies:

```text
target_issue_status: done | review | ready | blocked
blocked_reason
failure_reason_code
interruption_reason_code
reason_details
```

Review completion supplies:

```text
review_outcome:
  approved
  changes_requested
  blocked
```

`view` supports exactly `compact` and `full`. It defaults to `compact` when omitted. Explicit `view: "full"` preserves the legacy complete finish response with the full attempt and issue payloads plus the complete artifact set.

Compact output (`view: "compact"`, default):

```json
{
  "attempt": {
    "id": "01J0ABCDEF1234567890",
    "issue_id": "01ARZ3NDEKTSV4RRFFQ69G5FAV",
    "kind": "work",
    "status": "completed",
    "issue_version_at_start": 8
  },
  "issue": {
    "id": "01ARZ3NDEKTSV4RRFFQ69G5FAV",
    "status": "done",
    "version": 9
  },
  "warnings": [],
  "latest_event_id": 1842,
  "artifacts": [
    {
      "id": "01J0XYZ1234567890",
      "type": "file",
      "uri": "file:///tmp/summary.md",
      "title": "Summary"
    }
  ],
  "next_actions": ["Select new work from get_planning_graph."]
}
```

Compact finish responses omit issue bodies, labels, timestamps, unnecessary attempt history/default fields, artifact metadata, and artifact timestamps. They also never introduce lease tokens; only claim results carry that field. Migration guidance is the same as for the other mutations: callers that need the legacy full finish payload should pass `view: "full"`.

Completion checks:

- lease validity;
- issue archive/cancel state;
- blockers;
- issue changes since claim;
- required acknowledgments.

### 11.5. `get_work_context`

Input:

```json
{
  "issue_id": "ISSUE-42",
  "include": [],
  "limits": {}
}
```

Minimal default includes:

```text
issue title and description
acceptance criteria
effective status
unresolved blockers
active decision summaries
review summaries
previous attempt result summary
previous attempt next steps
latest checkpoint
warnings
```

Optional includes:

```text
parent_epic
relations
related_issue_summaries
recent_comments
recent_attempt_notes
decision_content
attempt_history
artifacts
project_instructions
changes_since_previous_attempt
```

Output:

```text
issue
blockers
decisions
reviews
previous_attempt
checkpoint
requested optional sections
warnings
truncated
truncated_sections
next_actions
```

Optional detail enriches existing records rather than creating parallel
copies. When `decision_content` is requested, matching entries in `decisions`
gain `content`, `supersedes_id`, and `created_by_session_id`; no separate
`decision_content` output collection is emitted. When both `parent_epic` and
`related_issue_summaries` are requested, the parent appears only in
`parent_epic`, even if an explicit relation would also select it as a related
issue.

**Response budget.** `get_work_context` is scoped to one issue, so its full
`description`/`acceptance_criteria` bodies (needed to actually work the
issue) are an intentional, expected part of the default response — this is
unlike `list_issues`, where the same fields were being repeated once per
backlog item for no benefit. Every optional list section (`related_issue_summaries`,
`recent_comments`, `recent_attempt_notes`, decision details selected by `decision_content`,
`attempt_history`, `artifacts`, `changes_since_previous_attempt`) is capped at
1–20 items via `limits` (default varies per section; see the audited request
schema), and at most 10 sections can be requested at once
(`MaxWorkContextIncludes`), so optional-section growth is bounded. `blockers`
and `parent_epic` reuse the same per-issue projection as the primary `issue`
field (including full `description`/`acceptance_criteria`), and `blockers` in
particular has no configurable cap — it is bounded only by how many issues
directly block the requested one, which is normally small for a real
dependency graph. `related_issue_summaries` is named "summaries" but, like
`blockers`, currently returns the full per-issue projection rather than a
truncated preview; this is a known imprecision worth tightening in a future
change but is not addressed here, since (unlike the `list_issues` default) it
requires an explicit `include` entry and is capped at 20 items.

---

## 12. Search and synchronization

### 12.1. `search`

Input:

```json
{
  "query": "\"renewable lease\" OR heartbeat",
  "entity_types": [
    "issue",
    "comment",
    "decision",
    "review",
    "attempt_note"
  ],
  "issue_id": null,
  "epic_id": null,
  "statuses": [],
  "labels": [],
  "include_archived": false,
  "limit": 20,
  "cursor": null,
  "snippet_length": 300
}
```

Supported entity types are `issue`, `comment`, `decision`, `review`, and
`attempt_note`.

FTS5 syntax supports raw phrases, boolean operators, prefixes, and column
filters. Punctuation-bearing literal terms, including hyphenated terms such as
`multi-project`, must be quoted as FTS5 phrases (for example, `"multi-project"`).

Maximum snippet length: `1000`.

Output:

```text
results:
  entity_type
  entity_id
  issue_id
  title
  snippet
  score
next_cursor
has_more
```

Full source documents are never returned by search.

**Response budget.** `snippet_length` truncation is enforced at the storage
layer (a SQL `substr` over the FTS5 snippet, re-validated against
`MaxSearchSnippetRunes` on the way out), not merely documented. Combined with
the `limit` cap, a `search` response is bounded by at most `limit` (maximum
`100`) results, each with a `title` and a `snippet` of at most
`snippet_length` runes (maximum `1000`, default `300`); the worst case is
therefore on the order of 100 KB, and the default (`limit` `20`,
`snippet_length` `300`) is on the order of 10 KB.

### 12.2. `get_changes`

Input:

```json
{
  "since_event_id": 1842,
  "issue_id": null,
  "event_types": [],
  "limit": 50
}
```

Maximum limit: `200`.

Output:

```text
events
latest_event_id
has_more
next_event_id
```

This tool supports incremental refresh instead of repeatedly reading full state.

Relation writes retain one durable event per endpoint so an `issue_id`-scoped
feed observes every relation that affects that issue. An unfiltered global feed
returns one canonical event for each matching `relation_added` or
`relation_removed` endpoint pair. Consequently, global event IDs are strictly
increasing but may contain gaps where the redundant endpoint copy was hidden.
Clients must advance with `next_event_id` and `has_more`, not by assuming event
IDs are contiguous. Event-type filtering, ordering, pagination, and
`latest_event_id` semantics are otherwise unchanged.

## 13. Error codes

Required domain error codes:

```text
ISSUE_NOT_FOUND
ISSUE_ARCHIVED
ISSUE_BLOCKED
ISSUE_NOT_CLAIMABLE
INVALID_STATUS_TRANSITION
VERSION_CONFLICT
ACTIVE_ATTEMPT_EXISTS
ATTEMPT_NOT_FOUND
ATTEMPT_NOT_ACTIVE
LEASE_EXPIRED
INVALID_LEASE_TOKEN
ISSUE_CHANGED_DURING_ATTEMPT
UNRESOLVED_BLOCKERS_ADDED
BLOCKS_CYCLE
RELATION_ALREADY_EXISTS
INVALID_EPIC_PARENT
IDEMPOTENCY_CONFLICT
LIMIT_EXCEEDED
VALIDATION_ERROR
```

Internal SQLite errors and stack traces are logged locally and mapped to stable domain errors.
