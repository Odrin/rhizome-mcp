// Package mcp exposes application services through MCP tools.
package mcp

import (
	"context"
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
	IssueService    *application.IssueService
	ProjectService  *application.ProjectService
	RelationService *application.RelationService
	GraphService    *application.GraphService
	PlanningService *application.PlanningService
	CommentService  *application.CommentService
	DecisionService *application.DecisionService
	AttemptService  *application.AttemptService
	SessionService  *application.AgentSessionService
	ServerName      string
	ServerVersion   string
	ConfigVersion   int
}

type adapter struct {
	issues        *application.IssueService
	projects      *application.ProjectService
	relations     *application.RelationService
	graphs        *application.GraphService
	plans         *application.PlanningService
	comments      *application.CommentService
	decisions     *application.DecisionService
	attempts      *application.AttemptService
	sessions      *application.AgentSessionService
	appVersion    string
	configVersion int

	sessionMu          sync.Mutex
	connectionSessions map[*sdkmcp.ServerSession]string
	sessionStarted     map[*sdkmcp.ServerSession]struct{}
	sessionEnded       map[*sdkmcp.ServerSession]struct{}
}

// Server owns the MCP SDK server and its adapter lifecycle tracking.
type Server struct {
	server  *sdkmcp.Server
	adapter *adapter
}

// NewServer composes a tools-only MCP server. It has no process-global
// dependencies and deliberately exposes no resources or prototype task tools.
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
	if options.AttemptService == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "attempt service is required", false)
	}
	if options.SessionService == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "session service is required", false)
	}
	if options.ServerName == "" {
		return nil, domain.NewError(domain.CodeInvalidArgument, "server name is required", false)
	}
	if options.ServerVersion == "" {
		return nil, domain.NewError(domain.CodeInvalidArgument, "server version is required", false)
	}
	adapter := &adapter{
		issues:             options.IssueService,
		projects:           options.ProjectService,
		relations:          options.RelationService,
		graphs:             options.GraphService,
		plans:              options.PlanningService,
		comments:           options.CommentService,
		decisions:          options.DecisionService,
		attempts:           options.AttemptService,
		sessions:           options.SessionService,
		appVersion:         options.ServerVersion,
		configVersion:      options.ConfigVersion,
		connectionSessions: make(map[*sdkmcp.ServerSession]string),
		sessionStarted:     make(map[*sdkmcp.ServerSession]struct{}),
		sessionEnded:       make(map[*sdkmcp.ServerSession]struct{}),
	}
	server := sdkmcp.NewServer(
		&sdkmcp.Implementation{Name: options.ServerName, Version: options.ServerVersion},
		&sdkmcp.ServerOptions{
			Capabilities:       &sdkmcp.ServerCapabilities{Tools: &sdkmcp.ToolCapabilities{}},
			InitializedHandler: adapter.startSession,
		},
	)
	adapter.register(server)
	return &Server{server: server, adapter: adapter}, nil
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
	adapter.sessionMu.Lock()
	ended := false
	if _, ok := adapter.sessionEnded[sdkSession]; ok {
		ended = true
	} else {
		adapter.connectionSessions[sdkSession] = created.ID
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
	adapter.sessionEnded[sdkSession] = struct{}{}
	sessionID := adapter.connectionSessions[sdkSession]
	delete(adapter.connectionSessions, sdkSession)
	adapter.sessionMu.Unlock()
	if sessionID == "" {
		return
	}
	if _, err := adapter.sessions.End(ctx, sessionID); err != nil && !isContextCancellation(err) {
		slog.Error("agent session end failed", "error", err)
	}
}

func isContextCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (adapter *adapter) register(server *sdkmcp.Server) {
	sdkmcp.AddTool(server, tool("get_project", "Return current project metadata and server capabilities", schemaGetProject(), schemaProjectOutput()), adapter.getProject)
	sdkmcp.AddTool(server, tool("list_labels", "List reusable labels in deterministic order", schemaListLabels(), schemaLabelListOutput()), adapter.listLabels)
	sdkmcp.AddTool(server, tool("create_issue", "Create an issue", schemaCreateIssue(), schemaIssueOutput()), adapter.createIssue)
	sdkmcp.AddTool(server, tool("update_issue", "Apply an optimistic issue patch", schemaUpdateIssue(), schemaUpdateOutput()), adapter.updateIssue)
	sdkmcp.AddTool(server, tool("get_issue", "Get an issue by internal or display ID", schemaGetIssue(), schemaIssueOutput()), adapter.getIssue)
	sdkmcp.AddTool(server, tool("list_issues", "List issues in deterministic order", schemaListIssues(), schemaIssueListOutput()), adapter.listIssues)
	sdkmcp.AddTool(server, tool("archive_issue", "Archive an issue with an optimistic version precondition", schemaArchiveIssue(), schemaIssueOutput()), adapter.archiveIssue)
	sdkmcp.AddTool(server, tool("manage_issue_relation", "Add or remove one relation between two issues", schemaManageIssueRelation(), schemaManageIssueRelationOutput()), adapter.manageIssueRelation)
	sdkmcp.AddTool(server, tool("get_issue_graph", "Return a bounded compact issue graph", schemaGetIssueGraph(), schemaGraphOutput()), adapter.getIssueGraph)
	sdkmcp.AddTool(server, tool("get_planning_graph", "Return a bounded planning graph", schemaGetPlanningGraph(), schemaGraphOutput()), adapter.getPlanningGraph)
	sdkmcp.AddTool(server, tool("validate_issue_plan", "Validate a bounded issue plan without changes", schemaValidateIssuePlan(), schemaPlanValidationOutput()), adapter.validateIssuePlan)
	sdkmcp.AddTool(server, tool("apply_issue_plan", "Atomically apply a validated issue plan", schemaApplyIssuePlan(), schemaApplyIssuePlanOutput()), adapter.applyIssuePlan)
	sdkmcp.AddTool(server, tool("add_comment", "Append a comment to an issue", schemaAddComment(), schemaAddCommentOutput()), adapter.addComment)
	sdkmcp.AddTool(server, tool("record_decision", "Record an append-only project or issue decision", schemaRecordDecision(), schemaRecordDecisionOutput()), adapter.recordDecision)
	sdkmcp.AddTool(server, tool("claim_issue", "Atomically claim a ready or review issue with a renewable lease", schemaClaimIssue(), schemaClaimIssueOutput()), adapter.claimIssue)
	sdkmcp.AddTool(server, tool("renew_attempt", "Renew an active attempt lease", schemaRenewAttempt(), schemaRenewAttemptOutput()), adapter.renewAttempt)
	sdkmcp.AddTool(server, tool("save_attempt_note", "Save an append-only note for an active leased attempt", schemaSaveAttemptNote(), schemaSaveAttemptNoteOutput()), adapter.saveAttemptNote)
	sdkmcp.AddTool(server, tool("finish_attempt", "Finish an active leased work or review attempt", schemaFinishAttempt(), schemaFinishAttemptOutput()), adapter.finishAttempt)
}

func (adapter *adapter) claimIssue(ctx context.Context, request *sdkmcp.CallToolRequest, input claimIssueInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
	sessionID := adapter.sessionIDFor(request.Session)
	if input.IdempotencyKey != nil {
		return adapter.failure(unsupportedField("idempotency_key"))
	}
	result, err := adapter.attempts.ClaimIssue(ctx, domain.ClaimIssueInput{IssueID: input.IssueID, LeaseSeconds: input.LeaseSeconds, SessionID: sessionID})
	if err != nil {
		return adapter.failure(err)
	}
	attempt := attemptDTOFromDomain(result.Attempt)
	return success(claimIssueOutput{
		Issue: issueListItemDTO{issueDTO: issueDTOFromDomain(result.Issue), EffectiveStatus: string(domain.EffectiveStatusInProgress),
			UnresolvedBlockerCount: 0, IsBlocked: false, IsClaimable: false, ActiveAttemptID: &result.Attempt.ID},
		Attempt: attempt, LeaseToken: result.LeaseToken, LeaseExpiresAt: result.Attempt.LeaseExpiresAt,
		MinimalWorkContext: emptyWorkContextDTO{}, Warnings: []string{},
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
	return success(renewAttemptOutput{LeaseExpiresAt: result.LeaseExpiresAt, ServerTime: result.ServerTime}, "attempt lease renewed")
}

func (adapter *adapter) saveAttemptNote(ctx context.Context, request *sdkmcp.CallToolRequest, input saveAttemptNoteInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
	sessionID := adapter.sessionIDFor(request.Session)
	if input.IdempotencyKey != nil {
		return adapter.failure(unsupportedField("idempotency_key"))
	}
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
	})
	if err != nil {
		return adapter.failure(err)
	}
	outputArtifacts := make([]artifactDTO, len(result.Artifacts))
	for index, artifact := range result.Artifacts {
		outputArtifacts[index] = artifactDTOFromDomain(artifact)
	}
	return success(saveAttemptNoteOutput{AttemptNote: attemptNoteDTOFromDomain(result.Note), Artifacts: outputArtifacts}, "attempt note saved")
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
		Warnings: append([]string{}, result.Warnings...), LatestEventID: result.LatestEventID, Artifacts: outputArtifacts}, "attempt finished")
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
	return success(planValidationOutputFromDomain(validation), "issue plan validated")
}

