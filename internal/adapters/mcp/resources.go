package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/projectrouting"
)

const (
	// MaxContextPackBytes bounds every dynamic context-pack MCP resource payload
	// at strictly 16 KiB of serialized JSON.
	MaxContextPackBytes = 16 * 1024

	issueBriefTemplateURI               = "rhizome://projects/{project_ref}/issues/{issue_id}/brief"
	defaultProjectIssueBriefTemplateURI = "rhizome://issues/{issue_id}/brief"
	projectBriefTemplateURI             = "rhizome://projects/{project_ref}/brief"

	jsonResourceMIMEType       = "application/json"
	truncationReasonByteBudget = "byte_budget"
	truncatedTextSuffix        = "...(truncated)"
	issueBriefReviewQueryLimit = 20
	issueBriefMaxReviews       = 10
)

type projectRootLease interface {
	RootPath() string
}

type legacyProjectRootLease interface {
	ProjectRoot() string
}

type issueBriefOutput struct {
	ProjectRef       string                   `json:"project_ref"`
	Issue            issueBriefIssueDTO       `json:"issue"`
	Gates            issueBriefGatesDTO       `json:"gates"`
	Blockers         []issueBriefBlockerDTO   `json:"blockers"`
	Decisions        []issueBriefDecisionDTO  `json:"decisions"`
	Reviews          []issueBriefReviewDTO    `json:"reviews"`
	Warnings         []string                 `json:"warnings"`
	Truncated        bool                     `json:"truncated"`
	TruncationReason *string                  `json:"truncation_reason"`
}

type issueBriefIssueDTO struct {
	ID                 string  `json:"id"`
	DisplayID          string  `json:"display_id"`
	SequenceNo         int64   `json:"sequence_no"`
	Type               string  `json:"type"`
	Title              string  `json:"title"`
	Status             string  `json:"status"`
	EffectiveStatus    string  `json:"effective_status"`
	Priority           string  `json:"priority"`
	ParentIssueID      *string `json:"parent_issue_id"`
	BlockedReason      *string `json:"blocked_reason"`
	Version            int64   `json:"version"`
	Description        *string `json:"description"`
	AcceptanceCriteria *string `json:"acceptance_criteria"`
}

type issueBriefGatesDTO struct {
	EnforcementPoint string                          `json:"enforcement_point"`
	Passed           bool                            `json:"passed"`
	RequirementCount int64                           `json:"requirement_count"`
	SatisfiedCount   int64                           `json:"satisfied_count"`
	Unmet            []issueBriefUnmetRequirementDTO `json:"unmet"`
}

type issueBriefUnmetRequirementDTO struct {
	PolicyID       string `json:"policy_id"`
	RequirementKey string `json:"requirement_key"`
	Reason         string `json:"reason"`
}

