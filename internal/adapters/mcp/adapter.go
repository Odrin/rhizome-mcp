// Package mcp exposes application services through MCP tools.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/projectrouting"
)

// resolveView validates and defaults the view parameter. An empty input yields
// the defaultView; a value in allowed is returned as-is; anything else returns
// the unsupported field error.
func resolveView(input string, defaultView string, allowed ...string) (string, error) {
	view := input
	if view == "" {
		view = defaultView
	}
	for _, candidate := range allowed {
		if view == candidate {
			return view, nil
		}
	}
	return "", unsupportedField("view")
}

// Options supplies the explicit composition dependencies for the MCP adapter.
type Options struct {
	ProjectRouter   projectrouting.ProjectRouter
	ServerName      string
	ServerVersion   string
	ConfigVersion   int
	ExportDirectory string
	// ToolProfile selects which capability groups of the tool catalog this
	// server instance advertises. Blank defaults to domain.ToolProfileFull.
	// Mutually exclusive with Toolsets.
	ToolProfile string
	// Toolsets selects a free-form comma-separated list of capability
	// groups (domain.AllToolsets) to advertise instead of a named profile;
	// the core group is always included. Blank means no selection.
	// Mutually exclusive with ToolProfile.
	Toolsets string
	// ContentMode controls how tool results populate Content and
	// StructuredContent (auto, structured, text). Blank defaults to
	// domain.ContentModeAuto.
	ContentMode string
}

type adapter struct {
	router        projectrouting.ProjectRouter
	services      application.Bundle
	appVersion    string
	configVersion int
	toolProfile   domain.ToolProfile
	// toolsets is the active free-form catalog selection; nil means the
	// named toolProfile decides registration instead (profile.go).
	toolsets    map[domain.Toolset]bool
	contentMode domain.ContentMode
	exports     *exportArtifactStore
}

// Server owns the MCP SDK server and its adapter lifecycle tracking.
type Server struct {
	server  *sdkmcp.Server
	adapter *adapter
}

// NewServer composes the MCP server without process-global dependencies.
func NewServer(options Options) (*Server, error) {
	if options.ProjectRouter == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "project router is required", false)
	}
	if options.ServerName == "" {
		return nil, domain.NewError(domain.CodeInvalidArgument, "server name is required", false)
	}
	if options.ServerVersion == "" {
		return nil, domain.NewError(domain.CodeInvalidArgument, "server version is required", false)
	}
	if strings.TrimSpace(options.ToolProfile) != "" && strings.TrimSpace(options.Toolsets) != "" {
		return nil, domain.NewError(domain.CodeInvalidArgument,
			"tool profile and toolsets are mutually exclusive; configure at most one of --profile/RHIZOME_TOOL_PROFILE and --toolsets/RHIZOME_TOOLSETS", false,
			domain.Detail{Field: "tool_profile", Code: "MUTUALLY_EXCLUSIVE"},
			domain.Detail{Field: "toolsets", Code: "MUTUALLY_EXCLUSIVE"})
	}
	toolProfile, err := domain.ParseToolProfile(options.ToolProfile)
	if err != nil {
		return nil, err
	}
	selectedToolsets, err := domain.ParseToolsets(options.Toolsets)
	if err != nil {
		return nil, err
	}
	contentMode, err := domain.ParseContentMode(options.ContentMode)
	if err != nil {
		return nil, err
	}
	var toolsets map[domain.Toolset]bool
	if selectedToolsets != nil {
		toolsets = make(map[domain.Toolset]bool, len(selectedToolsets))
		for _, toolset := range selectedToolsets {
			toolsets[toolset] = true
		}
	}
	exports, err := newExportArtifactStore(options.ExportDirectory)
	if err != nil {
		return nil, domain.WrapError(err, domain.CodeStorageConfiguration, "managed export artifacts could not be initialized", false)
	}
	var services application.Bundle
	if lease, err := options.ProjectRouter.Acquire(context.Background(), nil); err != nil {
		var domainErr *domain.Error
		if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeProjectRequired {
			return nil, err
		}
	} else if lease == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "project router returned a nil lease", false)
	} else {
		services = lease.Services()
		if err := lease.Release(); err != nil {
			return nil, err
		}
		if err := services.Validate(); err != nil {
			return nil, err
		}
	}
	adapter := &adapter{
		router:        options.ProjectRouter,
		services:      services,
		appVersion:    options.ServerVersion,
		configVersion: options.ConfigVersion,
		toolProfile:   toolProfile,
		toolsets:      toolsets,
		contentMode:   contentMode,
		exports:       exports,
	}
	server := sdkmcp.NewServer(
		&sdkmcp.Implementation{Name: options.ServerName, Version: options.ServerVersion},
		&sdkmcp.ServerOptions{
			Capabilities: &sdkmcp.ServerCapabilities{Tools: &sdkmcp.ToolCapabilities{}},
			Instructions: initializeInstructions,
		},
	)
	server.AddReceivingMiddleware(adapter.credentialValidationMiddleware)
	adapter.register(server)
	registerGuides(server)
	return &Server{server: server, adapter: adapter}, nil
}

func (adapter *adapter) credentialValidationMiddleware(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
	return func(ctx context.Context, method string, request sdkmcp.Request) (sdkmcp.Result, error) {
		if method == "tools/call" {
			if call, ok := request.(*sdkmcp.CallToolRequest); ok && call.Params != nil {
				if err := validateCredentialArguments(call.Params.Name, call.Params.Arguments); err != nil {
					result, _, failureErr := adapter.failure(err)
					return result, failureErr
				}
			}
		}
		return next(ctx, method, request)
	}
}

// SDKServer exposes the underlying SDK server for transports that manage their own lifecycle.
func (server *Server) SDKServer() *sdkmcp.Server {
	if server == nil {
		return nil
	}
	return server.server
}

// EndSession no longer controls durable attribution. It remains a no-op until
// callers have migrated to end_agent_session.
func (server *Server) EndSession(context.Context, string) error { return nil }

