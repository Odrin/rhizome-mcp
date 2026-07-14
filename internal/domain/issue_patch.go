package domain

import (
	"errors"
	"sort"
	"strings"
)

// OptionalValue represents an optionally supplied non-null patch field. Set is
// false for an absent field.
type OptionalValue[T any] struct {
	Set   bool
	Value T
}

// OptionalString represents an optionally supplied nullable string patch
// field. Set false means absent, while Set true with Value nil means explicit
// null.
type OptionalString struct {
	Set   bool
	Value *string
}

// IssuePatch contains the fields supported by this internal update slice.
// Labels are intentionally unsupported in this internal slice.
type IssuePatch struct {
	Title              OptionalValue[string]
	Description        OptionalString
	AcceptanceCriteria OptionalString
	Type               OptionalValue[Type]
	Priority           OptionalValue[Priority]
	Status             OptionalValue[Status]
	ParentID           OptionalString
	BlockedReason      OptionalString
}

// UpdateIssueInput is a typed request to patch one issue. IssueID accepts a
// canonical ULID or ISSUE-N. ExpectedVersion is required. Labels are
// intentionally unsupported in this internal slice.
type UpdateIssueInput struct {
	IssueID         string
	ExpectedVersion int64
	Changes         IssuePatch
}

// Validate checks request-local patch rules and normalizes issue references.
// Rules that require the current projection, including transitions and the
// resulting parent hierarchy, are checked by ApplyIssuePatch.
func (input UpdateIssueInput) Validate() (normalized UpdateIssueInput, err error) {
	defer func() {
		err = normalizeUpdateValidationError(err)
	}()
	if input.ExpectedVersion < 1 {
		return UpdateIssueInput{}, validationError("expected_version", "REQUIRED", "must be at least 1")
	}
	identifier, err := ParseIssueIdentifier(input.IssueID)
	if err != nil {
		return UpdateIssueInput{}, err
	}
	patch := input.Changes
	if !patch.anySet() {
		return UpdateIssueInput{}, validationError("changes", "REQUIRED", "must contain at least one changed field")
	}
	if patch.Title.Set {
		if err := ValidateText("title", patch.Title.Value, MaxTitleRunes); err != nil {
			return UpdateIssueInput{}, err
		}
		if strings.TrimSpace(patch.Title.Value) == "" {
			return UpdateIssueInput{}, validationError("title", "REQUIRED", "must not be blank")
		}
	}
	if patch.Description.Set {
		if err := validateOptionalText("description", patch.Description.Value, MaxDescriptionRunes); err != nil {
			return UpdateIssueInput{}, err
		}
	}
	if patch.AcceptanceCriteria.Set {
		if err := validateOptionalText("acceptance_criteria", patch.AcceptanceCriteria.Value, MaxAcceptanceCriteriaRunes); err != nil {
			return UpdateIssueInput{}, err
		}
	}
	if patch.Type.Set && !patch.Type.Value.Valid() {
		return UpdateIssueInput{}, invalidEnum("type", string(patch.Type.Value))
	}
	if patch.Priority.Set && !patch.Priority.Value.Valid() {
		return UpdateIssueInput{}, invalidEnum("priority", string(patch.Priority.Value))
	}
	if patch.Status.Set && !patch.Status.Value.Valid() {
		return UpdateIssueInput{}, invalidEnum("status", string(patch.Status.Value))
	}
	if patch.ParentID.Set && patch.ParentID.Value != nil {
		if err := ValidateText("parent_id", *patch.ParentID.Value, -1); err != nil {
			return UpdateIssueInput{}, err
		}
		parent, err := ParseIssueIdentifier(*patch.ParentID.Value)
		if err != nil {
			return UpdateIssueInput{}, validationError("parent_id", "INVALID_IDENTIFIER", "must be a canonical ULID or ISSUE-N")
		}
		patch.ParentID.Value = &parent.Value
	}
	if patch.BlockedReason.Set && patch.BlockedReason.Value != nil {
		if err := ValidateText("blocked_reason", *patch.BlockedReason.Value, -1); err != nil {
			return UpdateIssueInput{}, err
		}
	}
	return UpdateIssueInput{
		IssueID:         identifier.Value,
		ExpectedVersion: input.ExpectedVersion,
		Changes:         copyIssuePatch(patch),
	}, nil
}

func normalizeUpdateValidationError(err error) error {
	if err == nil {
		return nil
	}
	var domainErr *Error
	if !errors.As(err, &domainErr) || domainErr.Code != CodeInvalidArgument {
		return err
	}
	return NewError(CodeValidationError, domainErr.Message, false, domainErr.Details...)
}

