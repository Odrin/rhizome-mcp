package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"rhizome-mcp/internal/domain"
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
	EffectiveStatus string `json:"effective_status"`
	IsBlocked       bool   `json:"is_blocked"`
	IsClaimable     bool   `json:"is_claimable"`
}

type issueListOutput struct {
	Items      []issueListItemDTO `json:"items"`
	NextCursor *string            `json:"next_cursor"`
	HasMore    bool               `json:"has_more"`
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