// Run serves one MCP connection.
func (server *Server) Run(ctx context.Context, transport sdkmcp.Transport) error {
	sdkSession, err := server.server.Connect(ctx, transport, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = sdkSession.Close()
	}()

	done := make(chan error, 1)
	go func() {
		done <- sdkSession.Wait()
	}()
	select {
	case <-ctx.Done():
		_ = sdkSession.Close()
		<-done
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func isContextCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// register builds every tool definition once and hands it to registerTool
// with an explicit capability group; registerTool then advertises it only
// if the adapter's active tool profile includes that group (see profile.go).
// Every call below is required to name a group — there is no path to
// sdkmcp.AddTool that skips this decision.
func (target *adapter) register(server *sdkmcp.Server) {
	target.registerTool(server, groupLifecycle, tool("create_agent_session", "Create a durable attribution session and one unrecoverable handle; writes a new session on every call (non-idempotent).", schemaCreateAgentSession(), schemaCreateAgentSessionOutput(), toolHints(false, false, false, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[createAgentSessionInput, any](target, t, (*adapter).createAgentSession))
	})
	target.registerTool(server, groupLifecycle, tool("end_agent_session", "End one explicitly created durable agent session handle.", schemaEndAgentSession(), schemaEndAgentSessionOutput(), toolHints(false, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[endAgentSessionInput, any](target, t, (*adapter).endAgentSession))
	})
	target.registerTool(server, groupCore, tool("get_project", "Get metadata, limits, supported values, event position, and guide links for a project_ref or configured default.", schemaGetProject(), schemaProjectOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[getProjectInput, any](target, t, (*adapter).getProject))
	})
	target.registerTool(server, groupCore, tool("open_project", "Open a project by absolute root and return its project_ref, metadata, limits, supported values, event position, and guide links.", schemaOpenProject(), schemaProjectOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, func(ctx context.Context, request *sdkmcp.CallToolRequest, input openProjectInput) (*sdkmcp.CallToolResult, any, error) {
			return target.openProject(ctx, request, input)
		})
	})
	target.registerTool(server, groupMigration, tool("export_project", "Export the selected project as the version 1 logical interchange document.", schemaExportProject(), schemaExportProjectOutput(), toolHints(false, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[exportProjectInput, any](target, t, (*adapter).exportProject))
	})
	target.registerTool(server, groupMigration, tool("validate_import", "Validate a logical project import document without writing anything.", schemaValidateImport(), schemaValidateImportOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[validateImportInput, any](target, t, (*adapter).validateImport))
	})
	// apply_import requires an empty destination, so a bare repeat with the
	// same document fails safely once the destination is non-empty: no
	// additional effect on retry, hence idempotentHint true despite no
	// idempotency_key support.
	target.registerTool(server, groupMigration, tool("apply_import", "Apply a validated logical project import document into an empty destination.", schemaApplyImport(), schemaApplyImportOutput(), toolHints(false, true, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[applyImportInput, any](target, t, (*adapter).applyImport))
	})
	target.registerTool(server, groupIssues, tool("list_labels", "List reusable labels with optional name search and cursor pagination.", schemaListLabels(), schemaLabelListOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[listLabelsInput, any](target, t, (*adapter).listLabels))
	})
	// create_issue's idempotency_key is optional: a bare repeat without it
	// creates a second issue, so idempotentHint is false.
	target.registerTool(server, groupIssues, tool("create_issue", "Create one epic, task, or bug with optional hierarchy and labels.", schemaCreateIssue(), schemaCreateIssueOutput(), toolHints(false, false, false, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[createIssueInput, any](target, t, (*adapter).createIssue))
	})
	// expected_version gates every write: a bare repeat with the same
	// (now-stale) version conflict-fails with no further mutation.
	target.registerTool(server, groupIssues, tool("update_issue", "Patch one issue using its current version for optimistic concurrency.", schemaUpdateIssue(), schemaUpdateOutput(), toolHints(false, true, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[updateIssueInput, any](target, t, (*adapter).updateIssue))
	})
	target.registerTool(server, groupIssues, tool("get_issue", "Get the current issue record by ULID or ISSUE-N display ID.", schemaGetIssue(), schemaGetIssueOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[getIssueInput, any](target, t, (*adapter).getIssue))
	})
	target.registerTool(server, groupIssues, tool("list_issues", "List and filter issues, including effective status, blockers, and claimability.", schemaListIssues(), schemaIssueListOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[listIssuesInput, any](target, t, (*adapter).listIssues))
	})
	target.registerTool(server, groupIssues, tool("archive_issue", "Archive one issue using its current version; history remains available.", schemaArchiveIssue(), schemaArchiveIssueOutput(), toolHints(false, true, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[archiveIssueInput, any](target, t, (*adapter).archiveIssue))
	})
	target.registerTool(server, groupIssues, tool("unarchive_issue", "Restore one archived issue to its default visible state using its current version; history remains available.", schemaUnarchiveIssue(), schemaUnarchiveIssueOutput(), toolHints(false, true, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[unarchiveIssueInput, any](target, t, (*adapter).unarchiveIssue))
	})
	target.registerTool(server, groupReview, tool("cancel_review_request", "Cancel an open or claimed review request using its current version.", schemaCancelReviewRequest(), schemaReviewRequestOutput(), toolHints(false, true, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[cancelReviewRequestInput, any](target, t, (*adapter).cancelReviewRequest))
	})
	// create_review_request opens the first request for a target; it is the
	// only path from "no request" to `open` (docs/09 state transitions), and
	// the only one available once a predecessor is terminal, which
	// replace_review_request rejects with REVIEW_REQUEST_NOT_REPLACEABLE. It
	// carries no supersedes_id -- superseding a live predecessor is
	// replace_review_request's whole job -- and no idempotency_key: the
	// repository already replays an identical duplicate against the same
	// target and rejects a differing one with REVIEW_ALREADY_EXISTS, so
	// creation is content-idempotent while that request is still live.
	// idempotentHint is still false, though: the replay gate is the target's
	// open-or-claimed request, which a cancel resolves without changing the
	// issue, so a bare repeat after that opens a second request rather than
	// replaying -- short of the unconditional guarantee section 4 requires.
	target.registerTool(server, groupReview, tool("create_review_request", "Open a review request freezing an exact issue version, event position, artifact set, and purposes.", schemaCreateReviewRequest(), schemaReviewRequestOutput(), toolHints(false, false, false, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[createReviewRequestInput, any](target, t, (*adapter).createReviewRequest))
	})
	target.registerTool(server, groupReview, tool("get_review_request", "Get one review request by identifier.", schemaGetReviewRequest(), schemaReviewRequestOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[getReviewRequestInput, any](target, t, (*adapter).getReviewRequest))
	})
	target.registerTool(server, groupReview, tool("list_review_requests", "List review requests with optional status and claimability filters.", schemaListReviewRequests(), schemaReviewRequestListOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[listReviewRequestsInput, any](target, t, (*adapter).listReviewRequests))
	})
	// add/remove are each gated (unique constraint on add, not-found on
	// remove), so a bare repeat has no additional effect; remove can destroy
	// an existing relation.
	// submit_gate_evidence is groupLifecycle, not groupGovernance: it is how an
	// agent satisfies a gate, so the agent profile must have it. It is
	// lease-authenticated and an idempotent upsert on (attempt, key).
	target.registerTool(server, groupLifecycle, tool("submit_gate_evidence", "Record lease-authenticated evidence satisfying one gate requirement on an active attempt.", schemaSubmitGateEvidence(), schemaSubmitGateEvidenceOutput(), toolHints(false, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[submitGateEvidenceInput, any](target, t, (*adapter).submitGateEvidence))
	})
	// Workflow policy administration is groupGovernance: excluded from the
	// agent profile so an agent cannot rewrite or archive the gates that
	// constrain it. create/update/archive share one action-dispatched tool the
	// way manage_issue_relation does. Not idempotent: create with no
	// idempotency_key makes a new policy each call, and archive is
	// destructive in the sense that it retires a policy irreversibly.
	target.registerTool(server, groupGovernance, tool("manage_workflow_policy", "Create, update, or archive one workflow policy defining quality gates.", schemaManageWorkflowPolicy(), schemaWorkflowPolicyOutput(), toolHints(false, true, false, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[manageWorkflowPolicyInput, any](target, t, (*adapter).manageWorkflowPolicy))
	})
	// The two policy reads are groupGovernance too, but readOnlyHint puts them
	// in the read-only profile: read-only membership is decided before the
	// group check, so an inspector sees the policy set without being able to
	// change it.
	target.registerTool(server, groupGovernance, tool("get_workflow_policy", "Read one workflow policy; compact omits requirement bodies.", schemaGetWorkflowPolicy(), schemaWorkflowPolicyOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[getWorkflowPolicyInput, any](target, t, (*adapter).getWorkflowPolicy))
	})
	target.registerTool(server, groupGovernance, tool("list_workflow_policies", "List workflow policies as compact summaries without requirement bodies.", schemaListWorkflowPolicies(), schemaWorkflowPolicyListOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[listWorkflowPoliciesInput, any](target, t, (*adapter).listWorkflowPolicies))
	})
	// evaluate_gates is groupLifecycle, not groupGovernance: an agent needs to
	// see why its own gate failed, and this cannot change anything. It has no
	// authority to transition state -- it reports what an enforcement point
	// would decide, using the same evaluator the mutation path uses.
	target.registerTool(server, groupLifecycle, tool("evaluate_gates", "Report what a workflow gate would decide at one enforcement point, without changing anything.", schemaEvaluateGates(), schemaEvaluateGatesOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[evaluateGatesInput, any](target, t, (*adapter).evaluateGates))
	})
	target.registerTool(server, groupIssues, tool("manage_issue_relation", "Add or remove one blocks, related_to, or duplicates relation.", schemaManageIssueRelation(), schemaManageIssueRelationOutput(), toolHints(false, true, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[manageIssueRelationInput, any](target, t, (*adapter).manageIssueRelation))
	})
	// idempotency_key is required (not optional) and the repository replays
	// the original result for a repeated key, so idempotentHint is
	// genuinely true. A claimed predecessor is rejected (CodeReviewRequestClaimed)
	// rather than replaced, since this operation does not hold the attempt's
	// lease token.
	target.registerTool(server, groupReview, tool("replace_review_request", "Atomically supersede a predecessor review request and create its open successor in one transaction.", schemaReplaceReviewRequest(), schemaReplaceReviewRequestOutput(), toolHints(false, true, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[replaceReviewRequestInput, any](target, t, (*adapter).replaceReviewRequest))
	})
	target.registerTool(server, groupIssues, tool("get_issue_graph", "Get a bounded relation and hierarchy graph around one issue.", schemaGetIssueGraph(), schemaGraphOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[getIssueGraphInput, any](target, t, (*adapter).getIssueGraph))
	})
	target.registerTool(server, groupIssues, tool("get_planning_graph", "Get dependency-aware entry points and blocking nodes for work selection.", schemaGetPlanningGraph(), schemaGraphOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[getPlanningGraphInput, any](target, t, (*adapter).getPlanningGraph))
	})
	target.registerTool(server, groupPlanning, tool("validate_issue_plan", "Normalize and validate a bounded multi-issue plan without writing it.", schemaValidateIssuePlan(), schemaPlanValidationOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[issuePlanInput, any](target, t, (*adapter).validateIssuePlan))
	})
	// idempotency_key is required (not optional) for apply_issue_plan and
	// the repository replays the original result for a repeated key, so
	// idempotentHint is genuinely true for the advertised contract.
	target.registerTool(server, groupPlanning, tool("apply_issue_plan", "Atomically create issues, relations, and decisions from a valid plan.", schemaApplyIssuePlan(), schemaApplyIssuePlanOutput(), toolHints(false, true, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[applyIssuePlanInput, any](target, t, (*adapter).applyIssuePlan))
	})
	// add_comment's idempotency_key is optional: a bare repeat without it
	// appends a second comment.
	target.registerTool(server, groupKnowledge, tool("add_comment", "Append collaboration context to an issue without rewriting history.", schemaAddComment(), schemaAddCommentOutput(), toolHints(false, false, false, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[addCommentInput, any](target, t, (*adapter).addComment))
	})
	// record_decision has no idempotency_key at all (a bare repeat always
	// appends a new decision), and an optional supersedes_id overwrites the
	// predecessor decision's status in the same transaction.
	target.registerTool(server, groupKnowledge, tool("record_decision", "Append a durable project or issue decision, optionally superseding one.", schemaRecordDecision(), schemaRecordDecisionOutput(), toolHints(false, true, false, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[recordDecisionInput, any](target, t, (*adapter).recordDecision))
	})
	target.registerTool(server, groupKnowledge, tool("list_decisions", "List project-wide or issue-scoped decisions with cursor pagination.", schemaListDecisions(), schemaDecisionListOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[listDecisionsInput, any](target, t, (*adapter).listDecisions))
	})
	target.registerTool(server, groupKnowledge, tool("get_issue_activity", "Get a unified newest-first timeline of issue work and artifacts.", schemaGetIssueActivity(), schemaGetIssueActivityOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[getIssueActivityInput, any](target, t, (*adapter).getIssueActivity))
	})
	// claimability gates every claim: once claimed, a bare repeat fails with
	// no further effect. Claiming does not destroy prior state.
	target.registerTool(server, groupLifecycle, tool("claim_issue", "Atomically acquire exclusive ready/review work for a 60-3600s renewable lease; already claimed work fails; keyed retries replay.", schemaClaimIssue(), schemaClaimIssueOutput(), toolHints(false, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[claimIssueInput, any](target, t, (*adapter).claimIssue))
	})
	// unlike claim/finish, renew_attempt has no gate: each repeat pushes the
	// lease expiry further out, a genuine additional effect every call.
	target.registerTool(server, groupLifecycle, tool("renew_attempt", "Extend an active work or review lease before it expires.", schemaRenewAttempt(), schemaRenewAttemptOutput(), toolHints(false, false, false, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[renewAttemptInput, any](target, t, (*adapter).renewAttempt))
	})
	target.registerTool(server, groupLifecycle, tool("save_attempt_note", "Append a progress, finding, warning, or checkpoint note to a leased attempt; checkpoints seed successor recovery via get_work_context; keyed retries replay.", schemaSaveAttemptNote(), schemaSaveAttemptNoteOutput(), toolHints(false, false, false, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[saveAttemptNoteInput, any](target, t, (*adapter).saveAttemptNote))
	})
	// finish_attempt is lease-gated (repository requires status = 'active'),
	// so a bare repeat after the first success fails with no further
	// mutation; it can overwrite the issue's status/blocked_reason.
	target.registerTool(server, groupLifecycle, tool("finish_attempt", "End a leased attempt with outcome, verification, and status. Kind determines required fields: set at claim_issue time.", schemaFinishAttempt(), schemaFinishAttemptOutput(), toolHints(false, true, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[finishAttemptInput, any](target, t, (*adapter).finishAttempt))
	})
	target.registerTool(server, groupLifecycle, tool("get_work_context", "Get bounded task, blocker, decision, checkpoint, and recovery context.", schemaGetWorkContext(), schemaGetWorkContextOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[getWorkContextInput, any](target, t, (*adapter).getWorkContext))
	})
	// reserve_resources is all-or-nothing but not itself claimability-gated;
	// a bare repeat with the same resources is a genuine second acquisition
	// attempt (which conflicts with the first), not a no-op.
	target.registerTool(server, groupLifecycle, tool("reserve_resources", "Add a bounded set of resources to an active work attempt's reservations, all-or-nothing.", schemaReserveResources(), schemaReserveResourcesOutput(), toolHints(false, false, false, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[reserveResourcesInput, any](target, t, (*adapter).reserveResources))
	})
	target.registerTool(server, groupLifecycle, tool("release_resources", "Release reservations owned by an active work attempt; empty reservation_ids releases every active reservation it owns.", schemaReleaseResources(), schemaReleaseResourcesOutput(), toolHints(false, true, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[releaseResourcesInput, any](target, t, (*adapter).releaseResources))
	})
	target.registerTool(server, groupLifecycle, tool("list_resource_reservations", "List resource reservations filtered by issue, attempt, kind, and active state, with cursor pagination.", schemaListResourceReservations(), schemaReservationListOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[listResourceReservationsInput, any](target, t, (*adapter).listResourceReservations))
	})
	target.registerTool(server, groupLifecycle, tool("get_resource_reservation", "Get one resource reservation by id.", schemaGetResourceReservation(), schemaGetResourceReservationOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[getResourceReservationInput, any](target, t, (*adapter).getResourceReservation))
	})
	target.registerTool(server, groupKnowledge, tool("search", "Full-text search with cursor pagination; default limit 20; archived records are excluded unless requested; results are relevance ordered.", schemaSearch(), schemaSearchOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[searchInput, any](target, t, (*adapter).search))
	})
	target.registerTool(server, groupSync, tool("get_changes", "Get ordered issue events after an event ID for incremental synchronization.", schemaGetChanges(), schemaChangesOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[getChangesInput, any](target, t, (*adapter).getChanges))
	})
}

