package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

type getProjectInput struct {
	IncludeInstructions bool `json:"include_instructions,omitempty"`
}

type listLabelsInput struct {
	Query  *string `json:"query,omitempty"`
	Limit  int     `json:"limit,omitempty"`
	Cursor *string `json:"cursor,omitempty"`
}

type createIssueInput struct {
	Type                string   `json:"type"`
	Title               string   `json:"title"`
	Description         *string  `json:"description,omitempty"`
	AcceptanceCriteria  *string  `json:"acceptance_criteria,omitempty"`
	Status              string   `json:"status,omitempty"`
	Priority            string   `json:"priority,omitempty"`
	ParentIssueID       *string  `json:"parent_issue_id,omitempty"`
	BlockedReason       *string  `json:"blocked_reason,omitempty"`
	Labels              []string `json:"labels,omitempty"`
	CreateMissingLabels bool     `json:"create_missing_labels,omitempty"`
	IdempotencyKey      *string  `json:"idempotency_key,omitempty"`
}

type updateIssueInput struct {
	IssueID             string     `json:"issue_id"`
	ExpectedVersion     int64      `json:"expected_version"`
	Changes             patchInput `json:"changes"`
	CreateMissingLabels bool       `json:"create_missing_labels,omitempty"`
	IdempotencyKey      *string    `json:"idempotency_key,omitempty"`
}

type getIssueInput struct {
	IssueID string                     `json:"issue_id"`
	View    string                     `json:"view,omitempty"`
	Include []string                   `json:"include,omitempty"`
	Limits  map[string]json.RawMessage `json:"limits,omitempty"`
}

type listIssuesInput struct {
	Types             []string `json:"types,omitempty"`
	Statuses          []string `json:"statuses,omitempty"`
	EffectiveStatuses []string `json:"effective_statuses,omitempty"`
	Priorities        []string `json:"priorities,omitempty"`
	Labels            []string `json:"labels,omitempty"`
	ParentIssueID     *string  `json:"parent_issue_id,omitempty"`
	IsBlocked         *bool    `json:"is_blocked,omitempty"`
	IsClaimable       *bool    `json:"is_claimable,omitempty"`
	IncludeArchived   bool     `json:"include_archived,omitempty"`
	Limit             int      `json:"limit,omitempty"`
	Cursor            *string  `json:"cursor,omitempty"`
	View              string   `json:"view,omitempty"`
}

type archiveIssueInput struct {
	IssueID         string  `json:"issue_id"`
	ExpectedVersion int64   `json:"expected_version"`
	IdempotencyKey  *string `json:"idempotency_key,omitempty"`
}

type manageIssueRelationInput struct {
	Action         string  `json:"action"`
	SourceIssueID  string  `json:"source_issue_id"`
	TargetIssueID  string  `json:"target_issue_id"`
	RelationType   string  `json:"relation_type"`
	IdempotencyKey *string `json:"idempotency_key,omitempty"`
}

type getIssueGraphInput struct {
	RootIssueID      string   `json:"root_issue_id"`
	Depth            *int     `json:"depth,omitempty"`
	Direction        string   `json:"direction,omitempty"`
	RelationTypes    []string `json:"relation_types,omitempty"`
	IncludeHierarchy *bool    `json:"include_hierarchy,omitempty"`
	IncludeTerminal  *bool    `json:"include_terminal,omitempty"`
	MaxNodes         *int     `json:"max_nodes,omitempty"`
	View             string   `json:"view,omitempty"`
}

type getPlanningGraphInput struct {
	RootIssueID    *string `json:"root_issue_id,omitempty"`
	Depth          *int    `json:"depth,omitempty"`
	MaxNodes       *int    `json:"max_nodes,omitempty"`
	IncludeReview  *bool   `json:"include_review,omitempty"`
	IncludeRelated *bool   `json:"include_related,omitempty"`
}

