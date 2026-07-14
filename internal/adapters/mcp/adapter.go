// Package mcp exposes the Phase 2 application services through MCP tools.
package mcp

import (
	"context"
	"errors"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/domain"
)

// Options supplies the explicit composition dependencies for the MCP adapter.
type Options struct {
	IssueService   *application.IssueService
	ProjectService *application.ProjectService
	ServerName     string
	ServerVersion  string
	ConfigVersion  int
}

type adapter struct {
	issues        *application.IssueService
	projects      *application.ProjectService
	appVersion    string
	configVersion int
}

// NewServer composes a tools-only Phase 2 MCP server. It has no process-global
// dependencies and deliberately exposes no resources or prototype task tools.
func NewServer(options Options) (*sdkmcp.Server, error) {
	if options.IssueService == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "issue service is required", false)
	}
	if options.ProjectService == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "project service is required", false)
	}
	if options.ServerName == "" {
		return nil, domain.NewError(domain.CodeInvalidArgument, "server name is required", false)
	}
	if options.ServerVersion == "" {
		return nil, domain.NewError(domain.CodeInvalidArgument, "server version is required", false)
	}
	adapter := &adapter{
		issues:        options.IssueService,
		projects:      options.ProjectService,
		appVersion:    options.ServerVersion,
		configVersion: options.ConfigVersion,
	}
	server := sdkmcp.NewServer(
		&sdkmcp.Implementation{Name: options.ServerName, Version: options.ServerVersion},
		&sdkmcp.ServerOptions{Capabilities: &sdkmcp.ServerCapabilities{Tools: &sdkmcp.ToolCapabilities{}}},
	)
	adapter.register(server)
	return server, nil
}

func (adapter *adapter) register(server *sdkmcp.Server) {
	sdkmcp.AddTool(server, tool("get_project", "Return current project metadata and Phase 2 capabilities", schemaGetProject(), schemaProjectOutput()), adapter.getProject)
	sdkmcp.AddTool(server, tool("list_labels", "List reusable labels in deterministic order", schemaListLabels(), schemaLabelListOutput()), adapter.listLabels)
	sdkmcp.AddTool(server, tool("create_issue", "Create an issue", schemaCreateIssue(), schemaIssueOutput()), adapter.createIssue)
	sdkmcp.AddTool(server, tool("update_issue", "Apply an optimistic issue patch", schemaUpdateIssue(), schemaUpdateOutput()), adapter.updateIssue)
	sdkmcp.AddTool(server, tool("get_issue", "Get an issue by internal or display ID", schemaGetIssue(), schemaIssueOutput()), adapter.getIssue)
	sdkmcp.AddTool(server, tool("list_issues", "List issues in deterministic order", schemaListIssues(), schemaIssueListOutput()), adapter.listIssues)
	sdkmcp.AddTool(server, tool("archive_issue", "Archive an issue with an optimistic version precondition", schemaArchiveIssue(), schemaIssueOutput()), adapter.archiveIssue)
}

func (adapter *adapter) getProject(ctx context.Context, _ *sdkmcp.CallToolRequest, input getProjectInput) (*sdkmcp.CallToolResult, any, error) {
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
		SupportedRelationTypes: []string{},
		SupportedPriorities:    []string{"low", "medium", "high", "critical"},
		LatestEventID:          project.LatestEventID,
	}
	return success(output, "project metadata returned")
}

func (adapter *adapter) listLabels(ctx context.Context, _ *sdkmcp.CallToolRequest, input listLabelsInput) (*sdkmcp.CallToolResult, any, error) {
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

func (adapter *adapter) createIssue(ctx context.Context, _ *sdkmcp.CallToolRequest, input createIssueInput) (*sdkmcp.CallToolResult, any, error) {
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

func (adapter *adapter) updateIssue(ctx context.Context, _ *sdkmcp.CallToolRequest, input updateIssueInput) (*sdkmcp.CallToolResult, any, error) {
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

func (adapter *adapter) getIssue(ctx context.Context, _ *sdkmcp.CallToolRequest, input getIssueInput) (*sdkmcp.CallToolResult, any, error) {
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

func (adapter *adapter) listIssues(ctx context.Context, _ *sdkmcp.CallToolRequest, input listIssuesInput) (*sdkmcp.CallToolResult, any, error) {
	if input.View != "" && input.View != "compact" {
		return adapter.failure(unsupportedField("view"))
	}
	result, err := adapter.issues.ListIssues(ctx, domain.ListIssuesInput{
		Types:             stringsToTypes(input.Types),
		Statuses:          stringsToStatuses(input.Statuses),
		EffectiveStatuses: stringsToStatuses(input.EffectiveStatuses),
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
			issueDTO:        issueDTOFromDomain(item.Issue),
			EffectiveStatus: string(item.EffectiveStatus),
			IsBlocked:       item.IsBlocked,
			IsClaimable:     item.IsClaimable,
		}
	}
	return success(issueListOutput{Items: items, NextCursor: result.NextCursor, HasMore: result.HasMore}, "issues listed")
}

func (adapter *adapter) archiveIssue(ctx context.Context, _ *sdkmcp.CallToolRequest, input archiveIssueInput) (*sdkmcp.CallToolResult, any, error) {
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
	return domain.NewError(domain.CodeInvalidArgument, "field is not supported in Phase 2", false,
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

func stringsToPriorities(values []string) []domain.Priority {
	result := make([]domain.Priority, len(values))
	for i, value := range values {
		result[i] = domain.Priority(value)
	}
	return result
}
