package domain

import "sort"

// ListIssuesInput requests one deterministic page of issue projections.
// Labels use any-label semantics: an issue matches when it has at least one
// of the supplied labels.
type ListIssuesInput struct {
	Types             []Type
	Statuses          []Status
	EffectiveStatuses []EffectiveStatus
	Priorities        []Priority
	Labels            []string
	ParentIssueID     *string
	IsBlocked         *bool
	IsClaimable       *bool
	IncludeArchived   bool
	Limit             int
	Cursor            string
}

// IssueProjection is an issue projection enriched with the computed fields
// exposed by list_issues.
type IssueProjection struct {
	Issue
	EffectiveStatus        EffectiveStatus
	UnresolvedBlockerCount int64
	IsBlocked              bool
	IsClaimable            bool
	ActiveAttemptID        *string
}

// IssueList is one cursor-paginated deterministic issue page.
type IssueList struct {
	Items      []IssueProjection
	NextCursor *string
	HasMore    bool
}

// Validate validates and defensively copies all list input. A zero limit
// defaults to 20 and the maximum page size is 100.
func (input ListIssuesInput) Validate() (ListIssuesInput, error) {
	if input.Limit < 0 || input.Limit > 100 {
		return ListIssuesInput{}, validationError("limit", "OUT_OF_RANGE", "must be 0 (default) or between 1 and 100")
	}
	types, err := copyIssueListEnums("types", input.Types, func(value Type) bool { return value.Valid() })
	if err != nil {
		return ListIssuesInput{}, err
	}
	statuses, err := copyIssueListEnums("statuses", input.Statuses, func(value Status) bool { return value.Valid() })
	if err != nil {
		return ListIssuesInput{}, err
	}
	effectiveStatuses, err := copyIssueListEnums("effective_statuses", input.EffectiveStatuses, func(value EffectiveStatus) bool { return value.Valid() })
	if err != nil {
		return ListIssuesInput{}, err
	}
	priorities, err := copyIssueListEnums("priorities", input.Priorities, func(value Priority) bool { return value.Valid() })
	if err != nil {
		return ListIssuesInput{}, err
	}
	labels, err := normalizeIssueQueryLabels(input.Labels)
	if err != nil {
		return ListIssuesInput{}, err
	}

	var parentID *string
	if input.ParentIssueID != nil {
		identifier, err := ParseIssueIdentifier(*input.ParentIssueID)
		if err != nil {
			return ListIssuesInput{}, validationError("parent_issue_id", "INVALID_IDENTIFIER", "must be a canonical ULID or ISSUE-N")
		}
		parentID = &identifier.Value
	}
	limit := input.Limit
	if limit == 0 {
		limit = 20
	}
	return ListIssuesInput{
		Types:             types,
		Statuses:          statuses,
		EffectiveStatuses: effectiveStatuses,
		Priorities:        priorities,
		Labels:            labels,
		ParentIssueID:     parentID,
		IsBlocked:         copyBool(input.IsBlocked),
		IsClaimable:       copyBool(input.IsClaimable),
		IncludeArchived:   input.IncludeArchived,
		Limit:             limit,
		Cursor:            input.Cursor,
	}, nil
}

func copyIssueListEnums[T ~string](field string, values []T, valid func(T) bool) ([]T, error) {
	result := append([]T(nil), values...)
	seen := make(map[T]struct{}, len(result))
	for _, value := range result {
		if !valid(value) {
			return nil, invalidEnum(field, stringValue(value))
		}
		if _, exists := seen[value]; exists {
			return nil, validationError(field, "DUPLICATE", "must not contain duplicate values")
		}
		seen[value] = struct{}{}
	}
	return result, nil
}

func stringValue[T ~string](value T) string {
	return string(value)
}

func normalizeIssueQueryLabels(names []string) ([]string, error) {
	values, err := CopyBounded("labels", names, MaxLabelsPerIssue)
	if err != nil {
		return nil, err
	}
	type named struct {
		display    string
		normalized string
	}
	normalized := make([]named, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, name := range values {
		display, canonical, err := NormalizeLabelName(name)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		normalized = append(normalized, named{display: display, normalized: canonical})
	}
	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i].normalized < normalized[j].normalized
	})
	result := make([]string, len(normalized))
	for i, value := range normalized {
		result[i] = value.display
	}
	return result, nil
}

func copyBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