type issuePlanInput struct {
	Issues    []planIssueInput    `json:"issues"`
	Relations []planRelationInput `json:"relations"`
	Decisions []planDecisionInput `json:"decisions"`
}

type applyIssuePlanInput struct {
	Issues         []planIssueInput    `json:"issues"`
	Relations      []planRelationInput `json:"relations"`
	Decisions      []planDecisionInput `json:"decisions"`
	IdempotencyKey string              `json:"idempotency_key"`
}

func (input applyIssuePlanInput) domainPlan() domain.IssuePlan {
	return issuePlanInput{Issues: input.Issues, Relations: input.Relations, Decisions: input.Decisions}.domainPlan()
}

type planIssueInput struct {
	Ref                 string   `json:"ref,omitempty"`
	Type                string   `json:"type"`
	Title               string   `json:"title"`
	Description         *string  `json:"description,omitempty"`
	AcceptanceCriteria  *string  `json:"acceptance_criteria,omitempty"`
	Status              string   `json:"status,omitempty"`
	Priority            string   `json:"priority,omitempty"`
	ParentRef           *string  `json:"parent_ref,omitempty"`
	BlockedReason       *string  `json:"blocked_reason,omitempty"`
	Labels              []string `json:"labels,omitempty"`
	CreateMissingLabels bool     `json:"create_missing_labels,omitempty"`
}
type planRelationInput struct {
	SourceRef string `json:"source_ref"`
	TargetRef string `json:"target_ref"`
	Type      string `json:"type"`
}
type planDecisionInput struct {
	IssueRef *string `json:"issue_ref,omitempty"`
	Title    string  `json:"title"`
	Summary  string  `json:"summary"`
	Content  string  `json:"content"`
	Status   string  `json:"status,omitempty"`
}

func (input issuePlanInput) domainPlan() domain.IssuePlan {
	plan := domain.IssuePlan{
		Issues:    make([]domain.PlannedIssue, len(input.Issues)),
		Relations: make([]domain.PlannedRelation, len(input.Relations)),
		Decisions: make([]domain.PlannedDecision, len(input.Decisions)),
	}
	for i, issue := range input.Issues {
		plan.Issues[i] = domain.PlannedIssue{Ref: issue.Ref, Type: domain.Type(issue.Type), Title: issue.Title,
			Description: issue.Description, AcceptanceCriteria: issue.AcceptanceCriteria, Status: domain.Status(issue.Status),
			Priority: domain.Priority(issue.Priority), ParentRef: issue.ParentRef, BlockedReason: issue.BlockedReason,
			Labels: issue.Labels, CreateMissingLabels: issue.CreateMissingLabels}
	}
	for i, relation := range input.Relations {
		plan.Relations[i] = domain.PlannedRelation{SourceRef: relation.SourceRef, TargetRef: relation.TargetRef, Type: domain.RelationType(relation.Type)}
	}
	for i, decision := range input.Decisions {
		plan.Decisions[i] = domain.PlannedDecision{IssueRef: decision.IssueRef, Title: decision.Title, Summary: decision.Summary, Content: decision.Content, Status: decision.Status}
	}
	return plan
}

// patchInput records field presence independently from a null value.
type patchInput struct {
	Title              optionalString
	Description        optionalNullableString
	AcceptanceCriteria optionalNullableString
	Type               optionalString
	Priority           optionalString
	Status             optionalString
	ParentIssueID      optionalNullableString
	BlockedReason      optionalNullableString
	Labels             optionalStrings
}

type optionalString struct {
	set   bool
	value string
}

type optionalNullableString struct {
	set   bool
	value *string
}

type optionalStrings struct {
	set   bool
	value []string
}