// ApplyIssuePatch applies a validated patch to current and checks rules that
// depend on current state. If Status is absent, blocked_reason is allowed only
// for a currently blocked issue and must be a non-null, non-blank replacement.
// If Status is present, entering blocked requires a non-null reason; changing
// to every other status clears the reason and accepts only an absent or null
// blocked_reason. This makes absent, null, and value semantics safe for future
// JSON patch mapping.
func ApplyIssuePatch(current Issue, patch IssuePatch) (Issue, []string, error) {
	result := current
	changed := make(map[string]bool)
	if patch.Title.Set {
		result.Title = patch.Title.Value
		changed["title"] = true
	}
	if patch.Description.Set {
		result.Description = copyString(patch.Description.Value)
		changed["description"] = true
	}
	if patch.AcceptanceCriteria.Set {
		result.AcceptanceCriteria = copyString(patch.AcceptanceCriteria.Value)
		changed["acceptance_criteria"] = true
	}
	if patch.Type.Set {
		result.Type = patch.Type.Value
		changed["type"] = true
	}
	if patch.Priority.Set {
		result.Priority = patch.Priority.Value
		changed["priority"] = true
	}
	if patch.ParentID.Set {
		result.ParentID = copyString(patch.ParentID.Value)
		changed["parent_id"] = true
	}

	if patch.Status.Set {
		if patch.Status.Value == StatusBlocked {
			if !patch.BlockedReason.Set || patch.BlockedReason.Value == nil {
				return Issue{}, nil, blockedReasonRequired()
			}
			reason, err := ApplyStatusTransition(current.Status, patch.Status.Value, *patch.BlockedReason.Value)
			if err != nil {
				return Issue{}, nil, err
			}
			result.BlockedReason = &reason
			changed["blocked_reason"] = true
		} else {
			if patch.BlockedReason.Set && patch.BlockedReason.Value != nil {
				return Issue{}, nil, blockedReasonForbidden()
			}
			reason, err := ApplyStatusTransition(current.Status, patch.Status.Value, "")
			if err != nil {
				return Issue{}, nil, err
			}
			result.BlockedReason = nil
			if current.BlockedReason != nil || patch.BlockedReason.Set {
				changed["blocked_reason"] = true
			}
			_ = reason
		}
		result.Status = patch.Status.Value
		changed["status"] = true
	} else if patch.BlockedReason.Set {
		if current.Status != StatusBlocked || patch.BlockedReason.Value == nil || strings.TrimSpace(*patch.BlockedReason.Value) == "" {
			return Issue{}, nil, blockedReasonRequired()
		}
		result.BlockedReason = copyString(patch.BlockedReason.Value)
		changed["blocked_reason"] = true
	}

	if result.Type == TypeEpic && result.ParentID != nil {
		return Issue{}, nil, NewError(
			CodeInvalidEpicParent,
			"epic issues cannot have a parent",
			false,
			Detail{Field: "parent_id", Code: CodeInvalidEpicParent},
		)
	}
	return result, orderedChangedFields(changed), nil
}

func (patch IssuePatch) anySet() bool {
	return patch.Title.Set || patch.Description.Set || patch.AcceptanceCriteria.Set ||
		patch.Type.Set || patch.Priority.Set || patch.Status.Set ||
		patch.ParentID.Set || patch.BlockedReason.Set
}

func copyIssuePatch(patch IssuePatch) IssuePatch {
	patch.Description.Value = copyString(patch.Description.Value)
	patch.AcceptanceCriteria.Value = copyString(patch.AcceptanceCriteria.Value)
	patch.ParentID.Value = copyString(patch.ParentID.Value)
	patch.BlockedReason.Value = copyString(patch.BlockedReason.Value)
	return patch
}

func orderedChangedFields(fields map[string]bool) []string {
	result := make([]string, 0, len(fields))
	for field := range fields {
		result = append(result, field)
	}
	sort.Strings(result)
	return result
}

func blockedReasonRequired() *Error {
	return NewError(CodeInvalidArgument, "blocked_reason is required when status is blocked", false,
		Detail{Field: "blocked_reason", Code: "REQUIRED"})
}

func blockedReasonForbidden() *Error {
	return NewError(CodeInvalidArgument, "blocked_reason is only allowed when status is blocked", false,
		Detail{Field: "blocked_reason", Code: "FORBIDDEN"})
}