func (adapter *adapter) createAgentSession(ctx context.Context, request *sdkmcp.CallToolRequest, input createAgentSessionInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.services.SessionService.CreateWithHandle(ctx, domain.CreateAgentSessionInput{
		ClientName: input.ClientName, ClientVersion: input.ClientVersion, AgentLabel: input.AgentLabel,
		Model: input.Model, InstanceKey: input.InstanceKey,
	})
	if err != nil {
		return adapter.failure(err)
	}
	summary := fmt.Sprintf("agent session created (session_id=%s)", result.Session.ID)
	return adapter.success(createAgentSessionOutput{Session: sessionDTOFromDomain(result.Session), AgentSessionHandle: result.Handle}, summary)
}

func (adapter *adapter) endAgentSession(ctx context.Context, request *sdkmcp.CallToolRequest, input endAgentSessionInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.services.SessionService.EndWithHandle(ctx, input.AgentSessionHandle)
	if err != nil {
		return adapter.failure(err)
	}
	summary := fmt.Sprintf("agent session ended (session_id=%s)", result.ID)
	return adapter.success(endAgentSessionOutput{Session: sessionDTOFromDomain(result)}, summary)
}

func (adapter *adapter) search(ctx context.Context, request *sdkmcp.CallToolRequest, input searchInput) (*sdkmcp.CallToolResult, any, error) {
	entityTypes := make([]domain.SearchEntityType, len(input.EntityTypes))
	for index, value := range input.EntityTypes {
		entityTypes[index] = domain.SearchEntityType(value)
	}
	result, err := adapter.services.SearchService.Search(ctx, domain.SearchInput{
		Query: input.Query, EntityTypes: entityTypes, IssueID: input.IssueID, EpicID: input.EpicID,
		Statuses: stringsToStatuses(input.Statuses), Labels: input.Labels, IncludeArchived: input.IncludeArchived,
		Limit: input.Limit, Cursor: stringValue(input.Cursor), SnippetLength: input.SnippetLength,
	})
	if err != nil {
		return adapter.failure(err)
	}
	return adapter.success(searchOutputFromDomain(result), fmt.Sprintf("search results returned (count=%d, has_more=%t)", len(result.Results), result.HasMore))
}

func (adapter *adapter) getChanges(ctx context.Context, request *sdkmcp.CallToolRequest, input getChangesInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.services.SearchService.GetChanges(ctx, domain.GetChangesInput{
		SinceEventID: input.SinceEventID, IssueID: input.IssueID, EventTypes: input.EventTypes, Limit: input.Limit,
	})
	if err != nil {
		return adapter.failure(err)
	}
	return adapter.success(changesOutputFromDomain(result), fmt.Sprintf("changes returned (events=%d, latest_event_id=%d, has_more=%t)", len(result.Events), result.LatestEventID, result.HasMore))
}

func (adapter *adapter) getWorkContext(ctx context.Context, request *sdkmcp.CallToolRequest, input getWorkContextInput) (*sdkmcp.CallToolResult, any, error) {
	include := make([]domain.WorkContextInclude, len(input.Include))
	for index, value := range input.Include {
		include[index] = domain.WorkContextInclude(value)
	}
	limits := make(map[domain.WorkContextInclude]int)
	if input.Limits != nil {
		if input.Limits.RelatedIssueSummaries != nil {
			limits[domain.WorkContextIncludeRelatedIssueSummaries] = *input.Limits.RelatedIssueSummaries
		}
		if input.Limits.RecentComments != nil {
			limits[domain.WorkContextIncludeRecentComments] = *input.Limits.RecentComments
		}
		if input.Limits.RecentAttemptNotes != nil {
			limits[domain.WorkContextIncludeRecentAttemptNotes] = *input.Limits.RecentAttemptNotes
		}
		if input.Limits.DecisionContent != nil {
			limits[domain.WorkContextIncludeDecisionContent] = *input.Limits.DecisionContent
		}
		if input.Limits.AttemptHistory != nil {
			limits[domain.WorkContextIncludeAttemptHistory] = *input.Limits.AttemptHistory
		}
		if input.Limits.Artifacts != nil {
			limits[domain.WorkContextIncludeArtifacts] = *input.Limits.Artifacts
		}
		if input.Limits.ChangesSincePreviousAttempt != nil {
			limits[domain.WorkContextIncludeChangesSincePreviousAttempt] = *input.Limits.ChangesSincePreviousAttempt
		}
		if input.Limits.ResourceReservations != nil {
			limits[domain.WorkContextIncludeResourceReservations] = *input.Limits.ResourceReservations
		}
		if input.Limits.ReservationConflicts != nil {
			limits[domain.WorkContextIncludeReservationConflicts] = *input.Limits.ReservationConflicts
		}
	}
	result, err := adapter.services.WorkContextService.GetWorkContext(ctx, domain.GetWorkContextInput{
		IssueID: input.IssueID, Include: include, Limits: limits, DesiredResources: resourcesFromInput(input.DesiredResources),
	})
	if err != nil {
		return adapter.failure(err)
	}
	output := workContextOutputFromDomain(result)
	output.NextActions = []string{"Call claim_issue when the issue is claimable."}
	summary := fmt.Sprintf("work context returned (display_id=%s, id=%s, effective_status=%s)",
		output.Issue.DisplayID, output.Issue.ID, output.Issue.EffectiveStatus)
	return adapter.success(output, summary)
}