func (input *patchInput) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for name, raw := range fields {
		switch name {
		case "title":
			input.Title.set, input.Title.value = true, ""
			if err := json.Unmarshal(raw, &input.Title.value); err != nil {
				return fmt.Errorf("title: %w", err)
			}
		case "description":
			if err := unmarshalNullableString(raw, &input.Description); err != nil {
				return fmt.Errorf("description: %w", err)
			}
		case "acceptance_criteria":
			if err := unmarshalNullableString(raw, &input.AcceptanceCriteria); err != nil {
				return fmt.Errorf("acceptance_criteria: %w", err)
			}
		case "type":
			input.Type.set, input.Type.value = true, ""
			if err := json.Unmarshal(raw, &input.Type.value); err != nil {
				return fmt.Errorf("type: %w", err)
			}
		case "priority":
			input.Priority.set, input.Priority.value = true, ""
			if err := json.Unmarshal(raw, &input.Priority.value); err != nil {
				return fmt.Errorf("priority: %w", err)
			}
		case "status":
			input.Status.set, input.Status.value = true, ""
			if err := json.Unmarshal(raw, &input.Status.value); err != nil {
				return fmt.Errorf("status: %w", err)
			}
		case "parent_issue_id":
			if err := unmarshalNullableString(raw, &input.ParentIssueID); err != nil {
				return fmt.Errorf("parent_issue_id: %w", err)
			}
		case "blocked_reason":
			if err := unmarshalNullableString(raw, &input.BlockedReason); err != nil {
				return fmt.Errorf("blocked_reason: %w", err)
			}
		case "labels":
			input.Labels.set, input.Labels.value = true, nil
			if bytes.Equal(raw, []byte("null")) {
				continue
			}
			if err := json.Unmarshal(raw, &input.Labels.value); err != nil {
				return fmt.Errorf("labels: %w", err)
			}
		default:
			return fmt.Errorf("unknown patch field %q", name)
		}
	}
	return nil
}

func unmarshalNullableString(raw json.RawMessage, destination *optionalNullableString) error {
	destination.set, destination.value = true, nil
	if bytes.Equal(raw, []byte("null")) {
		return nil
	}
	return json.Unmarshal(raw, &destination.value)
}

func (input patchInput) domainPatch() domain.IssuePatch {
	return domain.IssuePatch{
		Title:              domain.OptionalValue[string]{Set: input.Title.set, Value: input.Title.value},
		Description:        domain.OptionalString{Set: input.Description.set, Value: input.Description.value},
		AcceptanceCriteria: domain.OptionalString{Set: input.AcceptanceCriteria.set, Value: input.AcceptanceCriteria.value},
		Type:               domain.OptionalValue[domain.Type]{Set: input.Type.set, Value: domain.Type(input.Type.value)},
		Priority:           domain.OptionalValue[domain.Priority]{Set: input.Priority.set, Value: domain.Priority(input.Priority.value)},
		Status:             domain.OptionalValue[domain.Status]{Set: input.Status.set, Value: domain.Status(input.Status.value)},
		ParentID:           domain.OptionalString{Set: input.ParentIssueID.set, Value: input.ParentIssueID.value},
		BlockedReason:      domain.OptionalString{Set: input.BlockedReason.set, Value: input.BlockedReason.value},
		Labels:             domain.OptionalValue[[]string]{Set: input.Labels.set, Value: input.Labels.value},
	}
}

type errorOutput struct {
	Code      string          `json:"code"`
	Message   string          `json:"message"`
	Details   []domain.Detail `json:"details"`
	Retryable bool            `json:"retryable"`
}

type projectOutput struct {
	Project                projectDTO `json:"project"`
	Session                any        `json:"session"`
	AppVersion             string     `json:"app_version"`
	SchemaVersion          int        `json:"schema_version"`
	ConfigVersion          int        `json:"config_version"`
	Limits                 limitsDTO  `json:"limits"`
	SupportedIssueTypes    []string   `json:"supported_issue_types"`
	SupportedStatuses      []string   `json:"supported_statuses"`
	SupportedRelationTypes []string   `json:"supported_relation_types"`
	SupportedPriorities    []string   `json:"supported_priorities"`
	LatestEventID          int64      `json:"latest_event_id"`
}

