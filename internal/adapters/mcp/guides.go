package mcp

import (
	"context"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const initializeInstructions = "Start with get_project, then use get_planning_graph or list_issues to find claimable work. Read get_work_context before claim_issue. While working, renew the lease and save restartable checkpoints; always finish_attempt on completion, failure, or handoff. Use expected_version for issue writes. Detailed guides: rhizome://guides/agent-workflow."

type guide struct {
	URI         string
	Name        string
	Title       string
	Description string
	Content     string
}

var guides = []guide{
	{
		URI:         "rhizome://guides/agent-workflow",
		Name:        "agent-workflow",
		Title:       "Rhizome Agent Workflow",
		Description: "End-to-end workflow for selecting, claiming, executing, and finishing tracked work.",
		Content: `# Agent workflow

## 1. Orient

Call ` + "`get_project`" + ` once per session. Use its limits, supported values, latest event ID, project instructions, and guide links. Read only the guide needed for the current operation.

## 2. Find work

- Use ` + "`get_planning_graph`" + ` for dependency-aware selection. Entry points are executable roots; blocking nodes explain stalled work.
- Use ` + "`list_issues`" + ` with ` + "`is_claimable: true`" + ` for a narrow ready queue.
- Use ` + "`search`" + ` for historical knowledge, not as the authoritative current state.
- Follow cursors or event IDs when a result says more data exists.

Select one coherent issue. Do not begin blocked work or duplicate an active attempt.

## 3. Load context

Call ` + "`get_work_context`" + ` before claiming. Start with the default compact context, then request only needed sections such as parent epic, relations, recent comments, decision content, attempt history, artifacts, project instructions, or changes since the previous attempt. Use ` + "`get_issue_activity`" + ` for a chronological audit trail.

Treat active decisions and acceptance criteria as durable constraints. If requirements are missing or contradictory, add a comment or record a decision instead of guessing.

## 4. Claim before execution

Call ` + "`claim_issue`" + ` only for a claimable ` + "`ready`" + ` or ` + "`review`" + ` issue. Keep the returned attempt ID and lease token private and available until the attempt ends. Effective ` + "`in_progress`" + ` is derived from this active lease; it is not a stored issue status.

For long work, call ` + "`renew_attempt`" + ` before expiry. A lost or expired lease must not be treated as ownership.

## 5. Execute durably

- Use ` + "`save_attempt_note`" + ` for restartable checkpoints, important findings, warnings, and concrete next steps.
- Attach useful artifacts such as commits, branches, pull requests, files, URLs, and logs.
- Use comments for collaboration and decisions for durable architectural or product choices.
- Use ` + "`update_issue`" + ` and ` + "`archive_issue`" + ` with the current issue version. On a version conflict, refetch, reconcile, and retry; never overwrite concurrent changes blindly.
- Validate multi-issue plans before applying them atomically.

## 6. Finish every attempt

Call ` + "`finish_attempt`" + ` exactly once when work completes, fails, becomes blocked, or is handed off. Include a concise result, verification actually performed, artifacts, and actionable next steps.

- Completed implementation normally targets ` + "`review`" + ` or ` + "`done`" + ` according to project policy.
- Failed work records a failure reason and truthful details.
- Handoffs use ` + "`outcome: interrupted`" + ` with ` + "`interruption_reason_code: handoff`" + `.
- If relevant changes happened after the claim, inspect them and acknowledge the issue version and latest event ID.

Never leave an attempt active merely because the agent is stopping.`,
	},
	{
		URI:         "rhizome://guides/issue-lifecycle",
		Name:        "issue-lifecycle",
		Title:       "Rhizome Issue Lifecycle",
		Description: "Status, dependency, review, versioning, and archival rules for issues.",
		Content: `# Issue lifecycle

## Types and hierarchy

Issues are ` + "`epic`" + `, ` + "`task`" + `, or ` + "`bug`" + `. Use parent relationships for decomposition and ` + "`blocks`" + ` relations for execution order. ` + "`related_to`" + ` adds context without scheduling semantics; ` + "`duplicates`" + ` identifies equivalent work.

## Stored statuses

- ` + "`open`" + `: known work that is not yet executable.
- ` + "`ready`" + `: implementation work may be claimed when dependencies permit.
- ` + "`blocked`" + `: explicitly paused; provide a useful blocked reason.
- ` + "`review`" + `: completed work awaiting review; review attempts may claim it.
- ` + "`done`" + `: accepted terminal work.
- ` + "`cancelled`" + `: intentionally abandoned terminal work.

` + "`in_progress`" + ` is an effective status derived from an active leased attempt. Never write it as an issue status. If a lease expires, the effective status falls back to the stored state so work cannot remain permanently stuck.

## Readiness and blockers

An issue is claimable only when its stored status permits the requested attempt and unresolved blockers do not prevent execution. Use ` + "`get_planning_graph`" + ` or ` + "`list_issues`" + ` with claimability filters instead of inferring readiness from titles or comments.

When adding a ` + "`blocks`" + ` relation, the source blocks the target. Keep dependency graphs acyclic and use the planning graph to confirm entry points.

## Mutations and concurrency

Issue updates and archival use optimistic concurrency:

1. Read the issue and retain its ` + "`version`" + `.
2. Submit that value as ` + "`expected_version`" + `.
3. If the version conflicts, refetch and reconcile all intervening changes.

Do not retry a stale patch blindly. Use bounded plan validation plus atomic plan application when creating several related issues, relations, and decisions together.

## Review and completion

Implementation attempts should record verification and artifacts before moving work to review or done. Review attempts finish with ` + "`approved`" + `, ` + "`changes_requested`" + `, or ` + "`blocked`" + ` and set the target issue status consistently.

Use comments for transient collaboration. Use decisions for durable choices that future agents must follow. Supersede decisions append-only rather than rewriting history.

## Archival

Archival hides obsolete records from normal lists without deleting history. Archive only with the current version and include archived records explicitly when searching or listing them.`,
	},
	{
		URI:         "rhizome://guides/multi-agent-handoff",
		Name:        "multi-agent-handoff",
		Title:       "Rhizome Multi-Agent Handoff",
		Description: "Durable checkpoint, interruption, recovery, and review guidance across agents.",
		Content: `# Multi-agent handoff

## Before handing off

Preserve enough state for another agent to continue without reconstructing your session:

1. Save an important checkpoint with ` + "`save_attempt_note`" + `.
2. State what changed, what remains, current risks or blockers, and exact next steps.
3. Attach durable artifacts: commit, branch, pull request, relevant file, URL, or verification log.
4. Record durable design choices with ` + "`record_decision`" + `; do not bury them only in a checkpoint.
5. Finish the attempt as ` + "`interrupted`" + ` with reason ` + "`handoff`" + `. Never transfer or publish the lease token.

Keep notes factual and restartable. Avoid raw transcripts, speculative status, and duplicated repository documentation.

## Receiving a handoff

1. Refetch the issue and confirm it is claimable.
2. Call ` + "`get_work_context`" + ` with checkpoint, recent attempt notes, attempt history, artifacts, decisions, relations, and changes since the previous attempt as needed.
3. Inspect referenced artifacts and verify the repository state; do not trust a summary as proof.
4. Use ` + "`get_changes`" + ` from the prior context event ID or ` + "`get_issue_activity`" + ` when concurrent work may have changed assumptions.
5. Claim a new attempt. Never reuse another agent's attempt ID or lease token.

## Concurrent changes

Leases prevent duplicate active ownership of an issue, not edits elsewhere in the project. Before finishing, check relevant changes and reconcile newer issue versions, decisions, blockers, and artifacts. If the server requires acknowledgement, send the observed issue version and latest event ID.

## Review handoff

An implementation handoff should identify acceptance criteria covered, tests run, unverified behavior, and review entry points. A reviewer independently verifies artifacts and finishes its review attempt with an explicit review outcome. Changes requested should return the issue to an executable status with concrete next steps.

## Failure and recovery

If work cannot continue, finish the attempt truthfully as failed, interrupted, or blocked. Include a stable reason code, concise details, and next steps. Do not leave an active attempt as an implicit handoff; lease expiry is recovery protection, not a workflow.`,
	},
}

func registerGuides(server *sdkmcp.Server) {
	for _, item := range guides {
		item := item
		server.AddResource(&sdkmcp.Resource{
			URI: item.URI, Name: item.Name, Title: item.Title,
			Description: item.Description, MIMEType: "text/markdown", Size: int64(len(item.Content)),
		}, func(context.Context, *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
			return &sdkmcp.ReadResourceResult{Contents: []*sdkmcp.ResourceContents{{
				URI: item.URI, MIMEType: "text/markdown", Text: item.Content,
			}}}, nil
		})
	}
}

func guideLinks() []guideLinkDTO {
	result := make([]guideLinkDTO, len(guides))
	for index, item := range guides {
		result[index] = guideLinkDTO{URI: item.URI, Title: item.Title, Description: item.Description}
	}
	return result
}