func (adapter *adapter) claimIssue(ctx context.Context, request *sdkmcp.CallToolRequest, input claimIssueInput) (*sdkmcp.CallToolResult, any, error) {
	view, err := resolveView(input.View, "compact", "compact", "full")
	if err != nil {
		return adapter.failure(err)
	}
	sessionID := adapter.sessionIDForRequest(ctx, request)
	result, err := adapter.services.AttemptService.ClaimIssue(ctx, domain.ClaimIssueInput{
		IssueID: input.IssueID, LeaseSeconds: input.LeaseSeconds, SessionID: sessionID,
		Resources: resourcesFromInput(input.Resources), IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return adapter.failure(err)
	}
	summary := fmt.Sprintf("issue claimed (display_id=%s, id=%s, attempt_id=%s)",
		result.Issue.DisplayID, result.Issue.ID, result.Attempt.ID)
	if view == "full" {
		attempt := attemptDTOFromDomain(result.Attempt)
		var reservations []reservationDTO
		if len(result.Reservations) > 0 {
			reservations = reservationDTOsFromDomain(result.Reservations)
		}
		return adapter.success(claimIssueOutput{
			Issue: issueListItemDTO{
				issueDTO:               issueDTOFromDomain(result.Projection.Issue),
				EffectiveStatus:        string(result.Projection.EffectiveStatus),
				UnresolvedBlockerCount: result.Projection.UnresolvedBlockerCount,
				IsBlocked:              result.Projection.IsBlocked,
				IsClaimable:            result.Projection.IsClaimable,
				ActiveAttemptID:        result.Projection.ActiveAttemptID,
			},
			Attempt: attempt, Reservations: reservations, LeaseToken: result.LeaseToken, LeaseExpiresAt: result.Attempt.LeaseExpiresAt,
			MinimalWorkContext: emptyWorkContextDTO{}, Warnings: []string{},
			NextActions: []string{"Renew before expiry; finish_attempt on every exit."},
		}, summary)
	}
	return adapter.success(claimIssueCompactOutputFromDomain(result.Issue, result.Attempt, result.Reservations, result.LeaseToken), summary)
}

func (adapter *adapter) renewAttempt(ctx context.Context, request *sdkmcp.CallToolRequest, input renewAttemptInput) (*sdkmcp.CallToolResult, any, error) {
	sessionID := adapter.sessionIDForRequest(ctx, request)
	result, err := adapter.services.AttemptService.RenewAttempt(ctx, domain.RenewAttemptInput{
		AttemptID: input.AttemptID, LeaseToken: input.LeaseToken, LeaseSeconds: input.LeaseSeconds, SessionID: sessionID,
	})
	if err != nil {
		return adapter.failure(err)
	}
	return adapter.success(renewAttemptOutput{
		LeaseExpiresAt: result.LeaseExpiresAt, ServerTime: result.ServerTime,
		NextActions: []string{"Continue work; checkpoint or finish before expiry."},
	}, fmt.Sprintf("attempt lease renewed (attempt_id=%s)", input.AttemptID))
}

func (adapter *adapter) saveAttemptNote(ctx context.Context, request *sdkmcp.CallToolRequest, input saveAttemptNoteInput) (*sdkmcp.CallToolResult, any, error) {
	sessionID := adapter.sessionIDForRequest(ctx, request)
	artifacts := make([]domain.ArtifactInput, len(input.Artifacts))
	for index, artifact := range input.Artifacts {
		artifacts[index] = domain.ArtifactInput{
			Type: domain.ArtifactType(artifact.Type), URI: artifact.URI,
			Title: copyString(artifact.Title), Metadata: append([]byte(nil), artifact.Metadata...),
		}
	}
	result, err := adapter.services.AttemptService.SaveAttemptNote(ctx, domain.SaveAttemptNoteInput{
		AttemptID: input.AttemptID, LeaseToken: input.LeaseToken, Kind: domain.AttemptNoteKind(input.Kind),
		SessionID: sessionID, Content: input.Content, NextSteps: input.NextSteps, Important: input.Important, Artifacts: artifacts,
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return adapter.failure(err)
	}
	outputArtifacts := make([]artifactDTO, len(result.Artifacts))
	for index, artifact := range result.Artifacts {
		outputArtifacts[index] = artifactDTOFromDomain(artifact)
	}
	return adapter.success(saveAttemptNoteOutput{
		AttemptNote: attemptNoteDTOFromDomain(result.Note), Artifacts: outputArtifacts,
		NextActions: []string{"Continue work or call finish_attempt."},
	}, fmt.Sprintf("attempt note saved (note_id=%s, kind=%s)", result.Note.ID, result.Note.Kind))
}

func (adapter *adapter) finishAttempt(ctx context.Context, request *sdkmcp.CallToolRequest, input finishAttemptInput) (*sdkmcp.CallToolResult, any, error) {
	view, err := resolveView(input.View, "compact", "compact", "full")
	if err != nil {
		return adapter.failure(err)
	}
	sessionID := adapter.sessionIDForRequest(ctx, request)
	artifacts := make([]domain.ArtifactInput, len(input.Artifacts))
	for index, artifact := range input.Artifacts {
		artifacts[index] = domain.ArtifactInput{
			Type: domain.ArtifactType(artifact.Type), URI: artifact.URI,
			Title: copyString(artifact.Title), Metadata: append([]byte(nil), artifact.Metadata...),
		}
	}
	var acknowledgement *domain.AttemptAcknowledgement
	if input.AcknowledgedChanges != nil {
		acknowledgement = &domain.AttemptAcknowledgement{IssueVersion: input.AcknowledgedChanges.IssueVersion, LatestEventID: input.AcknowledgedChanges.LatestEventID}
	}
	result, err := adapter.services.AttemptService.FinishAttempt(ctx, domain.FinishAttemptInput{
		AttemptID: input.AttemptID, LeaseToken: input.LeaseToken, Outcome: domain.AttemptOutcome(input.Outcome),
		SessionID: sessionID, ResultSummary: input.ResultSummary, NextSteps: input.NextSteps, Verification: input.Verification,
		TargetIssueStatus: statusPointer(input.TargetIssueStatus), BlockedReason: input.BlockedReason,
		ReviewOutcome: reviewPointer(input.ReviewOutcome), FailureReasonCode: failurePointer(input.FailureReasonCode),
		InterruptionReasonCode: interruptionPointer(input.InterruptionReasonCode), ReasonDetails: input.ReasonDetails,
		AcknowledgedChanges: acknowledgement, Artifacts: artifacts, IdempotencyKey: copyString(input.IdempotencyKey),
	})
	if err != nil {
		return adapter.failure(err)
	}
	summary := fmt.Sprintf("attempt finished (attempt_id=%s, status=%s, display_id=%s, issue_status=%s, latest_event_id=%d)",
		result.Attempt.ID, result.Attempt.Status, result.Issue.DisplayID, result.Issue.Status, result.LatestEventID)
	if view == "full" {
		outputArtifacts := make([]artifactDTO, len(result.Artifacts))
		for index, artifact := range result.Artifacts {
			outputArtifacts[index] = artifactDTOFromDomain(artifact)
		}
		return adapter.success(finishAttemptOutput{Attempt: attemptDTOFromDomain(result.Attempt), Issue: issueDTOFromDomain(result.Issue),
			Warnings: append([]string{}, result.Warnings...), LatestEventID: result.LatestEventID, Artifacts: outputArtifacts,
			NextActions: []string{"Select new work from get_planning_graph."}}, summary)
	}
	return adapter.success(finishAttemptCompactOutputFromDomain(result.Attempt, result.Issue, result.Warnings, result.LatestEventID, result.Artifacts, []string{"Select new work from get_planning_graph."}), summary)
}

func (adapter *adapter) reserveResources(ctx context.Context, request *sdkmcp.CallToolRequest, input reserveResourcesInput) (*sdkmcp.CallToolResult, any, error) {
	sessionID := adapter.sessionIDForRequest(ctx, request)
	result, err := adapter.services.AttemptService.ReserveResources(ctx, domain.ReserveResourcesInput{
		AttemptID: input.AttemptID, LeaseToken: input.LeaseToken, SessionID: sessionID,
		Resources: resourcesFromInput(input.Resources), IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return adapter.failure(err)
	}
	return adapter.success(reserveResourcesOutput{
		Reservations: reservationDTOsFromDomain(result.Reservations),
		NextActions:  []string{"Continue work; release_resources when the reservation is no longer needed."},
	}, fmt.Sprintf("resources reserved (count=%d)", len(result.Reservations)))
}

func (adapter *adapter) releaseResources(ctx context.Context, request *sdkmcp.CallToolRequest, input releaseResourcesInput) (*sdkmcp.CallToolResult, any, error) {
	sessionID := adapter.sessionIDForRequest(ctx, request)
	result, err := adapter.services.AttemptService.ReleaseResources(ctx, domain.ReleaseResourcesInput{
		AttemptID: input.AttemptID, LeaseToken: input.LeaseToken, SessionID: sessionID,
		ReservationIDs: input.ReservationIDs, IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return adapter.failure(err)
	}
	return adapter.success(releaseResourcesOutput{
		Reservations: reservationDTOsFromDomain(result.Reservations),
		NextActions:  []string{"Continue work or call finish_attempt."},
	}, fmt.Sprintf("resources released (count=%d)", len(result.Reservations)))
}

func (adapter *adapter) listResourceReservations(ctx context.Context, request *sdkmcp.CallToolRequest, input listResourceReservationsInput) (*sdkmcp.CallToolResult, any, error) {
	var kind *domain.ResourceKind
	if input.Kind != nil {
		value := domain.ResourceKind(*input.Kind)
		kind = &value
	}
	cursor := ""
	if input.Cursor != nil {
		cursor = *input.Cursor
	}
	result, err := adapter.services.ReservationService.ListReservations(ctx, domain.ListResourceReservationsInput{
		IssueID: input.IssueID, AttemptID: input.AttemptID, Kind: kind, Active: input.Active,
		Limit: input.Limit, Cursor: cursor,
	})
	if err != nil {
		return adapter.failure(err)
	}
	return adapter.success(reservationListOutput{
		Items: reservationDTOsFromDomain(result.Items), NextCursor: result.NextCursor, HasMore: result.HasMore,
		NextActions: []string{"Use next_cursor for more results, if present."},
	}, fmt.Sprintf("reservations listed (count=%d, has_more=%t)", len(result.Items), result.HasMore))
}

func (adapter *adapter) getResourceReservation(ctx context.Context, request *sdkmcp.CallToolRequest, input getResourceReservationInput) (*sdkmcp.CallToolResult, any, error) {
	if input.View != "" && input.View != "compact" && input.View != "full" {
		return adapter.failure(unsupportedField("view"))
	}
	reservation, err := adapter.services.ReservationService.GetReservation(ctx, input.ReservationID)
	if err != nil {
		return adapter.failure(err)
	}
	return adapter.success(reservationDTOFromDomain(reservation, input.View == "full"), fmt.Sprintf("reservation retrieved (reservation_id=%s)", reservation.ID))
}

func (adapter *adapter) sessionIDForRequest(ctx context.Context, request *sdkmcp.CallToolRequest) *string {
	if ctx == nil || request == nil {
		return nil
	}
	value := ctx.Value(agentSessionContextKey{})
	if value == nil {
		return nil
	}
	sessionID, ok := value.(string)
	if !ok || sessionID == "" {
		return nil
	}
	copy := sessionID
	return &copy
}

func statusPointer(value *string) *domain.Status {
	if value == nil {
		return nil
	}
	result := domain.Status(*value)
	return &result
}
func reviewPointer(value *string) *domain.ReviewOutcome {
	if value == nil {
		return nil
	}
	result := domain.ReviewOutcome(*value)
	return &result
}
func failurePointer(value *string) *domain.FailureReasonCode {
	if value == nil {
		return nil
	}
	result := domain.FailureReasonCode(*value)
	return &result
}
func interruptionPointer(value *string) *domain.InterruptionReasonCode {
	if value == nil {
		return nil
	}
	result := domain.InterruptionReasonCode(*value)
	return &result
}

func (adapter *adapter) validateIssuePlan(ctx context.Context, request *sdkmcp.CallToolRequest, input issuePlanInput) (*sdkmcp.CallToolResult, any, error) {
	plan := input.domainPlan()
	validation, err := adapter.services.PlanningService.ValidateIssuePlan(ctx, plan)
	if err != nil {
		return adapter.failure(err)
	}
	output, err := planValidationOutputFromDomain(validation, plan, input.IncludeNormalizedPlan)
	if err != nil {
		return adapter.failure(domain.WrapError(err, domain.CodeStorageFailure, "normalized issue plan could not be encoded", false))
	}
	if output.Valid {
		output.NextActions = []string{"Request include_normalized_plan when normalized fields are needed for apply_issue_plan."}
	} else {
		output.NextActions = []string{"Correct errors and validate again."}
	}
	return adapter.success(output, fmt.Sprintf("issue plan validated (valid=%t, plan_fingerprint=%s)", output.Valid, output.PlanFingerprint))
}

func (adapter *adapter) applyIssuePlan(ctx context.Context, request *sdkmcp.CallToolRequest, input applyIssuePlanInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.services.PlanningService.ApplyIssuePlan(ctx, input.domainPlan(), input.IdempotencyKey)
	if err != nil {
		return adapter.failure(err)
	}
	output := applyIssuePlanOutputFromApplication(result)
	output.NextActions = []string{"Use get_planning_graph to select executable work."}
	return adapter.success(output, fmt.Sprintf("issue plan applied (created_issues=%d, latest_event_id=%d)", len(output.CreatedIssues), output.LatestEventID))
}

func (adapter *adapter) addComment(ctx context.Context, request *sdkmcp.CallToolRequest, input addCommentInput) (*sdkmcp.CallToolResult, any, error) {
	comment, err := adapter.services.CommentService.AddComment(ctx, domain.AddCommentInput{
		IssueID: input.IssueID, Content: input.Content, SessionID: adapter.sessionIDForRequest(ctx, request),
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return adapter.failure(err)
	}
	return adapter.success(addCommentOutput{Comment: commentDTOFromDomain(comment)}, fmt.Sprintf("comment added (comment_id=%s)", comment.ID))
}

func (adapter *adapter) recordDecision(ctx context.Context, request *sdkmcp.CallToolRequest, input recordDecisionInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.services.DecisionService.RecordDecision(ctx, domain.RecordDecisionInput{
		IssueID: input.IssueID, Title: input.Title, Summary: input.Summary, Content: input.Content,
		Status: domain.DecisionStatus(input.Status), SupersedesID: input.SupersedesID,
		SessionID: adapter.sessionIDForRequest(ctx, request),
	})
	if err != nil {
		return adapter.failure(err)
	}
	return adapter.success(recordDecisionOutput{
		Decision:             recordDecisionDTOFromDomain(result.Decision),
		SupersededDecisionID: copyString(result.SupersededDecisionID),
	}, fmt.Sprintf("decision recorded (decision_id=%s, status=%s)", result.Decision.ID, result.Decision.Status))
}

func (adapter *adapter) listDecisions(ctx context.Context, request *sdkmcp.CallToolRequest, input listDecisionsInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.services.DecisionService.ListDecisions(ctx, domain.ListDecisionsInput{
		IssueID: input.IssueID, Limit: input.Limit, Cursor: stringValue(input.Cursor),
	})
	if err != nil {
		return adapter.failure(err)
	}
	return adapter.success(decisionListOutputFromDomain(result), fmt.Sprintf("decisions listed (count=%d, has_more=%t)", len(result.Items), result.HasMore))
}

func (adapter *adapter) getIssueActivity(ctx context.Context, request *sdkmcp.CallToolRequest, input getIssueActivityInput) (*sdkmcp.CallToolResult, any, error) {
	activity, err := adapter.services.ActivityService.GetIssueActivity(ctx, getIssueActivityInputToDomain(input))
	if err != nil {
		return adapter.failure(err)
	}
	return adapter.success(issueActivityOutputFromDomain(activity), fmt.Sprintf("issue activity returned (items=%d, has_more=%t)", len(activity.Items), activity.HasMore))
}

func (adapter *adapter) getIssueGraph(ctx context.Context, request *sdkmcp.CallToolRequest, input getIssueGraphInput) (*sdkmcp.CallToolResult, any, error) {
	relationTypes := make([]domain.RelationType, len(input.RelationTypes))
	for index, relationType := range input.RelationTypes {
		relationTypes[index] = domain.RelationType(relationType)
	}
	graph, err := adapter.services.GraphService.GetIssueGraph(ctx, domain.GetIssueGraphInput{
		RootIssueID: input.RootIssueID, Depth: input.Depth, Direction: domain.GraphDirection(input.Direction),
		RelationTypes: relationTypes, IncludeHierarchy: input.IncludeHierarchy, IncludeTerminal: input.IncludeTerminal,
		MaxNodes: input.MaxNodes, View: input.View,
	})
	if err != nil {
		return adapter.failure(err)
	}
	output := graphOutputFromDomain(graph)
	output.NextActions = []string{"Inspect a node with get_work_context."}
	return adapter.success(output, fmt.Sprintf("issue graph returned (nodes=%d, edges=%d, truncated=%t)", len(graph.Nodes), len(graph.Edges), graph.Truncated))
}

func (adapter *adapter) getPlanningGraph(ctx context.Context, request *sdkmcp.CallToolRequest, input getPlanningGraphInput) (*sdkmcp.CallToolResult, any, error) {
	graph, err := adapter.services.GraphService.GetPlanningGraph(ctx, domain.GetPlanningGraphInput{
		RootIssueID: input.RootIssueID, Depth: input.Depth, MaxNodes: input.MaxNodes,
		IncludeReview: input.IncludeReview, IncludeRelated: input.IncludeRelated,
		IncludeTerminal: input.IncludeTerminal,
	})
	if err != nil {
		return adapter.failure(err)
	}
	output := graphOutputFromDomain(graph)
	output.NextActions = []string{"Inspect an entry point with get_work_context."}
	return adapter.success(output, fmt.Sprintf("planning graph returned (nodes=%d, entry_points=%d, truncated=%t)", len(graph.Nodes), len(graph.EntryPoints), graph.Truncated))
}

func (adapter *adapter) exportProject(ctx context.Context, request *sdkmcp.CallToolRequest, input exportProjectInput) (*sdkmcp.CallToolResult, any, error) {
	if input.Delivery != "" && input.Delivery != "artifact" && input.Delivery != "inline" {
		return adapter.failure(domain.NewError(domain.CodeInvalidArgument, "delivery must be artifact or inline", false, domain.Detail{Field: "delivery", Code: "INVALID_ENUM"}))
	}
	data, err := adapter.services.ProjectService.ExportLogicalProject(ctx)
	if err != nil {
		return adapter.failure(err)
	}
	if input.Delivery == "inline" {
		if len(data) > maxInlineExportBytes {
			return adapter.failure(domain.NewError(domain.CodeLimitExceeded, "inline export exceeds the maximum size of 65536 bytes", false, domain.Detail{Field: "delivery", Code: "MAX_INLINE_EXPORT_BYTES"}))
		}
		var document domain.LogicalProjectDocument
		if err := json.Unmarshal(data, &document); err != nil {
			return adapter.failure(domain.WrapError(err, domain.CodeStorageFailure, "logical project export could not be decoded", false))
		}
		return adapter.success(document, "project export returned")
	}
	artifact, err := adapter.exports.write(data)
	if err != nil {
		return adapter.failure(domain.WrapError(err, domain.CodeStorageFailure, "logical project export artifact could not be written", false))
	}
	var document domain.LogicalProjectDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return adapter.failure(domain.WrapError(err, domain.CodeStorageFailure, "logical project export could not be decoded", false))
	}
	artifact.Format, artifact.Version, artifact.ExportedAt = document.Format, document.Version, document.ExportedAt
	return adapter.success(artifact, fmt.Sprintf("project export artifact returned (artifact_uri=%s, byte_count=%d)", artifact.ArtifactURI, artifact.ByteCount))
}

func (adapter *adapter) validateImport(ctx context.Context, request *sdkmcp.CallToolRequest, input validateImportInput) (*sdkmcp.CallToolResult, any, error) {
	document, err := adapter.importDocument(input.Document, input.SourceURI)
	if err != nil {
		return adapter.failure(err)
	}
	dryRun, err := adapter.services.ProjectService.ValidateLogicalProjectImport(ctx, document)
	if err != nil {
		return adapter.failure(err)
	}
	return adapter.success(dryRun, "import validation dry run returned")
}

func (adapter *adapter) applyImport(ctx context.Context, request *sdkmcp.CallToolRequest, input applyImportInput) (*sdkmcp.CallToolResult, any, error) {
	document, err := adapter.importDocument(input.Document, input.SourceURI)
	if err != nil {
		return adapter.failure(err)
	}
	result, err := adapter.services.ProjectService.ApplyLogicalProjectImport(ctx, document)
	if err != nil {
		return adapter.failure(err)
	}
	return adapter.success(result, "import apply result returned")
}

func (adapter *adapter) importDocument(document *string, sourceURI *string) ([]byte, error) {
	if (document == nil) == (sourceURI == nil) {
		return nil, domain.NewError(domain.CodeInvalidArgument, "exactly one of document or source_uri is required", false,
			domain.Detail{Field: "document", Code: "EXACTLY_ONE_SOURCE_REQUIRED"},
			domain.Detail{Field: "source_uri", Code: "EXACTLY_ONE_SOURCE_REQUIRED"})
	}
	if document != nil {
		return []byte(*document), nil
	}
	return adapter.exports.read(*sourceURI)
}

// toolProfileCustom is the tool_profile value get_project and open_project
// report when a free-form toolset selection, not a named profile, decides
// the advertised catalog. It is a report-only value: ParseToolProfile does
// not accept it.
const toolProfileCustom = "custom"

// catalogReport renders the active catalog selection for get_project and
// open_project: the named profile with no toolset list in profile mode, or
// toolProfileCustom plus the advertised groups (canonical domain.AllToolsets
// order, always including core, which is advertised unconditionally) in
// toolset mode — so a client that expects a tool and doesn't see it in
// tools/list can tell exactly which selection is the reason.
func (adapter *adapter) catalogReport() (toolProfile string, toolsets []string) {
	if adapter.toolsets == nil {
		return string(adapter.toolProfile), nil
	}
	names := make([]string, 0, len(adapter.toolsets)+1)
	for _, candidate := range domain.AllToolsets {
		if candidate == domain.ToolsetCore || adapter.toolsets[candidate] {
			names = append(names, string(candidate))
		}
	}
	return toolProfileCustom, names
}

func (adapter *adapter) getProject(ctx context.Context, request *sdkmcp.CallToolRequest, input getProjectInput) (*sdkmcp.CallToolResult, any, error) {
	project, err := adapter.services.ProjectService.GetProject(ctx)
	if err != nil {
		return adapter.failure(err)
	}
	toolProfile, toolsets := adapter.catalogReport()
	output := projectOutputFor(
		ProjectRefFromContext(ctx),
		projectDTOFromDomain(project, input.IncludeInstructions),
		nil,
		adapter.appVersion,
		project.SchemaVersion,
		adapter.configVersion,
		toolProfile,
		toolsets,
		project.LatestEventID,
	)
	return adapter.success(output, fmt.Sprintf("project metadata returned (project_ref=%s, latest_event_id=%d)", output.ProjectRef, project.LatestEventID))
}

func (adapter *adapter) openProject(ctx context.Context, request *sdkmcp.CallToolRequest, input openProjectInput) (*sdkmcp.CallToolResult, any, error) {
	lease, err := adapter.router.OpenProject(ctx, input.ProjectRoot)
	if err != nil {
		return adapter.failure(err)
	}
	if lease == nil {
		return adapter.failure(domain.NewError(domain.CodeInvalidArgument, "project router returned a nil lease", false))
	}
	defer func() {
		_ = lease.Release()
	}()
	project, err := lease.Services().ProjectService.GetProject(ctx)
	if err != nil {
		return adapter.failure(err)
	}
	toolProfile, toolsets := adapter.catalogReport()
	output := projectOutputFor(
		lease.ProjectRef(),
		projectDTOFromDomain(project, false),
		nil,
		adapter.appVersion,
		project.SchemaVersion,
		adapter.configVersion,
		toolProfile,
		toolsets,
		project.LatestEventID,
	)
	return adapter.success(output, fmt.Sprintf("project opened (project_ref=%s, latest_event_id=%d)", output.ProjectRef, project.LatestEventID))
}

func (adapter *adapter) manageIssueRelation(ctx context.Context, request *sdkmcp.CallToolRequest, input manageIssueRelationInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.services.RelationService.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
		Action:         domain.RelationAction(input.Action),
		SourceIssueID:  input.SourceIssueID,
		TargetIssueID:  input.TargetIssueID,
		RelationType:   domain.RelationType(input.RelationType),
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return adapter.failure(err)
	}
	affected := make([]relationAffectedIssueDTO, len(result.AffectedIssues))
	for index, issue := range result.AffectedIssues {
		affected[index] = relationAffectedIssueDTOFromDomain(issue)
	}
	summary := "relation was already absent"
	if input.Action == string(domain.RelationActionAdd) {
		summary = "relation already present"
	}
	if result.Changed {
		summary = "relation added"
		if input.Action == string(domain.RelationActionRemove) {
			summary = "relation removed"
		}
	}
	return adapter.success(manageIssueRelationOutput{
		Relation: relationDTOFromDomain(result.Relation), AffectedIssues: affected, Changed: result.Changed,
	}, summary)
}

func (adapter *adapter) listLabels(ctx context.Context, request *sdkmcp.CallToolRequest, input listLabelsInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.services.IssueService.ListLabels(ctx, domain.ListLabelsInput{
		Query:  stringValue(input.Query),
		Limit:  input.Limit,
		Cursor: stringValue(input.Cursor),
	})
	if err != nil {
		return adapter.failure(err)
	}
	items := make([]labelDTO, len(result.Items))
	for i, item := range result.Items {
		items[i] = labelDTOFromDomain(item)
	}
	return adapter.success(labelListOutput{Items: items, NextCursor: result.NextCursor, HasMore: result.HasMore}, fmt.Sprintf("labels listed (count=%d, has_more=%t)", len(items), result.HasMore))
}

func (adapter *adapter) createIssue(ctx context.Context, request *sdkmcp.CallToolRequest, input createIssueInput) (*sdkmcp.CallToolResult, any, error) {
	view, err := resolveView(input.View, "compact", "compact", "full")
	if err != nil {
		return adapter.failure(err)
	}
	result, err := adapter.services.IssueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type:                domain.Type(input.Type),
		Title:               input.Title,
		Description:         input.Description,
		AcceptanceCriteria:  input.AcceptanceCriteria,
		Status:              domain.Status(input.Status),
		Priority:            domain.Priority(input.Priority),
		ParentID:            input.ParentIssueID,
		BlockedReason:       input.BlockedReason,
		Labels:              input.Labels,
		CreateMissingLabels: input.CreateMissingLabels,
		IdempotencyKey:      input.IdempotencyKey,
	})
	if err != nil {
		return adapter.failure(err)
	}
	summary := fmt.Sprintf("issue created (display_id=%s, id=%s, status=%s, version=%d)",
		result.Issue.DisplayID, result.Issue.ID, result.Issue.Status, result.Issue.Version)
	if view == "full" {
		return adapter.success(issueDTOFromDomain(result.Issue), summary)
	}
	return adapter.success(createIssueCompactOutputFromDomain(result.Issue), summary)
}

func (adapter *adapter) updateIssue(ctx context.Context, request *sdkmcp.CallToolRequest, input updateIssueInput) (*sdkmcp.CallToolResult, any, error) {
	view, err := resolveView(input.View, "compact", "compact", "full")
	if err != nil {
		return adapter.failure(err)
	}
	result, err := adapter.services.IssueService.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID:             input.IssueID,
		ExpectedVersion:     input.ExpectedVersion,
		Changes:             input.Changes.domainPatch(),
		CreateMissingLabels: input.CreateMissingLabels,
		IdempotencyKey:      input.IdempotencyKey,
	})
	if err != nil {
		return adapter.failure(err)
	}
	summary := fmt.Sprintf("issue updated (display_id=%s, id=%s, status=%s, version=%d)",
		result.Issue.DisplayID, result.Issue.ID, result.Issue.Status, result.Issue.Version)
	if view == "full" {
		return adapter.success(updateIssueOutput{Issue: issueDTOFromDomain(result.Issue), ChangedFields: result.ChangedFields}, summary)
	}
	return adapter.success(updateIssueCompactOutputFromDomain(result.Issue, result.ChangedFields), summary)
}

