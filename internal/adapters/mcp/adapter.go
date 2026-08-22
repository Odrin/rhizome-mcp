// Package mcp exposes application services through MCP tools.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/domain"
)

// Options supplies the explicit composition dependencies for the MCP adapter.
type Options struct {
	ProjectRouter   ProjectRouter
	ServerName      string
	ServerVersion   string
	ConfigVersion   int
	ExportDirectory string
	// ToolProfile selects which capability groups of the tool catalog this
	// server instance advertises. Blank defaults to domain.ToolProfileFull.
	ToolProfile string
}

type adapter struct {
	router        ProjectRouter
	issues        *application.IssueService
	projects      *application.ProjectService
	relations     *application.RelationService
	graphs        *application.GraphService
	plans         *application.PlanningService
	comments      *application.CommentService
	decisions     *application.DecisionService
	activities    *application.ActivityService
	searches      *application.SearchService
	reviews       *application.ReviewService
	attempts      *application.AttemptService
	sessions      *application.AgentSessionService
	workContexts  *application.WorkContextService
	appVersion    string
	configVersion int
	toolProfile   domain.ToolProfile
	exports       *exportArtifactStore
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
	toolProfile, err := domain.ParseToolProfile(options.ToolProfile)
	if err != nil {
		return nil, err
	}
	exports, err := newExportArtifactStore(options.ExportDirectory)
	if err != nil {
		return nil, domain.WrapError(err, domain.CodeStorageConfiguration, "managed export artifacts could not be initialized", false)
	}
	services := ProjectServices{}
	if lease, err := options.ProjectRouter.Acquire(context.Background(), nil); err != nil {
		var domainErr *domain.Error
		if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeProjectRequired {
			return nil, err
		}
	} else if lease == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "project router returned a nil lease", false)
	} else {
		services = ProjectServices{
			IssueService:       lease.IssueService(),
			ProjectService:     lease.ProjectService(),
			RelationService:    lease.RelationService(),
			GraphService:       lease.GraphService(),
			PlanningService:    lease.PlanningService(),
			CommentService:     lease.CommentService(),
			DecisionService:    lease.DecisionService(),
			ActivityService:    lease.ActivityService(),
			SearchService:      lease.SearchService(),
			ReviewService:      lease.ReviewService(),
			AttemptService:     lease.AttemptService(),
			SessionService:     lease.SessionService(),
			WorkContextService: lease.WorkContextService(),
		}
		if err := lease.Release(); err != nil {
			return nil, err
		}
		if err := validateProjectServices(services); err != nil {
			return nil, err
		}
	}
	adapter := &adapter{
		router:        options.ProjectRouter,
		issues:        services.IssueService,
		projects:      services.ProjectService,
		relations:     services.RelationService,
		graphs:        services.GraphService,
		plans:         services.PlanningService,
		comments:      services.CommentService,
		decisions:     services.DecisionService,
		activities:    services.ActivityService,
		searches:      services.SearchService,
		reviews:       services.ReviewService,
		attempts:      services.AttemptService,
		sessions:      services.SessionService,
		workContexts:  services.WorkContextService,
		appVersion:    options.ServerVersion,
		configVersion: options.ConfigVersion,
		toolProfile:   toolProfile,
		exports:       exports,
	}
	server := sdkmcp.NewServer(
		&sdkmcp.Implementation{Name: options.ServerName, Version: options.ServerVersion},
		&sdkmcp.ServerOptions{
			Capabilities: &sdkmcp.ServerCapabilities{Tools: &sdkmcp.ToolCapabilities{}},
			Instructions: initializeInstructions,
		},
	)
	adapter.register(server)
	registerGuides(server)
	return &Server{server: server, adapter: adapter}, nil
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
	target.registerTool(server, groupReview, tool("cancel_review_request", "Cancel an open or claimed review request using its current version.", schemaCancelReviewRequest(), schemaReviewRequestOutput(), toolHints(false, true, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[cancelReviewRequestInput, any](target, t, (*adapter).cancelReviewRequest))
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
	target.registerTool(server, groupLifecycle, tool("save_attempt_note", "Append a restartable checkpoint, finding, warning, or progress note.", schemaSaveAttemptNote(), schemaSaveAttemptNoteOutput(), toolHints(false, false, false, false)), func(t *sdkmcp.Tool) {
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
	target.registerTool(server, groupKnowledge, tool("search", "Full-text search with cursor pagination; default limit 20; archived records are excluded unless requested; results are relevance ordered.", schemaSearch(), schemaSearchOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[searchInput, any](target, t, (*adapter).search))
	})
	target.registerTool(server, groupSync, tool("get_changes", "Get ordered issue events after an event ID for incremental synchronization.", schemaGetChanges(), schemaChangesOutput(), toolHints(true, false, true, false)), func(t *sdkmcp.Tool) {
		sdkmcp.AddTool(server, t, routeProjectRequest[getChangesInput, any](target, t, (*adapter).getChanges))
	})
}

func (adapter *adapter) createAgentSession(ctx context.Context, request *sdkmcp.CallToolRequest, input createAgentSessionInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.sessions.CreateWithHandle(ctx, domain.CreateAgentSessionInput{
		ClientName: input.ClientName, ClientVersion: input.ClientVersion, AgentLabel: input.AgentLabel,
		Model: input.Model, InstanceKey: input.InstanceKey,
	})
	if err != nil {
		return adapter.failure(err)
	}
	return success(createAgentSessionOutput{Session: sessionDTOFromDomain(result.Session), AgentSessionHandle: result.Handle}, "agent session created")
}

func (adapter *adapter) endAgentSession(ctx context.Context, request *sdkmcp.CallToolRequest, input endAgentSessionInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.sessions.EndWithHandle(ctx, input.AgentSessionHandle)
	if err != nil {
		return adapter.failure(err)
	}
	return success(endAgentSessionOutput{Session: sessionDTOFromDomain(result)}, "agent session ended")
}

func (adapter *adapter) search(ctx context.Context, request *sdkmcp.CallToolRequest, input searchInput) (*sdkmcp.CallToolResult, any, error) {
	entityTypes := make([]domain.SearchEntityType, len(input.EntityTypes))
	for index, value := range input.EntityTypes {
		entityTypes[index] = domain.SearchEntityType(value)
	}
	result, err := adapter.searches.Search(ctx, domain.SearchInput{
		Query: input.Query, EntityTypes: entityTypes, IssueID: input.IssueID, EpicID: input.EpicID,
		Statuses: stringsToStatuses(input.Statuses), Labels: input.Labels, IncludeArchived: input.IncludeArchived,
		Limit: input.Limit, Cursor: stringValue(input.Cursor), SnippetLength: input.SnippetLength,
	})
	if err != nil {
		return adapter.failure(err)
	}
	return success(searchOutputFromDomain(result), "search results returned")
}

func (adapter *adapter) getChanges(ctx context.Context, request *sdkmcp.CallToolRequest, input getChangesInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.searches.GetChanges(ctx, domain.GetChangesInput{
		SinceEventID: input.SinceEventID, IssueID: input.IssueID, EventTypes: input.EventTypes, Limit: input.Limit,
	})
	if err != nil {
		return adapter.failure(err)
	}
	return success(changesOutputFromDomain(result), "changes returned")
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
	}
	result, err := adapter.workContexts.GetWorkContext(ctx, domain.GetWorkContextInput{IssueID: input.IssueID, Include: include, Limits: limits})
	if err != nil {
		return adapter.failure(err)
	}
	output := workContextOutputFromDomain(result)
	output.NextActions = []string{"Call claim_issue when the issue is claimable."}
	return success(output, "work context returned")
}

func (adapter *adapter) claimIssue(ctx context.Context, request *sdkmcp.CallToolRequest, input claimIssueInput) (*sdkmcp.CallToolResult, any, error) {
	if input.View != "" && input.View != "compact" && input.View != "full" {
		return adapter.failure(unsupportedField("view"))
	}
	sessionID := adapter.sessionIDForRequest(ctx, request)
	result, err := adapter.attempts.ClaimIssue(ctx, domain.ClaimIssueInput{IssueID: input.IssueID, LeaseSeconds: input.LeaseSeconds, SessionID: sessionID, IdempotencyKey: input.IdempotencyKey})
	if err != nil {
		return adapter.failure(err)
	}
	view := input.View
	if view == "" {
		view = "compact"
	}
	if view == "full" {
		attempt := attemptDTOFromDomain(result.Attempt)
		return success(claimIssueOutput{
			Issue: issueListItemDTO{
				issueDTO:               issueDTOFromDomain(result.Projection.Issue),
				EffectiveStatus:        string(result.Projection.EffectiveStatus),
				UnresolvedBlockerCount: result.Projection.UnresolvedBlockerCount,
				IsBlocked:              result.Projection.IsBlocked,
				IsClaimable:            result.Projection.IsClaimable,
				ActiveAttemptID:        result.Projection.ActiveAttemptID,
			},
			Attempt: attempt, LeaseToken: result.LeaseToken, LeaseExpiresAt: result.Attempt.LeaseExpiresAt,
			MinimalWorkContext: emptyWorkContextDTO{}, Warnings: []string{},
			NextActions: []string{"Renew before expiry; finish_attempt on every exit."},
		}, "issue claimed")
	}
	return success(claimIssueCompactOutputFromDomain(result.Issue, result.Attempt, result.LeaseToken), "issue claimed")
}

func (adapter *adapter) renewAttempt(ctx context.Context, request *sdkmcp.CallToolRequest, input renewAttemptInput) (*sdkmcp.CallToolResult, any, error) {
	sessionID := adapter.sessionIDForRequest(ctx, request)
	result, err := adapter.attempts.RenewAttempt(ctx, domain.RenewAttemptInput{
		AttemptID: input.AttemptID, LeaseToken: input.LeaseToken, LeaseSeconds: input.LeaseSeconds, SessionID: sessionID,
	})
	if err != nil {
		return adapter.failure(err)
	}
	return success(renewAttemptOutput{
		LeaseExpiresAt: result.LeaseExpiresAt, ServerTime: result.ServerTime,
		NextActions: []string{"Continue work; checkpoint or finish before expiry."},
	}, "attempt lease renewed")
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
	result, err := adapter.attempts.SaveAttemptNote(ctx, domain.SaveAttemptNoteInput{
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
	return success(saveAttemptNoteOutput{
		AttemptNote: attemptNoteDTOFromDomain(result.Note), Artifacts: outputArtifacts,
		NextActions: []string{"Continue work or call finish_attempt."},
	}, "attempt note saved")
}

func (adapter *adapter) finishAttempt(ctx context.Context, request *sdkmcp.CallToolRequest, input finishAttemptInput) (*sdkmcp.CallToolResult, any, error) {
	if input.View != "" && input.View != "compact" && input.View != "full" {
		return adapter.failure(unsupportedField("view"))
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
	result, err := adapter.attempts.FinishAttempt(ctx, domain.FinishAttemptInput{
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
	view := input.View
	if view == "" {
		view = "compact"
	}
	if view == "full" {
		outputArtifacts := make([]artifactDTO, len(result.Artifacts))
		for index, artifact := range result.Artifacts {
			outputArtifacts[index] = artifactDTOFromDomain(artifact)
		}
		return success(finishAttemptOutput{Attempt: attemptDTOFromDomain(result.Attempt), Issue: issueDTOFromDomain(result.Issue),
			Warnings: append([]string{}, result.Warnings...), LatestEventID: result.LatestEventID, Artifacts: outputArtifacts,
			NextActions: []string{"Select new work from get_planning_graph."}}, "attempt finished")
	}
	return success(finishAttemptCompactOutputFromDomain(result.Attempt, result.Issue, result.Warnings, result.LatestEventID, result.Artifacts, []string{"Select new work from get_planning_graph."}), "attempt finished")
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
	validation, err := adapter.plans.ValidateIssuePlan(ctx, plan)
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
	return success(output, "issue plan validated")
}

func (adapter *adapter) applyIssuePlan(ctx context.Context, request *sdkmcp.CallToolRequest, input applyIssuePlanInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.plans.ApplyIssuePlan(ctx, input.domainPlan(), input.IdempotencyKey)
	if err != nil {
		return adapter.failure(err)
	}
	output := applyIssuePlanOutputFromPort(result)
	output.NextActions = []string{"Use get_planning_graph to select executable work."}
	return success(output, "issue plan applied")
}

func (adapter *adapter) addComment(ctx context.Context, request *sdkmcp.CallToolRequest, input addCommentInput) (*sdkmcp.CallToolResult, any, error) {
	comment, err := adapter.comments.AddComment(ctx, domain.AddCommentInput{
		IssueID: input.IssueID, Content: input.Content, SessionID: adapter.sessionIDForRequest(ctx, request),
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return adapter.failure(err)
	}
	return success(addCommentOutput{Comment: commentDTOFromDomain(comment)}, "comment added")
}

func (adapter *adapter) recordDecision(ctx context.Context, request *sdkmcp.CallToolRequest, input recordDecisionInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.decisions.RecordDecision(ctx, domain.RecordDecisionInput{
		IssueID: input.IssueID, Title: input.Title, Summary: input.Summary, Content: input.Content,
		Status: domain.DecisionStatus(input.Status), SupersedesID: input.SupersedesID,
		SessionID: adapter.sessionIDForRequest(ctx, request),
	})
	if err != nil {
		return adapter.failure(err)
	}
	return success(recordDecisionOutput{
		Decision:             recordDecisionDTOFromDomain(result.Decision),
		SupersededDecisionID: copyString(result.SupersededDecisionID),
	}, "decision recorded")
}

func (adapter *adapter) listDecisions(ctx context.Context, request *sdkmcp.CallToolRequest, input listDecisionsInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.decisions.ListDecisions(ctx, domain.ListDecisionsInput{
		IssueID: input.IssueID, Limit: input.Limit, Cursor: stringValue(input.Cursor),
	})
	if err != nil {
		return adapter.failure(err)
	}
	return success(decisionListOutputFromDomain(result), "decisions listed")
}

func (adapter *adapter) getIssueActivity(ctx context.Context, request *sdkmcp.CallToolRequest, input getIssueActivityInput) (*sdkmcp.CallToolResult, any, error) {
	activity, err := adapter.activities.GetIssueActivity(ctx, getIssueActivityInputToDomain(input))
	if err != nil {
		return adapter.failure(err)
	}
	return success(issueActivityOutputFromDomain(activity), "issue activity returned")
}

func (adapter *adapter) getIssueGraph(ctx context.Context, request *sdkmcp.CallToolRequest, input getIssueGraphInput) (*sdkmcp.CallToolResult, any, error) {
	relationTypes := make([]domain.RelationType, len(input.RelationTypes))
	for index, relationType := range input.RelationTypes {
		relationTypes[index] = domain.RelationType(relationType)
	}
	graph, err := adapter.graphs.GetIssueGraph(ctx, domain.GetIssueGraphInput{
		RootIssueID: input.RootIssueID, Depth: input.Depth, Direction: domain.GraphDirection(input.Direction),
		RelationTypes: relationTypes, IncludeHierarchy: input.IncludeHierarchy, IncludeTerminal: input.IncludeTerminal,
		MaxNodes: input.MaxNodes, View: input.View,
	})
	if err != nil {
		return adapter.failure(err)
	}
	output := graphOutputFromDomain(graph)
	output.NextActions = []string{"Inspect a node with get_work_context."}
	return success(output, "issue graph returned")
}

func (adapter *adapter) getPlanningGraph(ctx context.Context, request *sdkmcp.CallToolRequest, input getPlanningGraphInput) (*sdkmcp.CallToolResult, any, error) {
	graph, err := adapter.graphs.GetPlanningGraph(ctx, domain.GetPlanningGraphInput{
		RootIssueID: input.RootIssueID, Depth: input.Depth, MaxNodes: input.MaxNodes,
		IncludeReview: input.IncludeReview, IncludeRelated: input.IncludeRelated,
	})
	if err != nil {
		return adapter.failure(err)
	}
	output := graphOutputFromDomain(graph)
	output.NextActions = []string{"Inspect an entry point with get_work_context."}
	return success(output, "planning graph returned")
}

func (adapter *adapter) exportProject(ctx context.Context, request *sdkmcp.CallToolRequest, input exportProjectInput) (*sdkmcp.CallToolResult, any, error) {
	if input.Delivery != "" && input.Delivery != "artifact" && input.Delivery != "inline" {
		return adapter.failure(domain.NewError(domain.CodeInvalidArgument, "delivery must be artifact or inline", false, domain.Detail{Field: "delivery", Code: "INVALID_ENUM"}))
	}
	data, err := adapter.projects.ExportLogicalProject(ctx)
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
		return success(document, "project export returned")
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
	return success(artifact, "project export artifact returned")
}

func (adapter *adapter) validateImport(ctx context.Context, request *sdkmcp.CallToolRequest, input validateImportInput) (*sdkmcp.CallToolResult, any, error) {
	document, err := adapter.importDocument(input.Document, input.SourceURI)
	if err != nil {
		return adapter.failure(err)
	}
	dryRun, err := adapter.projects.ValidateLogicalProjectImport(ctx, document)
	if err != nil {
		return adapter.failure(err)
	}
	return success(dryRun, "import validation dry run returned")
}

func (adapter *adapter) applyImport(ctx context.Context, request *sdkmcp.CallToolRequest, input applyImportInput) (*sdkmcp.CallToolResult, any, error) {
	document, err := adapter.importDocument(input.Document, input.SourceURI)
	if err != nil {
		return adapter.failure(err)
	}
	result, err := adapter.projects.ApplyLogicalProjectImport(ctx, document)
	if err != nil {
		return adapter.failure(err)
	}
	return success(result, "import apply result returned")
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

func (adapter *adapter) getProject(ctx context.Context, request *sdkmcp.CallToolRequest, input getProjectInput) (*sdkmcp.CallToolResult, any, error) {
	project, err := adapter.projects.GetProject(ctx)
	if err != nil {
		return adapter.failure(err)
	}
	output := projectOutput{
		ProjectRef:             ProjectRefFromContext(ctx),
		Project:                projectDTOFromDomain(project, input.IncludeInstructions),
		Session:                nil,
		AppVersion:             adapter.appVersion,
		SchemaVersion:          project.SchemaVersion,
		ConfigVersion:          adapter.configVersion,
		ToolProfile:            string(adapter.toolProfile),
		Limits:                 limitsDTO{DefaultIssueListLimit: 20, DefaultLabelListLimit: 50, MaxCollectionLimit: 100},
		SupportedIssueTypes:    []string{"epic", "task", "bug"},
		SupportedStatuses:      []string{"open", "ready", "blocked", "review", "done", "cancelled"},
		SupportedRelationTypes: []string{"blocks", "related_to", "duplicates"},
		SupportedPriorities:    []string{"low", "medium", "high", "critical"},
		LatestEventID:          project.LatestEventID,
		Guides:                 guideLinks(),
		NextActions:            []string{"Retain project_ref and pass it to later project-scoped calls; read rhizome://guides/agent-workflow."},
	}
	return success(output, "project metadata returned")
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
	project, err := lease.ProjectService().GetProject(ctx)
	if err != nil {
		return adapter.failure(err)
	}
	output := projectOutput{
		ProjectRef:             lease.ProjectRef(),
		Project:                projectDTOFromDomain(project, false),
		Session:                nil,
		AppVersion:             adapter.appVersion,
		SchemaVersion:          project.SchemaVersion,
		ConfigVersion:          adapter.configVersion,
		ToolProfile:            string(adapter.toolProfile),
		Limits:                 limitsDTO{DefaultIssueListLimit: 20, DefaultLabelListLimit: 50, MaxCollectionLimit: 100},
		SupportedIssueTypes:    []string{"epic", "task", "bug"},
		SupportedStatuses:      []string{"open", "ready", "blocked", "review", "done", "cancelled"},
		SupportedRelationTypes: []string{"blocks", "related_to", "duplicates"},
		SupportedPriorities:    []string{"low", "medium", "high", "critical"},
		LatestEventID:          project.LatestEventID,
		Guides:                 guideLinks(),
		NextActions:            []string{"Retain project_ref and pass it to later project-scoped calls; read rhizome://guides/agent-workflow."},
	}
	return success(output, "project opened")
}

func (adapter *adapter) manageIssueRelation(ctx context.Context, request *sdkmcp.CallToolRequest, input manageIssueRelationInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.relations.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
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
	return success(manageIssueRelationOutput{
		Relation: relationDTOFromDomain(result.Relation), AffectedIssues: affected, Changed: result.Changed,
	}, summary)
}

func (adapter *adapter) listLabels(ctx context.Context, request *sdkmcp.CallToolRequest, input listLabelsInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.issues.ListLabels(ctx, domain.ListLabelsInput{
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
	return success(labelListOutput{Items: items, NextCursor: result.NextCursor, HasMore: result.HasMore}, "labels listed")
}

func (adapter *adapter) createIssue(ctx context.Context, request *sdkmcp.CallToolRequest, input createIssueInput) (*sdkmcp.CallToolResult, any, error) {
	if input.View != "" && input.View != "compact" && input.View != "full" {
		return adapter.failure(unsupportedField("view"))
	}
	result, err := adapter.issues.CreateIssue(ctx, domain.CreateIssueInput{
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
	view := input.View
	if view == "" {
		view = "compact"
	}
	if view == "full" {
		return success(issueDTOFromDomain(result.Issue), "issue created")
	}
	return success(createIssueCompactOutputFromDomain(result.Issue), "issue created")
}

func (adapter *adapter) updateIssue(ctx context.Context, request *sdkmcp.CallToolRequest, input updateIssueInput) (*sdkmcp.CallToolResult, any, error) {
	if input.View != "" && input.View != "compact" && input.View != "full" {
		return adapter.failure(unsupportedField("view"))
	}
	result, err := adapter.issues.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID:             input.IssueID,
		ExpectedVersion:     input.ExpectedVersion,
		Changes:             input.Changes.domainPatch(),
		CreateMissingLabels: input.CreateMissingLabels,
		IdempotencyKey:      input.IdempotencyKey,
	})
	if err != nil {
		return adapter.failure(err)
	}
	view := input.View
	if view == "" {
		view = "compact"
	}
	if view == "full" {
		return success(updateIssueOutput{Issue: issueDTOFromDomain(result.Issue), ChangedFields: result.ChangedFields}, "issue updated")
	}
	return success(updateIssueCompactOutputFromDomain(result.Issue, result.ChangedFields), "issue updated")
}

func (adapter *adapter) getIssue(ctx context.Context, request *sdkmcp.CallToolRequest, input getIssueInput) (*sdkmcp.CallToolResult, any, error) {
	if input.View != "" && input.View != "compact" && input.View != "standard" && input.View != "full" {
		return adapter.failure(unsupportedField("view"))
	}
	if len(input.Include) != 0 {
		return adapter.failure(unsupportedField("include"))
	}
	if len(input.Limits) != 0 {
		return adapter.failure(unsupportedField("limits"))
	}
	issue, err := adapter.issues.GetIssue(ctx, input.IssueID)
	if err != nil {
		return adapter.failure(err)
	}
	view := input.View
	if view == "" {
		view = "standard"
	}
	switch view {
	case "compact":
		return success(issueCompactProjectionDTOFromDomain(issue), "issue returned")
	case "standard":
		return success(issueStandardProjectionDTOFromDomain(issue), "issue returned")
	case "full":
		return success(issueDTOFromDomain(issue), "issue returned")
	default:
		return adapter.failure(unsupportedField("view"))
	}
}

func (adapter *adapter) listIssues(ctx context.Context, request *sdkmcp.CallToolRequest, input listIssuesInput) (*sdkmcp.CallToolResult, any, error) {
	view := input.View
	if view == "" {
		view = "compact"
	}
	if view != "compact" && view != "full" {
		return adapter.failure(unsupportedField("view"))
	}
	result, err := adapter.issues.ListIssues(ctx, domain.ListIssuesInput{
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
		return success(issueListOutput{
			Items: items, NextCursor: result.NextCursor, HasMore: result.HasMore, NextActions: nextActions,
		}, "issues listed")
	}
	items := make([]issueListItemCompactDTO, len(result.Items))
	for i, item := range result.Items {
		items[i] = issueListItemCompactDTOFromDomain(item)
	}
	return success(issueListCompactOutput{
		Items: items, NextCursor: result.NextCursor, HasMore: result.HasMore, NextActions: nextActions,
	}, "issues listed")
}

func (adapter *adapter) archiveIssue(ctx context.Context, request *sdkmcp.CallToolRequest, input archiveIssueInput) (*sdkmcp.CallToolResult, any, error) {
	if input.View != "" && input.View != "compact" && input.View != "full" {
		return adapter.failure(unsupportedField("view"))
	}
	result, err := adapter.issues.ArchiveIssue(ctx, domain.ArchiveIssueInput{
		IssueID:         input.IssueID,
		ExpectedVersion: input.ExpectedVersion,
		IdempotencyKey:  input.IdempotencyKey,
	})
	if err != nil {
		return adapter.failure(err)
	}
	view := input.View
	if view == "" {
		view = "compact"
	}
	if view == "full" {
		return success(issueDTOFromDomain(result.Issue), "issue archived")
	}
	return success(archiveIssueCompactOutputFromDomain(result.Issue), "issue archived")
}

func (adapter *adapter) getReviewRequest(ctx context.Context, request *sdkmcp.CallToolRequest, input getReviewRequestInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.reviews.GetReviewRequest(ctx, input.ReviewRequestID)
	if err != nil {
		return adapter.failure(err)
	}
	return success(reviewRequestDTOFromDomain(result.Request, result.Claimable), "review request read")
}

func (adapter *adapter) listReviewRequests(ctx context.Context, request *sdkmcp.CallToolRequest, input listReviewRequestsInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.reviews.ListReviewRequests(ctx, application.ListReviewRequestsInput{
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
	return success(output, "review requests listed")
}

func (adapter *adapter) cancelReviewRequest(ctx context.Context, request *sdkmcp.CallToolRequest, input cancelReviewRequestInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.reviews.CancelReviewRequest(ctx, application.ReviewMutationInput{RequestID: input.ReviewRequestID, ExpectedVersion: input.ExpectedVersion})
	if err != nil {
		return adapter.failure(err)
	}
	return success(reviewRequestDTOFromDomain(result.Request, result.Claimable), "review request cancelled")
}

func (adapter *adapter) replaceReviewRequest(ctx context.Context, request *sdkmcp.CallToolRequest, input replaceReviewRequestInput) (*sdkmcp.CallToolResult, any, error) {
	result, err := adapter.reviews.ReplaceReviewRequest(ctx, application.ReplaceReviewRequestInput{
		PredecessorRequestID:       input.PredecessorRequestID,
		PredecessorExpectedVersion: input.PredecessorExpectedVersion,
		TargetIssueVersion:         input.TargetIssueVersion,
		TargetEventID:              input.TargetEventID,
		ArtifactIDs:                append([]string(nil), input.ArtifactIDs...),
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
	return success(output, "review request replaced")
}

func success(output any, summary string) (*sdkmcp.CallToolResult, any, error) {
	return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: summary}}, StructuredContent: output}, nil, nil
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
	return &sdkmcp.CallToolResult{
		Content:           []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("%s: %s", output.Code, output.Message)}},
		StructuredContent: output,
		IsError:           true,
	}, nil, nil
}

func validateProjectServices(services ProjectServices) error {
	if services.IssueService == nil {
		return domain.NewError(domain.CodeInvalidArgument, "issue service is required", false)
	}
	if services.ProjectService == nil {
		return domain.NewError(domain.CodeInvalidArgument, "project service is required", false)
	}
	if services.RelationService == nil {
		return domain.NewError(domain.CodeInvalidArgument, "relation service is required", false)
	}
	if services.GraphService == nil {
		return domain.NewError(domain.CodeInvalidArgument, "graph service is required", false)
	}
	if services.PlanningService == nil {
		return domain.NewError(domain.CodeInvalidArgument, "planning service is required", false)
	}
	if services.CommentService == nil {
		return domain.NewError(domain.CodeInvalidArgument, "comment service is required", false)
	}
	if services.DecisionService == nil {
		return domain.NewError(domain.CodeInvalidArgument, "decision service is required", false)
	}
	if services.ActivityService == nil {
		return domain.NewError(domain.CodeInvalidArgument, "activity service is required", false)
	}
	if services.SearchService == nil {
		return domain.NewError(domain.CodeInvalidArgument, "search service is required", false)
	}
	if services.ReviewService == nil {
		return domain.NewError(domain.CodeInvalidArgument, "review service is required", false)
	}
	if services.AttemptService == nil {
		return domain.NewError(domain.CodeInvalidArgument, "attempt service is required", false)
	}
	if services.SessionService == nil {
		return domain.NewError(domain.CodeInvalidArgument, "session service is required", false)
	}
	if services.WorkContextService == nil {
		return domain.NewError(domain.CodeInvalidArgument, "work context service is required", false)
	}
	return nil
}

func unsupportedField(field string) *domain.Error {
	return domain.NewError(domain.CodeInvalidArgument, fmt.Sprintf("field %q is not supported", field), false,
		domain.Detail{Field: field, Code: "UNSUPPORTED"})
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