func (adapter *adapter) applyIssuePlan(ctx context.Context, request *sdkmcp.CallToolRequest, input applyIssuePlanInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
	result, err := adapter.plans.ApplyIssuePlan(ctx, input.domainPlan(), input.IdempotencyKey)
	if err != nil {
		return adapter.failure(err)
	}
	return success(applyIssuePlanOutputFromPort(result), "issue plan applied")
}

func (adapter *adapter) addComment(ctx context.Context, request *sdkmcp.CallToolRequest, input addCommentInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
	if input.IdempotencyKey != nil {
		return adapter.failure(unsupportedField("idempotency_key"))
	}
	comment, err := adapter.comments.AddComment(ctx, domain.AddCommentInput{
		IssueID: input.IssueID, Content: input.Content, SessionID: adapter.sessionIDFor(request.Session),
	})
	if err != nil {
		return adapter.failure(err)
	}
	return success(addCommentOutput{Comment: commentDTOFromDomain(comment)}, "comment added")
}

func (adapter *adapter) recordDecision(ctx context.Context, request *sdkmcp.CallToolRequest, input recordDecisionInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
	if input.IdempotencyKey != nil {
		return adapter.failure(unsupportedField("idempotency_key"))
	}
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
	return success(graphOutputFromDomain(graph), "issue graph returned")
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
	return success(graphOutputFromDomain(graph), "planning graph returned")
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
	}
	return success(output, "project metadata returned")
}

func (adapter *adapter) manageIssueRelation(ctx context.Context, request *sdkmcp.CallToolRequest, input manageIssueRelationInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
	if input.IdempotencyKey != nil {
		return adapter.failure(unsupportedField("idempotency_key"))
	}
	result, err := adapter.relations.ManageIssueRelation(ctx, domain.ManageIssueRelationInput{
		Action:        domain.RelationAction(input.Action),
		SourceIssueID: input.SourceIssueID,
		TargetIssueID: input.TargetIssueID,
		RelationType:  domain.RelationType(input.RelationType),
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
	if input.IdempotencyKey != nil {
		return adapter.failure(unsupportedField("idempotency_key"))
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
	})
	if err != nil {
		return adapter.failure(err)
	}
	return success(issueDTOFromDomain(result.Issue), "issue created")
}

func (adapter *adapter) updateIssue(ctx context.Context, request *sdkmcp.CallToolRequest, input updateIssueInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
	if input.IdempotencyKey != nil {
		return adapter.failure(unsupportedField("idempotency_key"))
	}
	result, err := adapter.issues.UpdateIssue(ctx, domain.UpdateIssueInput{
		IssueID:             input.IssueID,
		ExpectedVersion:     input.ExpectedVersion,
		Changes:             input.Changes.domainPatch(),
		CreateMissingLabels: input.CreateMissingLabels,
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
	return success(issueListOutput{Items: items, NextCursor: result.NextCursor, HasMore: result.HasMore}, "issues listed")
}

func (adapter *adapter) archiveIssue(ctx context.Context, request *sdkmcp.CallToolRequest, input archiveIssueInput) (*sdkmcp.CallToolResult, any, error) {
	adapter.touchSession(ctx, request.Session)
	if input.IdempotencyKey != nil {
		return adapter.failure(unsupportedField("idempotency_key"))
	}
	result, err := adapter.issues.ArchiveIssue(ctx, domain.ArchiveIssueInput{
		IssueID:         input.IssueID,
		ExpectedVersion: input.ExpectedVersion,
	})
	if err != nil {
		return adapter.failure(err)
	}
	return success(issueDTOFromDomain(result.Issue), "issue archived")
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
	return domain.NewError(domain.CodeInvalidArgument, "field is not supported", false,
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
