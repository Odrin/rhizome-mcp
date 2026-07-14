package domain

import "strings"

// CreateIssueInput is the validated input for creating an issue. Empty Status
// and Priority default to open and medium. When Status is blocked,
// BlockedReason must be non-nil and non-blank; for every other status,
// BlockedReason must be nil. An epic must not have ParentID, while a task or
// bug may have one; storage verifies that a supplied parent is an active epic.
type CreateIssueInput struct {
	Type               Type
	Title              string
	Description        *string
	AcceptanceCriteria *string
	Status             Status
	Priority           Priority
	ParentID           *string
	BlockedReason      *string
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

	return CreateIssueInput{
		Type:               input.Type,
		Title:              input.Title,
		Description:        copyString(input.Description),
		AcceptanceCriteria: copyString(input.AcceptanceCriteria),
		Status:             status,
		Priority:           priority,
		ParentID:           copyString(input.ParentID),
		BlockedReason:      copyString(input.BlockedReason),
	}, nil
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