func (adapter *adapter) getIssue(ctx context.Context, request *sdkmcp.CallToolRequest, input getIssueInput) (*sdkmcp.CallToolResult, any, error) {
	view, err := resolveView(input.View, "standard", "compact", "standard", "full")
	if err != nil {
		return adapter.failure(err)
	}
	if len(input.Include) != 0 {
		return adapter.failure(unsupportedField("include"))
	}
	if len(input.Limits) != 0 {
		return adapter.failure(unsupportedField("limits"))
	}
	issue, err := adapter.services.IssueService.GetIssue(ctx, input.IssueID)
	if err != nil {
		return adapter.failure(err)
	}
	summary := fmt.Sprintf("issue returned (display_id=%s, id=%s, status=%s, version=%d)",
		issue.DisplayID, issue.ID, issue.Status, issue.Version)
	switch view {
	case "compact":
		return adapter.success(issueCompactProjectionDTOFromDomain(issue), summary)
	case "standard":
		return adapter.success(issueStandardProjectionDTOFromDomain(issue), summary)
	case "full":
		return adapter.success(issueDTOFromDomain(issue), summary)
	default:
		return adapter.failure(unsupportedField("view"))
	}
}

func (adapter *adapter) listIssues(ctx context.Context, request *sdkmcp.CallToolRequest, input listIssuesInput) (*sdkmcp.CallToolResult, any, error) {
	view, err := resolveView(input.View, "compact", "compact", "full")
	if err != nil {
		return adapter.failure(err)
	}
	result, err := adapter.services.IssueService.ListIssues(ctx, domain.ListIssuesInput{
		Types:             stringsToTypes(input.Types),
		Statuses:          stringsToStatuses(input.Statuses),
		EffectiveStatuses: stringsToEffectiveStatuses(input.EffectiveStatuses),
		Priorities:        stringsToPriorities(input.Priorities),
		Labels:            input.Labels,
		ParentIssueID:     input.ParentIssueID,
		IsBlocked:         input.IsBlocked,
		IsClaimable:       input.IsClaimable,
		IncludeArchived:   input.IncludeArchived,
		Limit:             input.Limit,
		Cursor:            stringValue(input.Cursor),
	})
	if err != nil {
		return adapter.failure(err)
	}
	nextActions := []string{"Inspect a claimable issue with get_work_context."}
	if result.HasMore {
		nextActions = append(nextActions, "Continue with next_cursor.")
	}
	summary := fmt.Sprintf("issues listed (count=%d, has_more=%t)", len(result.Items), result.HasMore)
	if view == "full" {
		items := make([]issueListItemDTO, len(result.Items))
		for i, item := range result.Items {
			items[i] = issueListItemDTO{
				issueDTO:               issueDTOFromDomain(item.Issue),
				EffectiveStatus:        string(item.EffectiveStatus),
				UnresolvedBlockerCount: item.UnresolvedBlockerCount,
				IsBlocked:              item.IsBlocked,
				IsClaimable:            item.IsClaimable,
				ActiveAttemptID:        item.ActiveAttemptID,
			}
		}
		return adapter.success(issueListOutput{
			Items: items, NextCursor: result.NextCursor, HasMore: result.HasMore, NextActions: nextActions,
		}, summary)
	}
	items := make([]issueListItemCompactDTO, len(result.Items))
	for i, item := range result.Items {
		items[i] = issueListItemCompactDTOFromDomain(item)
	}
	return adapter.success(issueListCompactOutput{
		Items: items, NextCursor: result.NextCursor, HasMore: result.HasMore, NextActions: nextActions,
	}, summary)
}

