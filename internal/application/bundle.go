package application

import "rhizome-mcp/internal/domain"

// Bundle is the project-local service bundle shared by every adapter.
type Bundle struct {
	IssueService          *IssueService
	ProjectService        *ProjectService
	RelationService       *RelationService
	GraphService          *GraphService
	PlanningService       *PlanningService
	CommentService        *CommentService
	DecisionService       *DecisionService
	ActivityService       *ActivityService
	SearchService         *SearchService
	ReviewService         *ReviewService
	AttemptService        *AttemptService
	ReservationService    *ReservationService
	SessionService        *AgentSessionService
	WorkContextService    *WorkContextService
	WorkflowPolicyService *WorkflowPolicyService

	// CLI-only services; not required by the MCP request path.
	MaintenanceService *MaintenanceService
	BoardService       *BoardService
	IssueDetailService *IssueDetailService
}

// Validate ensures all required services are present.
func (bundle Bundle) Validate() error {
	for _, required := range []struct {
		name    string
		present bool
	}{
		{"issue service", bundle.IssueService != nil},
		{"project service", bundle.ProjectService != nil},
		{"relation service", bundle.RelationService != nil},
		{"graph service", bundle.GraphService != nil},
		{"planning service", bundle.PlanningService != nil},
		{"comment service", bundle.CommentService != nil},
		{"decision service", bundle.DecisionService != nil},
		{"activity service", bundle.ActivityService != nil},
		{"search service", bundle.SearchService != nil},
		{"review service", bundle.ReviewService != nil},
		{"attempt service", bundle.AttemptService != nil},
		{"reservation service", bundle.ReservationService != nil},
		{"session service", bundle.SessionService != nil},
		{"work context service", bundle.WorkContextService != nil},
		// Required from ISSUE-174 on: manage_workflow_policy,
		// get_workflow_policy, list_workflow_policies and evaluate_gates are
		// advertised tools, so a bundle without this service would fail at
		// request time rather than at startup.
		{"workflow policy service", bundle.WorkflowPolicyService != nil},
	} {
		if !required.present {
			return domain.NewError(domain.CodeInvalidArgument, required.name+" is required", false)
		}
	}
	return nil
}