type projectDTO struct {
	ID              string    `json:"id"`
	Name            *string   `json:"name"`
	Instructions    *string   `json:"instructions"`
	NextIssueNumber int64     `json:"next_issue_number"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type limitsDTO struct {
	DefaultIssueListLimit int `json:"default_issue_list_limit"`
	DefaultLabelListLimit int `json:"default_label_list_limit"`
	MaxCollectionLimit    int `json:"max_collection_limit"`
}

type labelDTO struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	NormalizedName string    `json:"normalized_name"`
	Description    *string   `json:"description"`
	CreatedAt      time.Time `json:"created_at"`
}

type labelListOutput struct {
	Items      []labelDTO `json:"items"`
	NextCursor *string    `json:"next_cursor"`
	HasMore    bool       `json:"has_more"`
}

type issueDTO struct {
	ID                 string     `json:"id"`
	DisplayID          string     `json:"display_id"`
	SequenceNo         int64      `json:"sequence_no"`
	Type               string     `json:"type"`
	Title              string     `json:"title"`
	Description        *string    `json:"description"`
	AcceptanceCriteria *string    `json:"acceptance_criteria"`
	Status             string     `json:"status"`
	Priority           string     `json:"priority"`
	ParentIssueID      *string    `json:"parent_issue_id"`
	BlockedReason      *string    `json:"blocked_reason"`
	Version            int64      `json:"version"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	ClosedAt           *time.Time `json:"closed_at"`
	ArchivedAt         *time.Time `json:"archived_at"`
	Labels             []labelDTO `json:"labels"`
}

type updateIssueOutput struct {
	Issue         issueDTO `json:"issue"`
	ChangedFields []string `json:"changed_fields"`
}

type issueListItemDTO struct {
	issueDTO
	EffectiveStatus        string `json:"effective_status"`
	UnresolvedBlockerCount int64  `json:"unresolved_blocker_count"`
	IsBlocked              bool   `json:"is_blocked"`
	IsClaimable            bool   `json:"is_claimable"`
}

type issueListOutput struct {
	Items      []issueListItemDTO `json:"items"`
	NextCursor *string            `json:"next_cursor"`
	HasMore    bool               `json:"has_more"`
}

type relationDTO struct {
	ID            string    `json:"id,omitempty"`
	SourceIssueID string    `json:"source_issue_id"`
	TargetIssueID string    `json:"target_issue_id"`
	Type          string    `json:"type"`
	CreatedAt     time.Time `json:"created_at,omitempty"`
}

type manageIssueRelationOutput struct {
	Relation       relationDTO        `json:"relation"`
	AffectedIssues []issueListItemDTO `json:"affected_issues"`
	Changed        bool               `json:"changed"`
}

type graphEdgeDTO struct {
	SourceIssueID string `json:"source_issue_id"`
	TargetIssueID string `json:"target_issue_id"`
	Type          string `json:"type"`
}

type graphSummaryDTO struct {
	NodeCount         int `json:"node_count"`
	EdgeCount         int `json:"edge_count"`
	EntryPointCount   int `json:"entry_point_count"`
	BlockingNodeCount int `json:"blocking_node_count"`
}

type graphOutput struct {
	RootIssueID      *string            `json:"root_issue_id,omitempty"`
	Nodes            []issueListItemDTO `json:"nodes"`
	Edges            []graphEdgeDTO     `json:"edges"`
	EntryPoints      []string           `json:"entry_points"`
	BlockingNodes    []string           `json:"blocking_nodes,omitempty"`
	Summary          graphSummaryDTO    `json:"summary"`
	Warnings         []string           `json:"warnings,omitempty"`
	Truncated        bool               `json:"truncated"`
	TruncationReason *string            `json:"truncation_reason,omitempty"`
}