func (adapter *adapter) archiveIssue(ctx context.Context, request *sdkmcp.CallToolRequest, input archiveIssueInput) (*sdkmcp.CallToolResult, any, error) {
	view, err := resolveView(input.View, "compact", "compact", "full")
	if err != nil {
		return adapter.failure(err)
	}
	result, err := adapter.services.IssueService.ArchiveIssue(ctx, domain.ArchiveIssueInput{
		IssueID:         input.IssueID,
		ExpectedVersion: input.ExpectedVersion,
		IdempotencyKey:  input.IdempotencyKey,
	})
	if err != nil {
		return adapter.failure(err)
	}
	summary := fmt.Sprintf("issue archived (display_id=%s, id=%s, status=%s, version=%d)",
		result.Issue.DisplayID, result.Issue.ID, result.Issue.Status, result.Issue.Version)
	if view == "full" {
		return adapter.success(issueDTOFromDomain(result.Issue), summary)
	}
	return adapter.success(archiveIssueCompactOutputFromDomain(result.Issue), summary)
}

func (adapter *adapter) unarchiveIssue(ctx context.Context, request *sdkmcp.CallToolRequest, input unarchiveIssueInput) (*sdkmcp.CallToolResult, any, error) {
	view, err := resolveView(input.View, "compact", "compact", "full")
	if err != nil {
		return adapter.failure(err)
	}
	result, err := adapter.services.IssueService.UnarchiveIssue(ctx, domain.UnarchiveIssueInput{
		IssueID:         input.IssueID,
		ExpectedVersion: input.ExpectedVersion,
		SessionID:       adapter.sessionIDForRequest(ctx, request),
		IdempotencyKey:  input.IdempotencyKey,
	})
	if err != nil {
		return adapter.failure(err)
	}
	summary := fmt.Sprintf("issue unarchived (display_id=%s, id=%s, status=%s, version=%d)",
		result.Issue.DisplayID, result.Issue.ID, result.Issue.Status, result.Issue.Version)
	if view == "full" {
		return adapter.success(issueDTOFromDomain(result.Issue), summary)
	}
	return adapter.success(unarchiveIssueCompactOutputFromDomain(result.Issue), summary)
}