type issueBriefBlockerDTO struct {
	ID        string `json:"id"`
	DisplayID string `json:"display_id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
}

type issueBriefDecisionDTO struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Status  string `json:"status"`
}

type issueBriefReviewDTO struct {
	ID                 string     `json:"id"`
	Status             string     `json:"status"`
	Purposes           []string   `json:"purposes"`
	TargetIssueVersion int64      `json:"target_issue_version"`
	CreatedAt          time.Time  `json:"created_at"`
	ResolvedAt         *time.Time `json:"resolved_at,omitempty"`
}

type projectBriefOutput struct {
	Project          projectBriefProjectDTO `json:"project"`
	Summary          projectBriefSummaryDTO `json:"summary"`
	ReadyIssues      []projectBriefIssueDTO `json:"ready_issues"`
	BlockedIssues    []projectBriefIssueDTO `json:"blocked_issues"`
	Warnings         []string               `json:"warnings"`
	Truncated        bool                   `json:"truncated"`
	TruncationReason *string                `json:"truncation_reason"`
}

type projectBriefProjectDTO struct {
	ProjectRef string  `json:"project_ref"`
	ID         string  `json:"id"`
	Name       *string `json:"name"`
	RootPath   *string `json:"root_path"`
}

type projectBriefSummaryDTO struct {
	StatusCounts       projectBriefStatusCountsDTO `json:"status_counts"`
	ActiveAttemptCount int64                       `json:"active_attempt_count"`
	BlockedCount       int64                       `json:"blocked_count"`
	OpenReviewCount    int64                       `json:"open_review_count"`
}

type projectBriefStatusCountsDTO struct {
	Open       int64 `json:"open"`
	Ready      int64 `json:"ready"`
	InProgress int64 `json:"in_progress"`
	Blocked    int64 `json:"blocked"`
	Review     int64 `json:"review"`
	Done       int64 `json:"done"`
	Cancelled  int64 `json:"cancelled"`
}

type projectBriefIssueDTO struct {
	ID                     string  `json:"id"`
	DisplayID              string  `json:"display_id"`
	Type                   string  `json:"type"`
	Title                  string  `json:"title"`
	Status                 string  `json:"status"`
	EffectiveStatus        string  `json:"effective_status"`
	Priority               string  `json:"priority"`
	IsClaimable            bool    `json:"is_claimable"`
	UnresolvedBlockerCount int64   `json:"unresolved_blocker_count,omitempty"`
	BlockedReason          *string `json:"blocked_reason,omitempty"`
}

// registerContextResources registers read-only dynamic MCP resource templates
// for bounded issue and project context packs. Static guide resources stay in
// ListResources so guide discovery remains unchanged.
func registerContextResources(server *sdkmcp.Server, a *adapter) {
	server.AddResourceTemplate(&sdkmcp.ResourceTemplate{
		URITemplate: issueBriefTemplateURI,
		Name:        "issue-brief",
		Title:       "Issue Brief Context Pack",
		Description: "Bounded read-only JSON context pack for one active issue in an explicitly routed project (max 16 KiB).",
		MIMEType:    jsonResourceMIMEType,
	}, a.handleIssueBriefResource)

	server.AddResourceTemplate(&sdkmcp.ResourceTemplate{
		URITemplate: defaultProjectIssueBriefTemplateURI,
		Name:        "default-project-issue-brief",
		Title:       "Default Project Issue Brief Context Pack",
		Description: "Bounded read-only JSON context pack for one active issue in the default project (max 16 KiB).",
		MIMEType:    jsonResourceMIMEType,
	}, a.handleDefaultProjectIssueBriefResource)

	server.AddResourceTemplate(&sdkmcp.ResourceTemplate{
		URITemplate: projectBriefTemplateURI,
		Name:        "project-brief",
		Title:       "Project Brief Context Pack",
		Description: "Bounded read-only JSON summary context pack for a project queue and health (max 16 KiB).",
		MIMEType:    jsonResourceMIMEType,
	}, a.handleProjectBriefResource)
}

func (a *adapter) handleIssueBriefResource(ctx context.Context, request *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
	rawURI := request.Params.URI
	params, err := parseTemplateURI(rawURI, []string{"projects", "", "issues", "", "brief"}, "project_ref", "issue_id")
	if err != nil {
		return nil, err
	}
	projectRef, issueID := params[0], params[1]
	if err := validateProjectRefForResource(rawURI, projectRef); err != nil {
		return nil, err
	}
	return a.readIssueBrief(ctx, rawURI, &projectRef, issueID)
}

func (a *adapter) handleDefaultProjectIssueBriefResource(ctx context.Context, request *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
	rawURI := request.Params.URI
	params, err := parseTemplateURI(rawURI, []string{"issues", "", "brief"}, "issue_id")
	if err != nil {
		return nil, err
	}
	return a.readIssueBrief(ctx, rawURI, nil, params[0])
}

func (a *adapter) handleProjectBriefResource(ctx context.Context, request *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
	rawURI := request.Params.URI
	params, err := parseTemplateURI(rawURI, []string{"projects", "", "brief"}, "project_ref")
	if err != nil {
		return nil, err
	}
	projectRef := params[0]
	if err := validateProjectRefForResource(rawURI, projectRef); err != nil {
		return nil, err
	}
	return a.readProjectBrief(ctx, rawURI, projectRef)
}

func (a *adapter) readIssueBrief(ctx context.Context, rawURI string, explicitRef *string, issueID string) (*sdkmcp.ReadResourceResult, error) {
	lease, err := a.acquireReadOnlyLease(ctx, rawURI, explicitRef)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lease.Release() }()

	services := lease.Services()
	if services.IssueService == nil || services.WorkContextService == nil {
		return nil, &jsonrpc.Error{
			Code:    jsonrpc.CodeInternalError,
			Message: "issue context services are unavailable",
		}
	}

	projection, err := services.IssueService.GetIssueProjection(ctx, issueID)
	if err != nil {
		return nil, mapResourceDomainError(rawURI, "issue_id", err)
	}
	if projection.Issue.ArchivedAt != nil {
		return nil, archivedIssueResourceError(rawURI, projection.Issue)
	}

	projectRef := lease.ProjectRef()
	if projectRef == "" && explicitRef != nil {
		projectRef = *explicitRef
	}
	if projectRef == "" && services.ProjectService != nil {
		project, projectErr := services.ProjectService.GetProject(ctx)
		if projectErr != nil {
			return nil, mapResourceDomainError(rawURI, "project_ref", projectErr)
		}
		projectRef = project.ID
	}

	workContext, err := services.WorkContextService.GetWorkContext(ctx, domain.GetWorkContextInput{
		IssueID: projection.Issue.ID,
	})
	if err != nil {
		return nil, mapResourceDomainError(rawURI, "issue_id", err)
	}

	reviews, err := collectIssueBriefReviews(ctx, services, projection.Issue.ID)
	if err != nil {
		return nil, mapResourceDomainError(rawURI, "issue_id", err)
	}

	pack := buildIssueBriefOutput(projectRef, projection, workContext, reviews)
	raw, err := fitIssueBriefToBudget(pack, MaxContextPackBytes)
	if err != nil {
		return nil, &jsonrpc.Error{
			Code:    jsonrpc.CodeInternalError,
			Message: "failed to encode issue brief context pack",
		}
	}
	return &sdkmcp.ReadResourceResult{
		Contents: []*sdkmcp.ResourceContents{{
			URI:      rawURI,
			MIMEType: jsonResourceMIMEType,
			Text:     string(raw),
		}},
	}, nil
}

func (a *adapter) readProjectBrief(ctx context.Context, rawURI string, projectRef string) (*sdkmcp.ReadResourceResult, error) {
	lease, err := a.acquireReadOnlyLease(ctx, rawURI, &projectRef)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lease.Release() }()

	services := lease.Services()
	if services.ProjectService == nil || services.IssueService == nil ||
		services.AttemptService == nil || services.ReviewService == nil {
		return nil, &jsonrpc.Error{
			Code:    jsonrpc.CodeInternalError,
			Message: "project brief services are unavailable",
		}
	}

	project, err := services.ProjectService.GetProject(ctx)
	if err != nil {
		return nil, mapResourceDomainError(rawURI, "project_ref", err)
	}
	resolvedRef := lease.ProjectRef()
	if resolvedRef == "" {
		resolvedRef = projectRef
	}
	if resolvedRef == "" {
		resolvedRef = project.ID
	}

	counts, err := services.IssueService.CountIssuesByEffectiveStatus(ctx)
	if err != nil {
		return nil, mapResourceDomainError(rawURI, "project_ref", err)
	}

	activeAttempts, err := services.AttemptService.ListActiveAttempts(ctx, domain.MaxCollectionLimit)
	if err != nil {
		return nil, mapResourceDomainError(rawURI, "project_ref", err)
	}

	openStatus := string(domain.ReviewRequestStatusOpen)
	openReviews, err := services.ReviewService.ListReviewRequests(ctx, application.ListReviewRequestsInput{
		Status: &openStatus,
		Limit:  domain.MaxCollectionLimit,
	})
	if err != nil {
		return nil, mapResourceDomainError(rawURI, "project_ref", err)
	}

	notBlocked := false
	readyPage, err := services.IssueService.ListIssues(ctx, domain.ListIssuesInput{
		EffectiveStatuses: []domain.EffectiveStatus{domain.EffectiveStatusReady},
		IsBlocked:         &notBlocked,
		Limit:             domain.MaxCollectionLimit,
	})
	if err != nil {
		return nil, mapResourceDomainError(rawURI, "project_ref", err)
	}

	blocked := true
	blockedPage, err := services.IssueService.ListIssues(ctx, domain.ListIssuesInput{
		IsBlocked: &blocked,
		Limit:     domain.MaxCollectionLimit,
	})
	if err != nil {
		return nil, mapResourceDomainError(rawURI, "project_ref", err)
	}

	pack := buildProjectBriefOutput(resolvedRef, project, leaseRootPath(lease), counts, activeAttempts, openReviews, readyPage, blockedPage)
	raw, err := fitProjectBriefToBudget(pack, MaxContextPackBytes)
	if err != nil {
		return nil, &jsonrpc.Error{
			Code:    jsonrpc.CodeInternalError,
			Message: "failed to encode project brief context pack",
		}
	}
	return &sdkmcp.ReadResourceResult{
		Contents: []*sdkmcp.ResourceContents{{
			URI:      rawURI,
			MIMEType: jsonResourceMIMEType,
			Text:     string(raw),
		}},
	}, nil
}

// parseTemplateURI splits rawURI after "rhizome://" and matches exact path
// segments. Template positions in expectedSegments are marked by empty strings,
// whose URL-unescaped values are returned in order matching paramNames.
func parseTemplateURI(rawURI string, expectedSegments []string, paramNames ...string) ([]string, error) {
	if !strings.HasPrefix(rawURI, "rhizome://") {
		return nil, sdkmcp.ResourceNotFoundError(rawURI)
	}
	if strings.ContainsAny(rawURI, "?#") {
		return nil, invalidResourceParamsError(rawURI, "uri", "resource URI must not contain query or fragment components")
	}
	trimmed := strings.TrimPrefix(rawURI, "rhizome://")
	segments := strings.Split(trimmed, "/")
	if len(segments) != len(expectedSegments) {
		return nil, sdkmcp.ResourceNotFoundError(rawURI)
	}

	values := make([]string, 0, len(paramNames))
	paramIdx := 0
	for i, literal := range expectedSegments {
		if literal != "" {
			if segments[i] != literal {
				return nil, sdkmcp.ResourceNotFoundError(rawURI)
			}
			continue
		}
		paramName := "uri"
		if paramIdx < len(paramNames) {
			paramName = paramNames[paramIdx]
		}
		paramIdx++

		unescaped, err := url.PathUnescape(segments[i])
		if err != nil {
			return nil, invalidResourceParamsError(rawURI, paramName, fmt.Sprintf("invalid %s URL encoding in resource URI", paramName))
		}
		if unescaped == "" || strings.TrimSpace(unescaped) != unescaped || strings.Contains(unescaped, "/") {
			return nil, invalidResourceParamsError(rawURI, paramName, fmt.Sprintf("invalid %s in resource URI", paramName))
		}
		values = append(values, unescaped)
	}
	return values, nil
}

func validateProjectRefForResource(rawURI, projectRef string) error {
	if err := projectrouting.ValidateProjectRef(projectRef); err != nil {
		msg := fmt.Sprintf("invalid project_ref: %s", err.Error())
		var domainErr *domain.Error
		if errors.As(err, &domainErr) {
			msg = fmt.Sprintf("invalid project_ref: %s", domainErr.Message)
		}
		return invalidResourceParamsError(rawURI, "project_ref", msg)
	}
	return nil
}

func (a *adapter) acquireReadOnlyLease(ctx context.Context, rawURI string, explicitRef *string) (projectrouting.ProjectLease, error) {
	if a == nil || a.router == nil {
		return nil, &jsonrpc.Error{
			Code:    jsonrpc.CodeInternalError,
			Message: "project router is unavailable",
		}
	}
	lease, err := a.router.Acquire(ctx, explicitRef)
	if err == nil {
		return lease, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		switch domainErr.Code {
		case domain.CodeProjectNotFound:
			return nil, sdkmcp.ResourceNotFoundError(rawURI)
		case domain.CodeProjectRequired:
			payload, _ := json.Marshal(struct {
				URI  string `json:"uri"`
				Code string `json:"code"`
			}{
				URI:  rawURI,
				Code: domain.CodeProjectRequired,
			})
			return nil, &jsonrpc.Error{
				Code:    sdkmcp.CodeResourceNotFound,
				Message: fmt.Sprintf("resource not found: %s (no default project configured)", rawURI),
				Data:    payload,
			}
		case domain.CodeInvalidArgument, domain.CodeValidationError:
			return nil, invalidResourceParamsError(rawURI, "project_ref", fmt.Sprintf("invalid project_ref: %s", domainErr.Message))
		default:
			payload, _ := json.Marshal(struct {
				URI  string `json:"uri"`
				Code string `json:"code"`
			}{
				URI:  rawURI,
				Code: domainErr.Code,
			})
			return nil, &jsonrpc.Error{
				Code:    jsonrpc.CodeInternalError,
				Message: domainErr.Message,
				Data:    payload,
			}
		}
	}
	return nil, &jsonrpc.Error{
		Code:    jsonrpc.CodeInternalError,
		Message: "failed to acquire project router lease",
	}
}

func mapResourceDomainError(rawURI, field string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		switch domainErr.Code {
		case domain.CodeIssueNotFound, domain.CodeProjectNotFound:
			return sdkmcp.ResourceNotFoundError(rawURI)
		case domain.CodeInvalidArgument, domain.CodeValidationError:
			return invalidResourceParamsError(rawURI, field, fmt.Sprintf("invalid %s: %s", field, domainErr.Message))
		default:
			payload, _ := json.Marshal(struct {
				URI  string `json:"uri"`
				Code string `json:"code"`
			}{
				URI:  rawURI,
				Code: domainErr.Code,
			})
			return &jsonrpc.Error{
				Code:    jsonrpc.CodeInternalError,
				Message: domainErr.Message,
				Data:    payload,
			}
		}
	}
	return &jsonrpc.Error{
		Code:    jsonrpc.CodeInternalError,
		Message: "failed to read resource",
	}
}

func invalidResourceParamsError(uri, field, message string) error {
	payload, _ := json.Marshal(struct {
		URI   string `json:"uri"`
		Field string `json:"field"`
		Code  string `json:"code"`
	}{
		URI:   uri,
		Field: field,
		Code:  domain.CodeValidationError,
	})
	return &jsonrpc.Error{
		Code:    jsonrpc.CodeInvalidParams,
		Message: message,
		Data:    payload,
	}
}

func archivedIssueResourceError(uri string, issue domain.Issue) error {
	payload, _ := json.Marshal(struct {
		URI        string     `json:"uri"`
		Code       string     `json:"code"`
		IssueID    string     `json:"issue_id"`
		DisplayID  string     `json:"display_id"`
		ArchivedAt *time.Time `json:"archived_at,omitempty"`
	}{
		URI:        uri,
		Code:       domain.CodeIssueArchived,
		IssueID:    issue.ID,
		DisplayID:  issue.DisplayID,
		ArchivedAt: issue.ArchivedAt,
	})
	return &jsonrpc.Error{
		Code:    jsonrpc.CodeInvalidParams,
		Message: fmt.Sprintf("archived issue %s (issue_id=%s) is unavailable", issue.DisplayID, issue.ID),
		Data:    payload,
	}
}

func collectIssueBriefReviews(ctx context.Context, services application.Bundle, canonicalIssueID string) ([]issueBriefReviewDTO, error) {
	reviews := make([]issueBriefReviewDTO, 0)
	if services.ActivityService == nil {
		return reviews, nil
	}
	activity, err := services.ActivityService.GetIssueActivity(ctx, domain.GetIssueActivityInput{
		IssueID: canonicalIssueID,
		Types:   []domain.ActivityCategory{domain.ActivityCategoryReviews},
		Limit:   issueBriefReviewQueryLimit,
		Order:   domain.ActivityOrderNewestFirst,
	})
	if err != nil {
		return nil, err
	}
	for _, item := range activity.Items {
		if item.Review == nil {
			continue
		}
		purposes := append([]string(nil), item.Review.Purposes...)
		if purposes == nil {
			purposes = []string{}
		}
		reviews = append(reviews, issueBriefReviewDTO{
			ID:                 item.Review.ID,
			Status:             string(item.Review.Status),
			Purposes:           purposes,
			TargetIssueVersion: item.Review.TargetIssueVersion,
			CreatedAt:          item.Review.CreatedAt,
			ResolvedAt:         copyTime(item.Review.ResolvedAt),
		})
	}
	// Surface open and claimed reviews ahead of resolved ones while retaining newest-first order.
	sort.SliceStable(reviews, func(i, j int) bool {
		return isActiveReviewStatus(reviews[i].Status) && !isActiveReviewStatus(reviews[j].Status)
	})
	if len(reviews) > issueBriefMaxReviews {
		reviews = reviews[:issueBriefMaxReviews]
	}
	return reviews, nil
}

func isActiveReviewStatus(status string) bool {
	return status == string(domain.ReviewRequestStatusOpen) || status == string(domain.ReviewRequestStatusClaimed)
}

func buildIssueBriefOutput(projectRef string, projection domain.IssueProjection, workContext domain.WorkContext, reviews []issueBriefReviewDTO) issueBriefOutput {
	issue := projection.Issue
	unmet := make([]issueBriefUnmetRequirementDTO, len(workContext.Gates.Unmet))
	for i, req := range workContext.Gates.Unmet {
		unmet[i] = issueBriefUnmetRequirementDTO{
			PolicyID:       req.PolicyID,
			RequirementKey: req.RequirementKey,
			Reason:         req.Reason,
		}
	}
	blockers := make([]issueBriefBlockerDTO, len(workContext.Blockers))
	for i, blocker := range workContext.Blockers {
		blockers[i] = issueBriefBlockerDTO{
			ID:        blocker.ID,
			DisplayID: blocker.DisplayID,
			Title:     blocker.Title,
			Status:    string(blocker.EffectiveStatus),
		}
	}
	decisions := make([]issueBriefDecisionDTO, len(workContext.Decisions))
	for i, decision := range workContext.Decisions {
		decisions[i] = issueBriefDecisionDTO{
			ID:      decision.ID,
			Title:   decision.Title,
			Summary: decision.Summary,
			Status:  string(decision.Status),
		}
	}
	warnings := append([]string{}, workContext.Warnings...)
	if warnings == nil {
		warnings = []string{}
	}
	if reviews == nil {
		reviews = []issueBriefReviewDTO{}
	}

	return issueBriefOutput{
		ProjectRef: projectRef,
		Issue: issueBriefIssueDTO{
			ID:                 issue.ID,
			DisplayID:          issue.DisplayID,
			SequenceNo:         issue.SequenceNo,
			Type:               string(issue.Type),
			Title:              issue.Title,
			Status:             string(issue.Status),
			EffectiveStatus:    string(projection.EffectiveStatus),
			Priority:           string(issue.Priority),
			ParentIssueID:      copyString(issue.ParentID),
			BlockedReason:      copyString(issue.BlockedReason),
			Version:            issue.Version,
			Description:        copyString(issue.Description),
			AcceptanceCriteria: copyString(issue.AcceptanceCriteria),
		},
		Gates: issueBriefGatesDTO{
			EnforcementPoint: string(workContext.Gates.Point),
			Passed:           len(workContext.Gates.Unmet) == 0,
			RequirementCount: workContext.Gates.RequirementCount,
			SatisfiedCount:   workContext.Gates.SatisfiedCount,
			Unmet:            unmet,
		},
		Blockers:  blockers,
		Decisions: decisions,
		Reviews:   reviews,
		Warnings:  warnings,
	}
}

func fitIssueBriefToBudget(pack issueBriefOutput, maxBytes int) ([]byte, error) {
	raw, err := json.Marshal(pack)
	if err != nil {
		return nil, err
	}
	if len(raw) <= maxBytes {
		return raw, nil
	}

	reason := truncationReasonByteBudget
	pack.Truncated = true
	pack.TruncationReason = &reason

	shrinkSlice := func(shrink func() bool) ([]byte, bool, error) {
		for shrink() {
			encoded, marshalErr := json.Marshal(pack)
			if marshalErr != nil {
				return nil, false, marshalErr
			}
			if len(encoded) <= maxBytes {
				return encoded, true, nil
			}
		}
		return nil, false, nil
	}

	for _, shrink := range []func() bool{
		func() bool {
			if len(pack.Reviews) == 0 {
				return false
			}
			pack.Reviews = pack.Reviews[:len(pack.Reviews)-1]
			return true
		},
		func() bool {
			if len(pack.Decisions) == 0 {
				return false
			}
			pack.Decisions = pack.Decisions[:len(pack.Decisions)-1]
			return true
		},
		func() bool {
			if len(pack.Blockers) == 0 {
				return false
			}
			pack.Blockers = pack.Blockers[:len(pack.Blockers)-1]
			return true
		},
		func() bool {
			if len(pack.Gates.Unmet) == 0 {
				return false
			}
			pack.Gates.Unmet = pack.Gates.Unmet[:len(pack.Gates.Unmet)-1]
			return true
		},
		func() bool {
			if len(pack.Warnings) == 0 {
				return false
			}
			pack.Warnings = pack.Warnings[:len(pack.Warnings)-1]
			return true
		},
	} {
		if encoded, done, err := shrinkSlice(shrink); err != nil {
			return nil, err
		} else if done {
			return encoded, nil
		}
	}

	if encoded, done, err := fitIssueTextFieldsToBudget(&pack, maxBytes); err != nil {
		return nil, err
	} else if done {
		return encoded, nil
	}

	encoded, done, err := trimOptionalTextFieldToBudget(maxBytes, &pack.Issue.Title, func() ([]byte, error) {
		return json.Marshal(pack)
	})
	if err != nil {
		return nil, err
	}
	if done {
		return encoded, nil
	}
	return json.Marshal(pack)
}

// fitIssueTextFieldsToBudget binary-searches one shared byte cap across
// description and acceptance_criteria so both long fields keep fair content
// while short fields stay intact, then trims blocked_reason only if needed.
func fitIssueTextFieldsToBudget(pack *issueBriefOutput, maxBytes int) ([]byte, bool, error) {
	origDesc := ""
	if pack.Issue.Description != nil {
		origDesc = *pack.Issue.Description
	}
	origAC := ""
	if pack.Issue.AcceptanceCriteria != nil {
		origAC = *pack.Issue.AcceptanceCriteria
	}
	origBlocked := ""
	if pack.Issue.BlockedReason != nil {
		origBlocked = *pack.Issue.BlockedReason
	}

	maxTextLen := max(len(origDesc), len(origAC))
	if maxTextLen > 0 {
		applyCap := func(capBytes int) {
			if pack.Issue.Description != nil {
				*pack.Issue.Description = capTextWithTruncatedSuffix(origDesc, capBytes)
			}
			if pack.Issue.AcceptanceCriteria != nil {
				*pack.Issue.AcceptanceCriteria = capTextWithTruncatedSuffix(origAC, capBytes)
			}
		}

		low, high := 0, maxTextLen
		bestCap := -1
		var bestEncoded []byte
		for low <= high {
			mid := low + (high-low)/2
			applyCap(mid)
			encoded, err := json.Marshal(pack)
			if err != nil {
				return nil, false, err
			}
			if len(encoded) <= maxBytes {
				bestCap = mid
				bestEncoded = encoded
				low = mid + 1
			} else {
				high = mid - 1
			}
		}
		if bestCap >= 0 {
			applyCap(bestCap)
			return bestEncoded, true, nil
		}
		applyCap(0)
	}

	if pack.Issue.BlockedReason != nil && origBlocked != "" {
		return trimOptionalTextFieldToBudget(maxBytes, pack.Issue.BlockedReason, func() ([]byte, error) {
			return json.Marshal(pack)
		})
	}
	encoded, err := json.Marshal(pack)
	if err != nil {
		return nil, false, err
	}
	return encoded, len(encoded) <= maxBytes, nil
}

func capTextWithTruncatedSuffix(original string, byteLimit int) string {
	if byteLimit >= len(original) {
		return original
	}
	prefix := utf8Prefix(original, byteLimit)
	if prefix == "" {
		return ""
	}
	return prefix + truncatedTextSuffix
}

func utf8Prefix(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if limit >= len(s) {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}

func leaseRootPath(lease projectrouting.ProjectLease) *string {
	if lease == nil {
		return nil
	}
	if rooted, ok := lease.(projectRootLease); ok {
		if root := strings.TrimSpace(rooted.RootPath()); root != "" {
			return &root
		}
	}
	if legacy, ok := lease.(legacyProjectRootLease); ok {
		if root := strings.TrimSpace(legacy.ProjectRoot()); root != "" {
			return &root
		}
	}
	return nil
}

func buildProjectBriefOutput(
	projectRef string,
	project domain.Project,
	rootPath *string,
	statusCounts []domain.EffectiveStatusCount,
	activeAttempts domain.ActiveAttemptList,
	openReviews application.ListReviewRequestsResult,
	readyPage domain.IssueList,
	blockedPage domain.IssueList,
) projectBriefOutput {
	var counts projectBriefStatusCountsDTO
	for _, entry := range statusCounts {
		switch entry.EffectiveStatus {
		case domain.EffectiveStatusOpen:
			counts.Open = entry.Count
		case domain.EffectiveStatusReady:
			counts.Ready = entry.Count
		case domain.EffectiveStatusInProgress:
			counts.InProgress = entry.Count
		case domain.EffectiveStatusBlocked:
			counts.Blocked = entry.Count
		case domain.EffectiveStatusReview:
			counts.Review = entry.Count
		case domain.EffectiveStatusDone:
			counts.Done = entry.Count
		case domain.EffectiveStatusCancelled:
			counts.Cancelled = entry.Count
		}
	}

	readyIssues := make([]projectBriefIssueDTO, 0, len(readyPage.Items))
	for _, item := range readyPage.Items {
		readyIssues = append(readyIssues, projectBriefIssueDTOFromProjection(item, false))
	}

	blockedIssues := make([]projectBriefIssueDTO, 0, len(blockedPage.Items))
	for _, item := range blockedPage.Items {
		blockedIssues = append(blockedIssues, projectBriefIssueDTOFromProjection(item, true))
	}

	warnings := make([]string, 0)
	if activeAttempts.HasMore {
		warnings = append(warnings, fmt.Sprintf("active_attempt_count capped at %d items", domain.MaxCollectionLimit))
	}
	if openReviews.HasMore {
		warnings = append(warnings, fmt.Sprintf("open_review_count capped at %d items", domain.MaxCollectionLimit))
	}
	if readyPage.HasMore {
		warnings = append(warnings, fmt.Sprintf("ready_issues page capped at %d items", domain.MaxCollectionLimit))
	}
	if blockedPage.HasMore {
		warnings = append(warnings, fmt.Sprintf("blocked_issues capped at %d items", domain.MaxCollectionLimit))
	}

	return projectBriefOutput{
		Project: projectBriefProjectDTO{
			ProjectRef: projectRef,
			ID:         project.ID,
			Name:       copyString(project.Name),
			RootPath:   copyString(rootPath),
		},
		Summary: projectBriefSummaryDTO{
			StatusCounts:       counts,
			ActiveAttemptCount: int64(len(activeAttempts.Items)),
			BlockedCount:       int64(len(blockedPage.Items)),
			OpenReviewCount:    int64(len(openReviews.Items)),
		},
		ReadyIssues:   readyIssues,
		BlockedIssues: blockedIssues,
		Warnings:      warnings,
	}
}

func projectBriefIssueDTOFromProjection(item domain.IssueProjection, includeBlockedDetails bool) projectBriefIssueDTO {
	dto := projectBriefIssueDTO{
		ID:              item.Issue.ID,
		DisplayID:       item.Issue.DisplayID,
		Type:            string(item.Issue.Type),
		Title:           item.Issue.Title,
		Status:          string(item.Issue.Status),
		EffectiveStatus: string(item.EffectiveStatus),
		Priority:        string(item.Issue.Priority),
		IsClaimable:     item.IsClaimable,
	}
	if includeBlockedDetails {
		dto.UnresolvedBlockerCount = item.UnresolvedBlockerCount
		dto.BlockedReason = copyString(item.Issue.BlockedReason)
	}
	return dto
}

func fitProjectBriefToBudget(pack projectBriefOutput, maxBytes int) ([]byte, error) {
	raw, err := json.Marshal(pack)
	if err != nil {
		return nil, err
	}
	if len(raw) <= maxBytes {
		return raw, nil
	}

	reason := truncationReasonByteBudget
	pack.Truncated = true
	pack.TruncationReason = &reason

	allReady := pack.ReadyIssues
	allBlocked := pack.BlockedIssues
	total := len(allReady) + len(allBlocked)

	balancedCounts := func(keepTotal int) (int, int) {
		if keepTotal <= 0 {
			return 0, 0
		}
		if keepTotal >= total {
			return len(allReady), len(allBlocked)
		}
		r := keepTotal / 2
		b := keepTotal - r
		if r > len(allReady) {
			r = len(allReady)
			b = min(len(allBlocked), keepTotal-r)
		} else if b > len(allBlocked) {
			b = len(allBlocked)
			r = min(len(allReady), keepTotal-b)
		}
		return r, b
	}

	low, high := 0, total
	best := -1
	var bestEncoded []byte
	for low <= high {
		mid := low + (high-low)/2
		r, b := balancedCounts(mid)
		pack.ReadyIssues = allReady[:r]
		pack.BlockedIssues = allBlocked[:b]
		encoded, marshalErr := json.Marshal(pack)
		if marshalErr != nil {
			return nil, marshalErr
		}
		if len(encoded) <= maxBytes {
			best = mid
			bestEncoded = encoded
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	if best >= 0 {
		return bestEncoded, nil
	}

	pack.ReadyIssues = []projectBriefIssueDTO{}
	pack.BlockedIssues = []projectBriefIssueDTO{}
	for len(pack.Warnings) > 0 {
		pack.Warnings = pack.Warnings[:len(pack.Warnings)-1]
		encoded, marshalErr := json.Marshal(pack)
		if marshalErr != nil {
			return nil, marshalErr
		}
		if len(encoded) <= maxBytes {
			return encoded, nil
		}
	}
	for _, target := range []*string{pack.Project.RootPath, pack.Project.Name} {
		if target == nil {
			continue
		}
		encoded, done, trimErr := trimOptionalTextFieldToBudget(maxBytes, target, func() ([]byte, error) {
			return json.Marshal(pack)
		})
		if trimErr != nil {
			return nil, trimErr
		}
		if done {
			return encoded, nil
		}
	}
	return json.Marshal(pack)
}

// trimOptionalTextFieldToBudget binary-searches the longest UTF-8-safe prefix of
// *field (with truncatedTextSuffix appended when non-empty) such that marshal()
// fits within maxBytes. If even zero runes with suffix exceed maxBytes, it sets
// *field = "" and reports whether marshal() now fits.
func trimOptionalTextFieldToBudget(maxBytes int, field *string, marshal func() ([]byte, error)) ([]byte, bool, error) {
	original := *field
	if original == "" {
		encoded, err := marshal()
		return encoded, err == nil && len(encoded) <= maxBytes, err
	}

	low, high := 0, len(original)
	bestLimit := -1
	var bestEncoded []byte
	for low <= high {
		mid := low + (high-low)/2
		*field = capTextWithTruncatedSuffix(original, mid)
		encoded, err := marshal()
		if err != nil {
			return nil, false, err
		}
		if len(encoded) <= maxBytes {
			bestLimit = mid
			bestEncoded = encoded
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	if bestLimit >= 0 {
		*field = capTextWithTruncatedSuffix(original, bestLimit)
		return bestEncoded, true, nil
	}

	*field = ""
	encoded, err := marshal()
	if err != nil {
		return nil, false, err
	}
	return encoded, len(encoded) <= maxBytes, nil
}