type planSummaryDTO struct {
	IssueCount           int `json:"issue_count"`
	RelationCount        int `json:"relation_count"`
	DecisionCount        int `json:"decision_count"`
	LabelAssignmentCount int `json:"label_assignment_count"`
}
type normalizedPlanDTO struct {
	Issues    []planIssueInput    `json:"issues"`
	Relations []planRelationInput `json:"relations"`
	Decisions []planDecisionInput `json:"decisions"`
}
type planValidationOutput struct {
	Valid          bool              `json:"valid"`
	Errors         []domain.Detail   `json:"errors"`
	Warnings       []string          `json:"warnings"`
	Summary        planSummaryDTO    `json:"summary"`
	NormalizedPlan normalizedPlanDTO `json:"normalized_plan"`
}
type createdPlanIssueDTO struct {
	Ref   string   `json:"ref,omitempty"`
	Issue issueDTO `json:"issue"`
}
type decisionDTO struct {
	ID        string    `json:"id"`
	IssueID   *string   `json:"issue_id,omitempty"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary"`
	Content   string    `json:"content"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}
type applyIssuePlanOutput struct {
	CreatedIssues    []createdPlanIssueDTO `json:"created_issues"`
	CreatedRelations []relationDTO         `json:"created_relations"`
	CreatedDecisions []decisionDTO         `json:"created_decisions"`
	LatestEventID    int64                 `json:"latest_event_id"`
}

func planValidationOutputFromDomain(value domain.PlanValidation) planValidationOutput {
	plan := issuePlanInputFromDomain(value.NormalizedPlan)
	return planValidationOutput{Valid: value.Valid, Errors: append([]domain.Detail{}, value.Errors...), Warnings: append([]string{}, value.Warnings...),
		Summary:        planSummaryDTO{IssueCount: value.Summary.IssueCount, RelationCount: value.Summary.RelationCount, DecisionCount: value.Summary.DecisionCount, LabelAssignmentCount: value.Summary.LabelAssignmentCount},
		NormalizedPlan: normalizedPlanDTO{Issues: plan.Issues, Relations: plan.Relations, Decisions: plan.Decisions}}
}
func issuePlanInputFromDomain(value domain.IssuePlan) issuePlanInput {
	result := issuePlanInput{Issues: make([]planIssueInput, len(value.Issues)), Relations: make([]planRelationInput, len(value.Relations)), Decisions: make([]planDecisionInput, len(value.Decisions))}
	for i, issue := range value.Issues {
		result.Issues[i] = planIssueInput{Ref: issue.Ref, Type: string(issue.Type), Title: issue.Title, Description: issue.Description, AcceptanceCriteria: issue.AcceptanceCriteria, Status: string(issue.Status), Priority: string(issue.Priority), ParentRef: issue.ParentRef, BlockedReason: issue.BlockedReason, Labels: issue.Labels, CreateMissingLabels: issue.CreateMissingLabels}
	}
	for i, relation := range value.Relations {
		result.Relations[i] = planRelationInput{SourceRef: relation.SourceRef, TargetRef: relation.TargetRef, Type: string(relation.Type)}
	}
	for i, decision := range value.Decisions {
		result.Decisions[i] = planDecisionInput{IssueRef: decision.IssueRef, Title: decision.Title, Summary: decision.Summary, Content: decision.Content, Status: decision.Status}
	}
	return result
}
func applyIssuePlanOutputFromPort(value ports.ApplyIssuePlanResult) applyIssuePlanOutput {
	result := applyIssuePlanOutput{CreatedIssues: make([]createdPlanIssueDTO, len(value.CreatedIssues)), CreatedRelations: make([]relationDTO, len(value.CreatedRelations)), CreatedDecisions: make([]decisionDTO, len(value.CreatedDecisions)), LatestEventID: value.LatestEventID}
	for i, issue := range value.CreatedIssues {
		result.CreatedIssues[i] = createdPlanIssueDTO{Ref: issue.Ref, Issue: issueDTOFromDomain(issue.Issue)}
	}
	for i, relation := range value.CreatedRelations {
		result.CreatedRelations[i] = relationDTOFromDomain(relation)
	}
	for i, decision := range value.CreatedDecisions {
		result.CreatedDecisions[i] = decisionDTO{ID: decision.ID, IssueID: decision.IssueID, Title: decision.Title, Summary: decision.Summary, Content: decision.Content, Status: decision.Status, CreatedAt: decision.CreatedAt}
	}
	return result
}

func projectDTOFromDomain(project domain.Project, includeInstructions bool) projectDTO {
	instructions := project.Instructions
	if !includeInstructions {
		instructions = nil
	}
	return projectDTO{
		ID: project.ID, Name: project.Name, Instructions: instructions, NextIssueNumber: project.NextIssueNumber,
		CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt,
	}
}

func labelDTOFromDomain(label domain.Label) labelDTO {
	return labelDTO{
		ID: label.ID, Name: label.Name, NormalizedName: label.NormalizedName,
		Description: label.Description, CreatedAt: label.CreatedAt,
	}
}

func issueDTOFromDomain(issue domain.Issue) issueDTO {
	labels := make([]labelDTO, len(issue.Labels))
	for i, label := range issue.Labels {
		labels[i] = labelDTOFromDomain(label)
	}
	return issueDTO{
		ID: issue.ID, DisplayID: issue.DisplayID, SequenceNo: issue.SequenceNo, Type: string(issue.Type),
		Title: issue.Title, Description: issue.Description, AcceptanceCriteria: issue.AcceptanceCriteria,
		Status: string(issue.Status), Priority: string(issue.Priority), ParentIssueID: issue.ParentID,
		BlockedReason: issue.BlockedReason, Version: issue.Version, CreatedAt: issue.CreatedAt,
		UpdatedAt: issue.UpdatedAt, ClosedAt: issue.ClosedAt, ArchivedAt: issue.ArchivedAt, Labels: labels,
	}
}

func relationDTOFromDomain(relation domain.IssueRelation) relationDTO {
	return relationDTO{
		ID: relation.ID, SourceIssueID: relation.SourceIssueID, TargetIssueID: relation.TargetIssueID,
		Type: string(relation.Type), CreatedAt: relation.CreatedAt,
	}
}

func graphOutputFromDomain(graph domain.GraphResult) graphOutput {
	nodes := make([]issueListItemDTO, len(graph.Nodes))
	for index, node := range graph.Nodes {
		nodes[index] = issueListItemDTO{
			issueDTO: issueDTOFromDomain(node.Issue), EffectiveStatus: string(node.EffectiveStatus),
			UnresolvedBlockerCount: node.UnresolvedBlockerCount, IsBlocked: node.IsBlocked, IsClaimable: node.IsClaimable,
		}
	}
	edges := make([]graphEdgeDTO, len(graph.Edges))
	for index, edge := range graph.Edges {
		edges[index] = graphEdgeDTO{SourceIssueID: edge.SourceIssueID, TargetIssueID: edge.TargetIssueID, Type: edge.Type}
	}
	return graphOutput{
		RootIssueID: graph.RootIssueID, Nodes: nodes, Edges: edges,
		EntryPoints: append([]string{}, graph.EntryPoints...), BlockingNodes: append([]string{}, graph.BlockingNodes...),
		Summary: graphSummaryDTO{NodeCount: graph.Summary.NodeCount, EdgeCount: graph.Summary.EdgeCount,
			EntryPointCount: graph.Summary.EntryPointCount, BlockingNodeCount: graph.Summary.BlockingNodeCount},
		Warnings: append([]string{}, graph.Warnings...), Truncated: graph.Truncated, TruncationReason: graph.TruncationReason,
	}
}