func (adapter *adapter) createReviewRequest(ctx context.Context, request *sdkmcp.CallToolRequest, input createReviewRequestInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.services.ReviewService.CreateReviewRequest(ctx, application.CreateReviewRequestInput{
		IssueID:            input.IssueID,
		TargetIssueVersion: input.TargetIssueVersion,
		TargetEventID:      input.TargetEventID,
		ArtifactIDs:        append([]string(nil), input.ArtifactIDs...),
		Purposes:           append([]string(nil), input.Purposes...),
	})
	if err != nil {
		return adapter.failure(err)
	}
	return adapter.success(reviewRequestDTOFromDomain(result.Request, result.Claimable),
		fmt.Sprintf("review request created (review_request_id=%s, version=%d)", result.Request.ID, result.Request.Version))
}

func (adapter *adapter) getReviewRequest(ctx context.Context, request *sdkmcp.CallToolRequest, input getReviewRequestInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.services.ReviewService.GetReviewRequest(ctx, input.ReviewRequestID)
	if err != nil {
		return adapter.failure(err)
	}
	return adapter.success(reviewRequestDTOFromDomain(result.Request, result.Claimable),
		fmt.Sprintf("review request read (review_request_id=%s, status=%s, version=%d)", result.Request.ID, result.Request.Status, result.Request.Version))
}

func (adapter *adapter) listReviewRequests(ctx context.Context, request *sdkmcp.CallToolRequest, input listReviewRequestsInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.services.ReviewService.ListReviewRequests(ctx, application.ListReviewRequestsInput{
		Status:    input.Status,
		Claimable: input.Claimable,
		Limit:     input.Limit,
		Cursor:    input.Cursor,
	})
	if err != nil {
		return adapter.failure(err)
	}
	items := make([]reviewRequestDTO, len(result.Items))
	for index, item := range result.Items {
		items[index] = reviewRequestDTOFromDomain(item.Request, item.Claimable)
	}
	output := reviewRequestListOutput{Items: items, HasMore: result.HasMore}
	if result.NextCursor != nil {
		output.NextCursor = result.NextCursor
	}
	return adapter.success(output, fmt.Sprintf("review requests listed (count=%d, has_more=%t)", len(items), result.HasMore))
}

func (adapter *adapter) cancelReviewRequest(ctx context.Context, request *sdkmcp.CallToolRequest, input cancelReviewRequestInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.services.ReviewService.CancelReviewRequest(ctx, application.ReviewMutationInput{RequestID: input.ReviewRequestID, ExpectedVersion: input.ExpectedVersion})
	if err != nil {
		return adapter.failure(err)
	}
	return adapter.success(reviewRequestDTOFromDomain(result.Request, result.Claimable),
		fmt.Sprintf("review request cancelled (review_request_id=%s, version=%d)", result.Request.ID, result.Request.Version))
}

func (adapter *adapter) replaceReviewRequest(ctx context.Context, request *sdkmcp.CallToolRequest, input replaceReviewRequestInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.services.ReviewService.ReplaceReviewRequest(ctx, application.ReplaceReviewRequestInput{
		PredecessorRequestID:       input.PredecessorRequestID,
		PredecessorExpectedVersion: input.PredecessorExpectedVersion,
		TargetIssueVersion:         input.TargetIssueVersion,
		TargetEventID:              input.TargetEventID,
		ArtifactIDs:                append([]string(nil), input.ArtifactIDs...),
		Purposes:                   append([]string(nil), input.Purposes...),
		IdempotencyKey:             input.IdempotencyKey,
	})
	if err != nil {
		return adapter.failure(err)
	}
	output := replaceReviewRequestOutput{
		Predecessor:   reviewRequestDTOFromDomain(result.Predecessor, false),
		Successor:     reviewRequestDTOFromDomain(result.Successor, true),
		LatestEventID: result.LatestEventID,
	}
	return adapter.success(output, fmt.Sprintf("review request replaced (predecessor_id=%s, successor_id=%s, latest_event_id=%d)",
		result.Predecessor.ID, result.Successor.ID, result.LatestEventID))
}

// maxAutoTextContentBytes caps inline JSON duplication in Content[0].Text
// under ContentModeAuto: every default response budget in docs/03 §3 is
// well under 64 KiB, while oversized explicit opt-in payloads (e.g.
// get_issue view:"full" on a multi-hundred-KiB body) fall back to the
// enriched summary string in Content[0].Text and keep the full payload in
// StructuredContent.
const maxAutoTextContentBytes = 64 * 1024

func (adapter *adapter) success(output any, summary string) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.renderResult(output, summary, false)
	if err != nil {
		return adapter.failure(domain.WrapError(err, domain.CodeStorageFailure, "tool output could not be encoded", false))
	}
	return result, nil, nil
}

