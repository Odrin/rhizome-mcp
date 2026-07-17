package domain

import (
	"encoding/json"
	"strings"
)

// CreateIssueInput is the validated input for creating an issue. Empty Status
// and Priority default to open and medium. When Status is blocked,
// BlockedReason must be non-nil and non-blank; for every other status,
// BlockedReason must be nil. An epic must not have ParentID, while a task or
// bug may have one; storage verifies that a supplied parent is an active epic.
// Labels are a replacement set for the new issue. Missing labels are created
// only when CreateMissingLabels is true.
type CreateIssueInput struct {
	Type                Type
	Title               string
	Description         *string
	AcceptanceCriteria  *string
	Status              Status
	Priority            Priority
	ParentID            *string
	BlockedReason       *string
	Labels              []string
	CreateMissingLabels bool
	IdempotencyKey      *string
}

// Validate applies creation defaults and validates invariants that do not
// require current storage state. The returned value owns copies of all optional
// strings and is safe to retain after the caller changes its input.
func (input CreateIssueInput) Validate() (CreateIssueInput, error) {
	if !input.Type.Valid() {
		return CreateIssueInput{}, invalidEnum("type", string(input.Type))
	}
	if err := ValidateText("title", input.Title, MaxTitleRunes); err != nil {
		return CreateIssueInput{}, err
	}
	if strings.TrimSpace(input.Title) == "" {
		return CreateIssueInput{}, validationError("title", "REQUIRED", "must not be blank")
	}
	if err := validateOptionalText("description", input.Description, MaxDescriptionRunes); err != nil {
		return CreateIssueInput{}, err
	}
	if err := validateOptionalText("acceptance_criteria", input.AcceptanceCriteria, MaxAcceptanceCriteriaRunes); err != nil {
		return CreateIssueInput{}, err
	}

	status := input.Status
	if status == "" {
		status = StatusOpen
	}
	if !status.Valid() {
		return CreateIssueInput{}, invalidEnum("status", string(status))
	}
	priority := input.Priority
	if priority == "" {
		priority = PriorityMedium
	}
	if !priority.Valid() {
		return CreateIssueInput{}, invalidEnum("priority", string(priority))
	}

	if input.ParentID != nil {
		if err := ValidateText("parent_id", *input.ParentID, -1); err != nil {
			return CreateIssueInput{}, err
		}
		identifier, err := ParseIssueIdentifier(*input.ParentID)
		if err != nil {
			return CreateIssueInput{}, validationError("parent_id", "INVALID_IDENTIFIER", "must be a canonical ULID or ISSUE-N")
		}
		input.ParentID = &identifier.Value
	}
	if input.Type == TypeEpic && input.ParentID != nil {
		return CreateIssueInput{}, NewError(
			CodeInvalidEpicParent,
			"epic issues cannot have a parent",
			false,
			Detail{Field: "parent_id", Code: CodeInvalidEpicParent},
		)
	}

	if status == StatusBlocked {
		if input.BlockedReason == nil || strings.TrimSpace(*input.BlockedReason) == "" {
			return CreateIssueInput{}, NewError(
				CodeInvalidArgument,
				"blocked_reason is required when status is blocked",
				false,
				Detail{Field: "blocked_reason", Code: "REQUIRED"},
			)
		}
		if err := ValidateText("blocked_reason", *input.BlockedReason, -1); err != nil {
			return CreateIssueInput{}, err
		}
	} else if input.BlockedReason != nil {
		return CreateIssueInput{}, NewError(
			CodeInvalidArgument,
			"blocked_reason is only allowed when status is blocked",
			false,
			Detail{Field: "blocked_reason", Code: "FORBIDDEN"},
		)
	}
	labels, err := NormalizeLabelNames(input.Labels)
	if err != nil {
		return CreateIssueInput{}, err
	}
	var idempotencyKey *string
	if input.IdempotencyKey != nil {
		if err := ValidateText("idempotency_key", *input.IdempotencyKey, MaxIdempotencyKeyRunes); err != nil {
			return CreateIssueInput{}, err
		}
		key := strings.TrimSpace(*input.IdempotencyKey)
		if key == "" {
			return CreateIssueInput{}, validationError("idempotency_key", "REQUIRED", "must not be blank")
		}
		idempotencyKey = &key
	}

	return CreateIssueInput{
		Type:                input.Type,
		Title:               input.Title,
		Description:         copyString(input.Description),
		AcceptanceCriteria:  copyString(input.AcceptanceCriteria),
		Status:              status,
		Priority:            priority,
		ParentID:            copyString(input.ParentID),
		BlockedReason:       copyString(input.BlockedReason),
		Labels:              labels,
		CreateMissingLabels: input.CreateMissingLabels,
		IdempotencyKey:      idempotencyKey,
	}, nil
}

// CanonicalCreateIssueRequest returns deterministic JSON for a normalized
// create request. The idempotency key is intentionally excluded.
func CanonicalCreateIssueRequest(input CreateIssueInput) ([]byte, error) {
	request := struct {
		Type                Type     `json:"type"`
		Title               string   `json:"title"`
		Description         *string  `json:"description"`
		AcceptanceCriteria  *string  `json:"acceptance_criteria"`
		Status              Status   `json:"status"`
		Priority            Priority `json:"priority"`
		ParentID            *string  `json:"parent_id"`
		BlockedReason       *string  `json:"blocked_reason"`
		Labels              []string `json:"labels"`
		CreateMissingLabels bool     `json:"create_missing_labels"`
	}{
		Type:                input.Type,
		Title:               input.Title,
		Description:         copyString(input.Description),
		AcceptanceCriteria:  copyString(input.AcceptanceCriteria),
		Status:              input.Status,
		Priority:            input.Priority,
		ParentID:            copyString(input.ParentID),
		BlockedReason:       copyString(input.BlockedReason),
		Labels:              append([]string(nil), input.Labels...),
		CreateMissingLabels: input.CreateMissingLabels,
	}
	return json.Marshal(request)
}

func validateOptionalText(field string, value *string, maximum int) error {
	if value == nil {
		return nil
	}
	return ValidateText(field, *value, maximum)
}

func copyString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
