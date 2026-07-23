// Package mcp exposes application services through MCP tools.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/domain"
)

// Options supplies the explicit composition dependencies for the MCP adapter.
type Options struct {
	IssueService       *application.IssueService
	ProjectService     *application.ProjectService
	RelationService    *application.RelationService
	GraphService       *application.GraphService
	PlanningService    *application.PlanningService
	CommentService     *application.CommentService
	DecisionService    *application.DecisionService
	ActivityService    *application.ActivityService
	SearchService      *application.SearchService
	ReviewService      *application.ReviewService
	AttemptService     *application.AttemptService
	SessionService     *application.AgentSessionService
	WorkContextService *application.WorkContextService
	ServerName         string
	ServerVersion      string
	ConfigVersion      int
}

type adapter struct {
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

	sessionMu            sync.Mutex
	connectionSessions   map[*sdkmcp.ServerSession]string
	sdkSessionIDs        map[string]string
	sessionStarted       map[*sdkmcp.ServerSession]struct{}
	sessionEnded         map[*sdkmcp.ServerSession]struct{}
	endedDurableSessions map[string]struct{}
}

// Server owns the MCP SDK server and its adapter lifecycle tracking.
type Server struct {
	server  *sdkmcp.Server
	adapter *adapter
}

// NewServer composes the MCP server without process-global dependencies.
func NewServer(options Options) (*Server, error) {
	if options.IssueService == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "issue service is required", false)
	}
	if options.ProjectService == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "project service is required", false)
	}
	if options.RelationService == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "relation service is required", false)
	}
	if options.GraphService == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "graph service is required", false)
	}
	if options.PlanningService == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "planning service is required", false)
	}
	if options.CommentService == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "comment service is required", false)
	}
	if options.DecisionService == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "decision service is required", false)
	}
	if options.ActivityService == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "activity service is required", false)
	}
	if options.SearchService == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "search service is required", false)
	}
	if options.ReviewService == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "review service is required", false)
	}
	if options.AttemptService == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "attempt service is required", false)
	}
	if options.SessionService == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "session service is required", false)
	}
	if options.WorkContextService == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "work context service is required", false)
	}
	if options.ServerName == "" {
		return nil, domain.NewError(domain.CodeInvalidArgument, "server name is required", false)
	}
	if options.ServerVersion == "" {
		return nil, domain.NewError(domain.CodeInvalidArgument, "server version is required", false)
	}
	adapter := &adapter{
		issues:               options.IssueService,
		projects:             options.ProjectService,
		relations:            options.RelationService,
		graphs:               options.GraphService,
		plans:                options.PlanningService,
		comments:             options.CommentService,
		decisions:            options.DecisionService,
		activities:           options.ActivityService,
		searches:             options.SearchService,
		reviews:              options.ReviewService,
		attempts:             options.AttemptService,
		sessions:             options.SessionService,
		workContexts:         options.WorkContextService,
		appVersion:           options.ServerVersion,
		configVersion:        options.ConfigVersion,
		connectionSessions:   make(map[*sdkmcp.ServerSession]string),
		sdkSessionIDs:        make(map[string]string),
		sessionStarted:       make(map[*sdkmcp.ServerSession]struct{}),
		sessionEnded:         make(map[*sdkmcp.ServerSession]struct{}),
		endedDurableSessions: make(map[string]struct{}),
	}
	server := sdkmcp.NewServer(
		&sdkmcp.Implementation{Name: options.ServerName, Version: options.ServerVersion},
		&sdkmcp.ServerOptions{
			Capabilities:       &sdkmcp.ServerCapabilities{Tools: &sdkmcp.ToolCapabilities{}},
			Instructions:       initializeInstructions,
			InitializedHandler: adapter.startSession,
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

// EndSession removes a durable agent session associated with an SDK session ID.
func (server *Server) EndSession(ctx context.Context, sdkSessionID string) error {
	if server == nil || sdkSessionID == "" {
		return nil
	}
	endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return server.adapter.endSessionBySDKSessionID(endCtx, sdkSessionID)
}

// Run serves one MCP connection and records its durable session lifecycle.
func (server *Server) Run(ctx context.Context, transport sdkmcp.Transport) error {
	sdkSession, err := server.server.Connect(ctx, transport, nil)
	if err != nil {
		return err
	}
	defer func() {
		endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		server.adapter.endSession(endCtx, sdkSession)
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

func (adapter *adapter) startSession(ctx context.Context, request *sdkmcp.InitializedRequest) {
	if request == nil || request.Session == nil {
		return
	}
	sdkSession := request.Session

	adapter.sessionMu.Lock()
	if _, started := adapter.sessionStarted[sdkSession]; started {
		adapter.sessionMu.Unlock()
		return
	}
	adapter.sessionStarted[sdkSession] = struct{}{}
	adapter.sessionMu.Unlock()

	clientName := "unknown"
	var clientVersion *string
	if params := sdkSession.InitializeParams(); params != nil && params.ClientInfo != nil {
		if name := strings.TrimSpace(params.ClientInfo.Name); name != "" {
			clientName = name
			if version := strings.TrimSpace(params.ClientInfo.Version); version != "" {
				clientVersion = &version
			}
		}
	}
	created, err := adapter.sessions.Create(ctx, domain.CreateAgentSessionInput{
		ClientName: clientName, ClientVersion: clientVersion,
	})
	if err != nil {
		slog.Error("agent session creation failed", "error", err)
		return
	}
	sdkSessionID := sdkSession.ID()
	adapter.sessionMu.Lock()
	ended := false
	if _, ok := adapter.sessionEnded[sdkSession]; ok {
		ended = true
	} else {
		adapter.connectionSessions[sdkSession] = created.ID
		if sdkSessionID != "" {
			adapter.sdkSessionIDs[sdkSessionID] = created.ID
		}
	}
	adapter.sessionMu.Unlock()
	if ended {
		endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := adapter.sessions.End(endCtx, created.ID); err != nil && !isContextCancellation(err) {
			slog.Error("agent session end failed", "error", err)
		}
	}
}

func (adapter *adapter) touchSession(ctx context.Context, sdkSession *sdkmcp.ServerSession) {
	sessionID := adapter.sessionIDFor(sdkSession)
	if sessionID == nil {
		return
	}
	if _, err := adapter.sessions.Touch(ctx, *sessionID); err != nil && !isContextCancellation(err) {
		slog.Error("agent session touch failed", "error", err)
	}
}

func (adapter *adapter) sessionIDFor(sdkSession *sdkmcp.ServerSession) *string {
	if sdkSession == nil {
		return nil
	}
	adapter.sessionMu.Lock()
	id := adapter.connectionSessions[sdkSession]
	adapter.sessionMu.Unlock()
	if id == "" {
		return nil
	}
	copy := id
	return &copy
}

func (adapter *adapter) endSession(ctx context.Context, sdkSession *sdkmcp.ServerSession) {
	if sdkSession == nil {
		return
	}
	adapter.sessionMu.Lock()
	if _, ok := adapter.sessionEnded[sdkSession]; ok {
		adapter.sessionMu.Unlock()
		return
	}
	adapter.sessionEnded[sdkSession] = struct{}{}
	sessionID := adapter.connectionSessions[sdkSession]
	delete(adapter.connectionSessions, sdkSession)
	if sdkSessionID := sdkSession.ID(); sdkSessionID != "" {
		delete(adapter.sdkSessionIDs, sdkSessionID)
	}
	if sessionID != "" {
		adapter.endedDurableSessions[sessionID] = struct{}{}
	}
	adapter.sessionMu.Unlock()
	if sessionID == "" {
		return
	}
	if _, err := adapter.sessions.End(ctx, sessionID); err != nil && !isContextCancellation(err) {
		slog.Error("agent session end failed", "error", err)
	}
}

// releaseConnectionsLocked drops per-connection tracking for every connection
// carrying the SDK session ID and reports the durable session IDs they held.
// A transport that serves many sessions from one server reuses the adapter, so
// terminated connections must not stay reachable from any tracking map.
// The caller must hold sessionMu.
func (adapter *adapter) releaseConnectionsLocked(sdkSessionID string) []string {
	var durableSessionIDs []string
	for sdkSession, sessionID := range adapter.connectionSessions {
		if sdkSession == nil || sdkSession.ID() != sdkSessionID {
			continue
		}
		delete(adapter.connectionSessions, sdkSession)
		delete(adapter.sessionStarted, sdkSession)
		if sessionID != "" {
			durableSessionIDs = append(durableSessionIDs, sessionID)
		}
	}
	return durableSessionIDs
}

func (adapter *adapter) endSessionBySDKSessionID(ctx context.Context, sdkSessionID string) error {
	if sdkSessionID == "" {
		return nil
	}
	adapter.sessionMu.Lock()
	durableSessionID, ok := adapter.sdkSessionIDs[sdkSessionID]
	if !ok {
		for _, sessionID := range adapter.releaseConnectionsLocked(sdkSessionID) {
			adapter.endedDurableSessions[sessionID] = struct{}{}
		}
		adapter.sessionMu.Unlock()
		return nil
	}
	delete(adapter.sdkSessionIDs, sdkSessionID)
	adapter.releaseConnectionsLocked(sdkSessionID)
	if _, ended := adapter.endedDurableSessions[durableSessionID]; ended {
		adapter.sessionMu.Unlock()
		return nil
	}
	adapter.endedDurableSessions[durableSessionID] = struct{}{}
	adapter.sessionMu.Unlock()
	if durableSessionID == "" {
		return nil
	}
	_, err := adapter.sessions.End(ctx, durableSessionID)
	if err != nil && !isContextCancellation(err) {
		slog.Error("agent session end failed", "error", err)
	}
	return err
}

func isContextCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (adapter *adapter) register(server *sdkmcp.Server) {
	sdkmcp.AddTool(server, tool("get_project", "Get project metadata, limits, supported values, event position, and guide links.", schemaGetProject(), schemaProjectOutput()), adapter.getProject)
	sdkmcp.AddTool(server, tool("export_project", "Export the current project as the version 1 logical interchange document.", schemaExportProject(), schemaExportProjectOutput()), adapter.exportProject)
	sdkmcp.AddTool(server, tool("validate_import", "Validate a logical project import document without writing anything.", schemaValidateImport(), schemaValidateImportOutput()), adapter.validateImport)
	sdkmcp.AddTool(server, tool("apply_import", "Apply a validated logical project import document into an empty destination.", schemaApplyImport(), schemaApplyImportOutput()), adapter.applyImport)
	sdkmcp.AddTool(server, tool("list_labels", "List reusable labels with optional name search and cursor pagination.", schemaListLabels(), schemaLabelListOutput()), adapter.listLabels)
	sdkmcp.AddTool(server, tool("create_issue", "Create one epic, task, or bug with optional hierarchy and labels.", schemaCreateIssue(), schemaIssueOutput()), adapter.createIssue)
	sdkmcp.AddTool(server, tool("update_issue", "Patch one issue using its current version for optimistic concurrency.", schemaUpdateIssue(), schemaUpdateOutput()), adapter.updateIssue)
	sdkmcp.AddTool(server, tool("get_issue", "Get the current issue record by ULID or ISSUE-N display ID.", schemaGetIssue(), schemaIssueOutput()), adapter.getIssue)
	sdkmcp.AddTool(server, tool("list_issues", "List and filter issues, including effective status, blockers, and claimability.", schemaListIssues(), schemaIssueListOutput()), adapter.listIssues)
	sdkmcp.AddTool(server, tool("archive_issue", "Archive one issue using its current version; history remains available.", schemaArchiveIssue(), schemaIssueOutput()), adapter.archiveIssue)
	sdkmcp.AddTool(server, tool("cancel_review_request", "Cancel an open or claimed review request using its current version.", schemaCancelReviewRequest(), schemaReviewRequestOutput()), adapter.cancelReviewRequest)
	sdkmcp.AddTool(server, tool("create_review_request", "Create a review request for an exact issue version, event position, and artifact set.", schemaCreateReviewRequest(), schemaReviewRequestOutput()), adapter.createReviewRequest)
	sdkmcp.AddTool(server, tool("get_review_request", "Get one review request by identifier.", schemaGetReviewRequest(), schemaReviewRequestOutput()), adapter.getReviewRequest)
	sdkmcp.AddTool(server, tool("list_review_requests", "List review requests with optional status and claimability filters.", schemaListReviewRequests(), schemaReviewRequestListOutput()), adapter.listReviewRequests)
	sdkmcp.AddTool(server, tool("manage_issue_relation", "Add or remove one blocks, related_to, or duplicates relation.", schemaManageIssueRelation(), schemaManageIssueRelationOutput()), adapter.manageIssueRelation)
	sdkmcp.AddTool(server, tool("supersede_review_request", "Supersede an open or claimed review request using its current version.", schemaSupersedeReviewRequest(), schemaReviewRequestOutput()), adapter.supersedeReviewRequest)
	sdkmcp.AddTool(server, tool("get_issue_graph", "Get a bounded relation and hierarchy graph around one issue.", schemaGetIssueGraph(), schemaGraphOutput()), adapter.getIssueGraph)
	sdkmcp.AddTool(server, tool("get_planning_graph", "Get dependency-aware entry points and blocking nodes for work selection.", schemaGetPlanningGraph(), schemaGraphOutput()), adapter.getPlanningGraph)
	sdkmcp.AddTool(server, tool("validate_issue_plan", "Normalize and validate a bounded multi-issue plan without writing it.", schemaValidateIssuePlan(), schemaPlanValidationOutput()), adapter.validateIssuePlan)
	sdkmcp.AddTool(server, tool("apply_issue_plan", "Atomically create issues, relations, and decisions from a valid plan.", schemaApplyIssuePlan(), schemaApplyIssuePlanOutput()), adapter.applyIssuePlan)
	sdkmcp.AddTool(server, tool("add_comment", "Append collaboration context to an issue without rewriting history.", schemaAddComment(), schemaAddCommentOutput()), adapter.addComment)
	sdkmcp.AddTool(server, tool("record_decision", "Append a durable project or issue decision, optionally superseding one.", schemaRecordDecision(), schemaRecordDecisionOutput()), adapter.recordDecision)
	sdkmcp.AddTool(server, tool("list_decisions", "List project-wide or issue-scoped decisions with cursor pagination.", schemaListDecisions(), schemaDecisionListOutput()), adapter.listDecisions)
	sdkmcp.AddTool(server, tool("get_issue_activity", "Get a unified newest-first timeline of issue work and artifacts.", schemaGetIssueActivity(), schemaGetIssueActivityOutput()), adapter.getIssueActivity)
	sdkmcp.AddTool(server, tool("claim_issue", "Claim claimable ready or review work and receive a renewable lease token.", schemaClaimIssue(), schemaClaimIssueOutput()), adapter.claimIssue)
	sdkmcp.AddTool(server, tool("renew_attempt", "Extend an active work or review lease before it expires.", schemaRenewAttempt(), schemaRenewAttemptOutput()), adapter.renewAttempt)
	sdkmcp.AddTool(server, tool("save_attempt_note", "Append a restartable checkpoint, finding, warning, or progress note.", schemaSaveAttemptNote(), schemaSaveAttemptNoteOutput()), adapter.saveAttemptNote)
	sdkmcp.AddTool(server, tool("finish_attempt", "End a leased attempt with outcome, verification, artifacts, and status.", schemaFinishAttempt(), schemaFinishAttemptOutput()), adapter.finishAttempt)
	sdkmcp.AddTool(server, tool("get_work_context", "Get bounded task, blocker, decision, checkpoint, and recovery context.", schemaGetWorkContext(), schemaGetWorkContextOutput()), adapter.getWorkContext)
	sdkmcp.AddTool(server, tool("search", "Full-text search issues, comments, decisions, and attempt notes.", schemaSearch(), schemaSearchOutput()), adapter.search)
	sdkmcp.AddTool(server, tool("get_changes", "Get ordered issue events after an event ID for incremental synchronization.", schemaGetChanges(), schemaChangesOutput()), adapter.getChanges)
}

func (adapter *adapter) search(ctx context.Context, request *sdkmcp.CallToolRequest, input searchInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
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
	adapter.touchSession(ctx, request.Session)
	result, err := adapter.searches.GetChanges(ctx, domain.GetChangesInput{
		SinceEventID: input.SinceEventID, IssueID: input.IssueID, EventTypes: input.EventTypes, Limit: input.Limit,
	})
	if err != nil {
		return adapter.failure(err)
	}
	return success(changesOutputFromDomain(result), "changes returned")
}

func (adapter *adapter) getWorkContext(ctx context.Context, request *sdkmcp.CallToolRequest, input getWorkContextInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
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
	adapter.touchSession(ctx, request.Session)
	sessionID := adapter.sessionIDFor(request.Session)
	result, err := adapter.attempts.ClaimIssue(ctx, domain.ClaimIssueInput{IssueID: input.IssueID, LeaseSeconds: input.LeaseSeconds, SessionID: sessionID, IdempotencyKey: input.IdempotencyKey})
	if err != nil {
		return adapter.failure(err)
	}
	attempt := attemptDTOFromDomain(result.Attempt)
	return success(claimIssueOutput{
		Issue: issueListItemDTO{issueDTO: issueDTOFromDomain(result.Issue), EffectiveStatus: string(domain.EffectiveStatusInProgress),
			UnresolvedBlockerCount: 0, IsBlocked: false, IsClaimable: false, ActiveAttemptID: &result.Attempt.ID},
		Attempt: attempt, LeaseToken: result.LeaseToken, LeaseExpiresAt: result.Attempt.LeaseExpiresAt,
		MinimalWorkContext: emptyWorkContextDTO{}, Warnings: []string{},
		NextActions: []string{"Renew before expiry; finish_attempt on every exit."},
	}, "issue claimed")
}

func (adapter *adapter) renewAttempt(ctx context.Context, request *sdkmcp.CallToolRequest, input renewAttemptInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
	sessionID := adapter.sessionIDFor(request.Session)
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
	adapter.touchSession(ctx, request.Session)
	sessionID := adapter.sessionIDFor(request.Session)
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
	adapter.touchSession(ctx, request.Session)
	sessionID := adapter.sessionIDFor(request.Session)
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
	outputArtifacts := make([]artifactDTO, len(result.Artifacts))
	for index, artifact := range result.Artifacts {
		outputArtifacts[index] = artifactDTOFromDomain(artifact)
	}
	return success(finishAttemptOutput{Attempt: attemptDTOFromDomain(result.Attempt), Issue: issueDTOFromDomain(result.Issue),
		Warnings: append([]string{}, result.Warnings...), LatestEventID: result.LatestEventID, Artifacts: outputArtifacts,
		NextActions: []string{"Select new work from get_planning_graph."}}, "attempt finished")
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
	adapter.touchSession(ctx, request.Session)
	validation, err := adapter.plans.ValidateIssuePlan(ctx, input.domainPlan())
	if err != nil {
		return adapter.failure(err)
	}
	output := planValidationOutputFromDomain(validation)
	if output.Valid {
		output.NextActions = []string{"Apply normalized_plan with apply_issue_plan."}
	} else {
		output.NextActions = []string{"Correct errors and validate again."}
	}
	return success(output, "issue plan validated")
}

func (adapter *adapter) applyIssuePlan(ctx context.Context, request *sdkmcp.CallToolRequest, input applyIssuePlanInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
	result, err := adapter.plans.ApplyIssuePlan(ctx, input.domainPlan(), input.IdempotencyKey)
	if err != nil {
		return adapter.failure(err)
	}
	output := applyIssuePlanOutputFromPort(result)
	output.NextActions = []string{"Use get_planning_graph to select executable work."}
	return success(output, "issue plan applied")
}

func (adapter *adapter) addComment(ctx context.Context, request *sdkmcp.CallToolRequest, input addCommentInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
	comment, err := adapter.comments.AddComment(ctx, domain.AddCommentInput{
		IssueID: input.IssueID, Content: input.Content, SessionID: adapter.sessionIDFor(request.Session),
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return adapter.failure(err)
	}
	return success(addCommentOutput{Comment: commentDTOFromDomain(comment)}, "comment added")
}

func (adapter *adapter) recordDecision(ctx context.Context, request *sdkmcp.CallToolRequest, input recordDecisionInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
	result, err := adapter.decisions.RecordDecision(ctx, domain.RecordDecisionInput{
		IssueID: input.IssueID, Title: input.Title, Summary: input.Summary, Content: input.Content,
		Status: domain.DecisionStatus(input.Status), SupersedesID: input.SupersedesID,
		SessionID: adapter.sessionIDFor(request.Session),
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
	adapter.touchSession(ctx, request.Session)
	result, err := adapter.decisions.ListDecisions(ctx, domain.ListDecisionsInput{
		IssueID: input.IssueID, Limit: input.Limit, Cursor: stringValue(input.Cursor),
	})
	if err != nil {
		return adapter.failure(err)
	}
	return success(decisionListOutputFromDomain(result), "decisions listed")
}

func (adapter *adapter) getIssueActivity(ctx context.Context, request *sdkmcp.CallToolRequest, input getIssueActivityInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
	activity, err := adapter.activities.GetIssueActivity(ctx, getIssueActivityInputToDomain(input))
	if err != nil {
		return adapter.failure(err)
	}
	return success(issueActivityOutputFromDomain(activity), "issue activity returned")
}

func (adapter *adapter) getIssueGraph(ctx context.Context, request *sdkmcp.CallToolRequest, input getIssueGraphInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
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
	adapter.touchSession(ctx, request.Session)
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
	adapter.touchSession(ctx, request.Session)
	data, err := adapter.projects.ExportLogicalProject(ctx)
	if err != nil {
		return adapter.failure(err)
	}
	var document domain.LogicalProjectDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return adapter.failure(domain.WrapError(err, domain.CodeStorageFailure, "logical project export could not be decoded", false))
	}
	return success(document, "project export returned")
}

func (adapter *adapter) validateImport(ctx context.Context, request *sdkmcp.CallToolRequest, input validateImportInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
	dryRun, err := adapter.projects.ValidateLogicalProjectImport(ctx, []byte(input.Document))
	if err != nil {
		return adapter.failure(err)
	}
	return success(dryRun, "import validation dry run returned")
}

func (adapter *adapter) applyImport(ctx context.Context, request *sdkmcp.CallToolRequest, input applyImportInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
	result, err := adapter.projects.ApplyLogicalProjectImport(ctx, []byte(input.Document))
	if err != nil {
		return adapter.failure(err)
	}
	return success(result, "import apply result returned")
}

func (adapter *adapter) getProject(ctx context.Context, request *sdkmcp.CallToolRequest, input getProjectInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
	project, err := adapter.projects.GetProject(ctx)
	if err != nil {
		return adapter.failure(err)
	}
	output := projectOutput{
		Project:                projectDTOFromDomain(project, input.IncludeInstructions),
		Session:                nil,
		AppVersion:             adapter.appVersion,
		SchemaVersion:          project.SchemaVersion,
		ConfigVersion:          adapter.configVersion,
		Limits:                 limitsDTO{DefaultIssueListLimit: 20, DefaultLabelListLimit: 50, MaxCollectionLimit: 100},
		SupportedIssueTypes:    []string{"epic", "task", "bug"},
		SupportedStatuses:      []string{"open", "ready", "blocked", "review", "done", "cancelled"},
		SupportedRelationTypes: []string{"blocks", "related_to", "duplicates"},
		SupportedPriorities:    []string{"low", "medium", "high", "critical"},
		LatestEventID:          project.LatestEventID,
		Guides:                 guideLinks(),
		NextActions:            []string{"Read rhizome://guides/agent-workflow; then find claimable work."},
	}
	return success(output, "project metadata returned")
}

func (adapter *adapter) manageIssueRelation(ctx context.Context, request *sdkmcp.CallToolRequest, input manageIssueRelationInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
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
	affected := make([]issueListItemDTO, len(result.AffectedIssues))
	for index, issue := range result.AffectedIssues {
		affected[index] = issueListItemDTO{
			issueDTO:               issueDTOFromDomain(issue.Issue),
			EffectiveStatus:        string(issue.EffectiveStatus),
			UnresolvedBlockerCount: issue.UnresolvedBlockerCount,
			IsBlocked:              issue.IsBlocked,
			IsClaimable:            issue.IsClaimable,
			ActiveAttemptID:        issue.ActiveAttemptID,
		}
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
	adapter.touchSession(ctx, request.Session)
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
	adapter.touchSession(ctx, request.Session)
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
	return success(issueDTOFromDomain(result.Issue), "issue created")
}

func (adapter *adapter) updateIssue(ctx context.Context, request *sdkmcp.CallToolRequest, input updateIssueInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
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
	return success(updateIssueOutput{Issue: issueDTOFromDomain(result.Issue), ChangedFields: result.ChangedFields}, "issue updated")
}

func (adapter *adapter) getIssue(ctx context.Context, request *sdkmcp.CallToolRequest, input getIssueInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
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
	return success(issueDTOFromDomain(issue), "issue returned")
}

func (adapter *adapter) listIssues(ctx context.Context, request *sdkmcp.CallToolRequest, input listIssuesInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
	if input.View != "" && input.View != "compact" {
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
	nextActions := []string{"Inspect a claimable issue with get_work_context."}
	if result.HasMore {
		nextActions = append(nextActions, "Continue with next_cursor.")
	}
	return success(issueListOutput{
		Items: items, NextCursor: result.NextCursor, HasMore: result.HasMore, NextActions: nextActions,
	}, "issues listed")
}

func (adapter *adapter) archiveIssue(ctx context.Context, request *sdkmcp.CallToolRequest, input archiveIssueInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
	result, err := adapter.issues.ArchiveIssue(ctx, domain.ArchiveIssueInput{
		IssueID:         input.IssueID,
		ExpectedVersion: input.ExpectedVersion,
		IdempotencyKey:  input.IdempotencyKey,
	})
	if err != nil {
		return adapter.failure(err)
	}
	return success(issueDTOFromDomain(result.Issue), "issue archived")
}

func (adapter *adapter) createReviewRequest(ctx context.Context, request *sdkmcp.CallToolRequest, input createReviewRequestInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
	result, err := adapter.reviews.CreateReviewRequest(ctx, application.CreateReviewRequestInput{
		IssueID:            input.IssueID,
		TargetIssueVersion: input.TargetIssueVersion,
		TargetEventID:      input.TargetEventID,
		ArtifactIDs:        append([]string(nil), input.ArtifactIDs...),
		SupersedesID:       copyReviewOptionalString(input.SupersedesID),
	})
	if err != nil {
		return adapter.failure(err)
	}
	return success(reviewRequestDTOFromDomain(result.Request, result.Claimable), "review request created")
}

func (adapter *adapter) getReviewRequest(ctx context.Context, request *sdkmcp.CallToolRequest, input getReviewRequestInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
	result, err := adapter.reviews.GetReviewRequest(ctx, input.ReviewRequestID)
	if err != nil {
		return adapter.failure(err)
	}
	return success(reviewRequestDTOFromDomain(result.Request, result.Claimable), "review request read")
}

func (adapter *adapter) listReviewRequests(ctx context.Context, request *sdkmcp.CallToolRequest, input listReviewRequestsInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
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
	adapter.touchSession(ctx, request.Session)
	result, err := adapter.reviews.CancelReviewRequest(ctx, application.ReviewMutationInput{RequestID: input.ReviewRequestID, ExpectedVersion: input.ExpectedVersion})
	if err != nil {
		return adapter.failure(err)
	}
	return success(reviewRequestDTOFromDomain(result.Request, result.Claimable), "review request cancelled")
}

func (adapter *adapter) supersedeReviewRequest(ctx context.Context, request *sdkmcp.CallToolRequest, input supersedeReviewRequestInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
	result, err := adapter.reviews.SupersedeReviewRequest(ctx, application.ReviewMutationInput{RequestID: input.ReviewRequestID, ExpectedVersion: input.ExpectedVersion})
	if err != nil {
		return adapter.failure(err)
	}
	return success(reviewRequestDTOFromDomain(result.Request, result.Claimable), "review request superseded")
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