func (adapter *adapter) failure(err error) (*sdkmcp.CallToolResult, any, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, nil, err
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		domainErr = domain.NewError(domain.CodeStorageFailure, "request could not be completed", false)
	}
	output := errorOutput{
		Code:      domainErr.Code,
		Message:   domainErr.Message,
		Details:   domainErr.Details,
		Retryable: domainErr.Retryable,
	}
	summary := fmt.Sprintf("%s: %s", output.Code, output.Message)
	result, marshalErr := adapter.renderResult(output, summary, true)
	if marshalErr != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: summary}},
			IsError: true,
		}, nil, nil
	}
	return result, nil, nil
}

func (adapter *adapter) renderResult(output any, summary string, isError bool) (*sdkmcp.CallToolResult, error) {
	switch adapter.contentMode {
	case domain.ContentModeStructured:
		return &sdkmcp.CallToolResult{
			Content:           []sdkmcp.Content{&sdkmcp.TextContent{Text: summary}},
			StructuredContent: output,
			IsError:           isError,
		}, nil
	case domain.ContentModeText:
		data, err := json.Marshal(output)
		if err != nil {
			return nil, err
		}
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: string(data)}},
			IsError: isError,
		}, nil
	default:
		data, err := json.Marshal(output)
		if err != nil {
			return nil, err
		}
		text := summary
		if len(data) <= maxAutoTextContentBytes {
			text = string(data)
		}
		return &sdkmcp.CallToolResult{
			Content:           []sdkmcp.Content{&sdkmcp.TextContent{Text: text}},
			StructuredContent: output,
			IsError:           isError,
		}, nil
	}
}

func unsupportedField(field string) *domain.Error {
	return domain.NewError(domain.CodeInvalidArgument, fmt.Sprintf("field %q is not supported", field), false,
		domain.Detail{Field: field, Code: "UNSUPPORTED"})
}

// requiredForAction reports that an action-dispatched tool was called without
// the fields that action needs. The action lives in the payload rather than in
// the tool name, so the JSON schema cannot express the dependency and the
// handler has to.
func requiredForAction(action string, fields ...string) *domain.Error {
	details := make([]domain.Detail, len(fields))
	for index, field := range fields {
		details[index] = domain.Detail{Field: field, Code: "REQUIRED"}
	}
	return domain.NewError(domain.CodeInvalidArgument,
		fmt.Sprintf("action %q requires %s", action, strings.Join(fields, " and ")), false, details...)
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func stringsToTypes(values []string) []domain.Type {
	result := make([]domain.Type, len(values))
	for i, value := range values {
		result[i] = domain.Type(value)
	}
	return result
}

func stringsToStatuses(values []string) []domain.Status {
	result := make([]domain.Status, len(values))
	for i, value := range values {
		result[i] = domain.Status(value)
	}
	return result
}

func stringsToEffectiveStatuses(values []string) []domain.EffectiveStatus {
	result := make([]domain.EffectiveStatus, len(values))
	for index, value := range values {
		result[index] = domain.EffectiveStatus(value)
	}
	return result
}

func stringsToPriorities(values []string) []domain.Priority {
	result := make([]domain.Priority, len(values))
	for i, value := range values {
		result[i] = domain.Priority(value)
	}
	return result
}

// --- ISSUE-174: workflow policy and gate handlers ---

func workflowPolicyInputFromDTO(selector *workflowPolicySelectorIn, requirements []workflowPolicyRequireIn) domain.WorkflowPolicyInput {
	var selectorInput domain.PolicySelectorInput
	if selector != nil {
		types := make([]domain.Type, len(selector.IssueTypes))
		for index, value := range selector.IssueTypes {
			types[index] = domain.Type(value)
		}
		selectorInput = domain.PolicySelectorInput{IssueTypes: types, LabelsAll: append([]string(nil), selector.LabelsAll...)}
	}
	requirementInputs := make([]domain.PolicyRequirementInput, len(requirements))
	for index, requirement := range requirements {
		requirementInputs[index] = domain.PolicyRequirementInput{
			Key: requirement.Key, Kind: domain.RequirementKind(requirement.Kind), Field: requirement.Field,
			EvidenceKey: requirement.EvidenceKey, Purpose: requirement.Purpose,
			AllowNotApplicable: requirement.AllowNotApplicable,
		}
	}
	return domain.WorkflowPolicyInput{Selector: selectorInput, Requirements: requirementInputs}
}

func (adapter *adapter) manageWorkflowPolicy(ctx context.Context, request *sdkmcp.CallToolRequest, input manageWorkflowPolicyInput) (*sdkmcp.CallToolResult, any, error) {
	sessionID := adapter.sessionIDForRequest(ctx, request)
	idempotencyKey := stringValue(input.IdempotencyKey)
	switch input.Action {
	case "create":
		policy, err := adapter.services.WorkflowPolicyService.CreatePolicy(ctx,
			workflowPolicyInputFromDTO(input.Selector, input.Requirements), sessionID, idempotencyKey)
		if err != nil {
			return adapter.failure(err)
		}
		return adapter.success(workflowPolicyDTOFromDomain(policy), fmt.Sprintf("workflow policy created (policy_id=%s, version=%d)", policy.ID, policy.Version))
	case "update":
		if input.PolicyID == nil || input.ExpectedVersion == nil {
			return adapter.failure(requiredForAction("update", "policy_id", "expected_version"))
		}
		policy, err := adapter.services.WorkflowPolicyService.UpdatePolicy(ctx, *input.PolicyID, *input.ExpectedVersion,
			workflowPolicyInputFromDTO(input.Selector, input.Requirements), sessionID, idempotencyKey)
		if err != nil {
			return adapter.failure(err)
		}
		return adapter.success(workflowPolicyDTOFromDomain(policy), fmt.Sprintf("workflow policy updated (policy_id=%s, version=%d)", policy.ID, policy.Version))
	case "archive":
		if input.PolicyID == nil || input.ExpectedVersion == nil {
			return adapter.failure(requiredForAction("archive", "policy_id", "expected_version"))
		}
		policy, err := adapter.services.WorkflowPolicyService.ArchivePolicy(ctx, *input.PolicyID, *input.ExpectedVersion, sessionID, idempotencyKey)
		if err != nil {
			return adapter.failure(err)
		}
		return adapter.success(workflowPolicyDTOFromDomain(policy), fmt.Sprintf("workflow policy archived (policy_id=%s, version=%d)", policy.ID, policy.Version))
	default:
		return adapter.failure(unsupportedField("action"))
	}
}

func (adapter *adapter) getWorkflowPolicy(ctx context.Context, request *sdkmcp.CallToolRequest, input getWorkflowPolicyInput) (*sdkmcp.CallToolResult, any, error) {
	view, err := resolveView(input.View, "compact", "compact", "full")
	if err != nil {
		return adapter.failure(err)
	}
	policy, err := adapter.services.WorkflowPolicyService.GetPolicy(ctx, input.PolicyID)
	if err != nil {
		return adapter.failure(err)
	}
	summary := fmt.Sprintf("workflow policy read (policy_id=%s, status=%s, version=%d)", policy.ID, policy.Status, policy.Version)
	if view == "compact" {
		return adapter.success(workflowPolicySummaryDTOFromDomain(policy), summary)
	}
	return adapter.success(workflowPolicyDTOFromDomain(policy), summary)
}

func (adapter *adapter) listWorkflowPolicies(ctx context.Context, request *sdkmcp.CallToolRequest, input listWorkflowPoliciesInput) (*sdkmcp.CallToolResult, any, error) {
	var status *domain.PolicyStatus
	if input.Status != nil {
		parsed := domain.PolicyStatus(*input.Status)
		status = &parsed
	}
	list, err := adapter.services.WorkflowPolicyService.ListPolicies(ctx, domain.ListWorkflowPoliciesInput{
		Status: status, Limit: input.Limit, Cursor: stringValue(input.Cursor),
	})
	if err != nil {
		return adapter.failure(err)
	}
	return adapter.success(workflowPolicyListOutputFromDomain(list), fmt.Sprintf("workflow policies listed (count=%d, has_more=%t)", len(list.Items), list.HasMore))
}

func (adapter *adapter) evaluateGates(ctx context.Context, request *sdkmcp.CallToolRequest, input evaluateGatesInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.services.WorkflowPolicyService.EvaluateGates(ctx, application.EvaluateGatesInput{
		IssueID:          input.IssueID,
		EnforcementPoint: domain.EnforcementPoint(input.EnforcementPoint),
		AttemptID:        input.AttemptID,
		ReviewTargetID:   input.ReviewTargetID,
	})
	if err != nil {
		return adapter.failure(err)
	}
	output := evaluateGatesOutputFromApplication(result)
	return adapter.success(output, fmt.Sprintf("workflow gates evaluated (passed=%t)", output.Passed))
}

func (adapter *adapter) submitGateEvidence(ctx context.Context, request *sdkmcp.CallToolRequest, input submitGateEvidenceInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.services.AttemptService.SubmitGateEvidence(ctx, domain.SubmitGateEvidenceInput{
		AttemptID: input.AttemptID, LeaseToken: input.LeaseToken, Key: input.Key,
		Result: domain.EvidenceResult(input.Result), Summary: input.Summary, Details: input.Details,
		ArtifactIDs: append([]string(nil), input.ArtifactIDs...), IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return adapter.failure(err)
	}
	return adapter.success(submitGateEvidenceOutput{Evidence: attemptEvidenceDTOFromDomain(result.Evidence)},
		fmt.Sprintf("gate evidence submitted (evidence_id=%s, key=%s, result=%s)", result.Evidence.ID, result.Evidence.Key, result.Evidence.Result))
}
