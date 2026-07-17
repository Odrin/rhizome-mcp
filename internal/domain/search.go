package domain

import "strings"

const (
	MaxSearchQueryRunes = 1_000
	MaxChangeEventTypes = 100
	MaxEventTypeRunes   = 128
)

type SearchEntityType string

const (
	SearchEntityTypeIssue       SearchEntityType = "issue"
	SearchEntityTypeComment     SearchEntityType = "comment"
	SearchEntityTypeDecision    SearchEntityType = "decision"
	SearchEntityTypeReview      SearchEntityType = "review"
	SearchEntityTypeAttemptNote SearchEntityType = "attempt_note"
)

func (value SearchEntityType) Valid() bool {
	switch value {
	case SearchEntityTypeIssue, SearchEntityTypeComment, SearchEntityTypeDecision, SearchEntityTypeReview, SearchEntityTypeAttemptNote:
		return true
	default:
		return false
	}
}

type SearchInput struct {
	Query           string
	EntityTypes     []SearchEntityType
	IssueID         *string
	EpicID          *string
	Statuses        []Status
	Labels          []string
	IncludeArchived bool
	Limit           int
	Cursor          string
	SnippetLength   int
}

func (input SearchInput) Validate() (SearchInput, error) {
	if err := ValidateText("query", input.Query, MaxSearchQueryRunes); err != nil {
		return SearchInput{}, err
	}
	if strings.TrimSpace(input.Query) == "" {
		return SearchInput{}, validationError("query", "REQUIRED", "is required")
	}
	if input.Limit < 0 || input.Limit > MaxSearchResults {
		return SearchInput{}, validationError("limit", "OUT_OF_RANGE", "must be 0 (default) or between 1 and 100")
	}
	if input.SnippetLength < 0 || input.SnippetLength > MaxSearchSnippetRunes {
		return SearchInput{}, validationError("snippet_length", "OUT_OF_RANGE", "must be 0 (default) or between 1 and 1000")
	}
	if input.Cursor != "" {
		if err := ValidateText("cursor", input.Cursor, 4096); err != nil {
			return SearchInput{}, err
		}
	}
	entityTypes, err := copySearchEntityTypes(input.EntityTypes)
	if err != nil {
		return SearchInput{}, err
	}
	statuses, err := copyIssueListEnums("statuses", input.Statuses, func(value Status) bool { return value.Valid() })
	if err != nil {
		return SearchInput{}, err
	}
	labels, err := normalizeIssueQueryLabels(input.Labels)
	if err != nil {
		return SearchInput{}, err
	}
	issueID, err := normalizeOptionalSearchIssueID("issue_id", input.IssueID)
	if err != nil {
		return SearchInput{}, err
	}
	epicID, err := normalizeOptionalSearchIssueID("epic_id", input.EpicID)
	if err != nil {
		return SearchInput{}, err
	}
	limit := input.Limit
	if limit == 0 {
		limit = 20
	}
	snippetLength := input.SnippetLength
	if snippetLength == 0 {
		snippetLength = 300
	}
	return SearchInput{
		Query:           input.Query,
		EntityTypes:     entityTypes,
		IssueID:         issueID,
		EpicID:          epicID,
		Statuses:        statuses,
		Labels:          labels,
		IncludeArchived: input.IncludeArchived,
		Limit:           limit,
		Cursor:          input.Cursor,
		SnippetLength:   snippetLength,
	}, nil
}

func copySearchEntityTypes(values []SearchEntityType) ([]SearchEntityType, error) {
	result := append([]SearchEntityType(nil), values...)
	seen := make(map[SearchEntityType]struct{}, len(result))
	for _, value := range result {
		if !value.Valid() {
			return nil, invalidEnum("entity_types", string(value))
		}
		if _, exists := seen[value]; exists {
			return nil, validationError("entity_types", "DUPLICATE", "must not contain duplicate values")
		}
		seen[value] = struct{}{}
	}
	return result, nil
}

func normalizeOptionalSearchIssueID(field string, value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	identifier, err := ParseIssueIdentifier(*value)
	if err != nil {
		return nil, validationError(field, "INVALID_IDENTIFIER", "must be a canonical ULID or ISSUE-N")
	}
	normalized := identifier.Value
	return &normalized, nil
}

type SearchResult struct {
	EntityType SearchEntityType
	EntityID   string
	IssueID    *string
	Title      string
	Snippet    string
	Score      float64
}

type SearchPage struct {
	Results    []SearchResult
	NextCursor *string
	HasMore    bool
}

func CloneSearchPage(page SearchPage) SearchPage {
	page.Results = append([]SearchResult(nil), page.Results...)
	for index := range page.Results {
		page.Results[index].IssueID = copyOptionalString(page.Results[index].IssueID)
	}
	page.NextCursor = copyOptionalString(page.NextCursor)
	if page.Results == nil {
		page.Results = []SearchResult{}
	}
	return page
}

type GetChangesInput struct {
	SinceEventID int64
	IssueID      *string
	EventTypes   []string
	Limit        int
}

func (input GetChangesInput) Validate() (GetChangesInput, error) {
	if input.SinceEventID < 0 {
		return GetChangesInput{}, validationError("since_event_id", "OUT_OF_RANGE", "must be zero or greater")
	}
	if input.Limit < 0 || input.Limit > 200 {
		return GetChangesInput{}, validationError("limit", "OUT_OF_RANGE", "must be 0 (default) or between 1 and 200")
	}
	issueID, err := normalizeOptionalSearchIssueID("issue_id", input.IssueID)
	if err != nil {
		return GetChangesInput{}, err
	}
	eventTypes, err := CopyBounded("event_types", input.EventTypes, MaxChangeEventTypes)
	if err != nil {
		return GetChangesInput{}, err
	}
	seen := make(map[string]struct{}, len(eventTypes))
	for _, eventType := range eventTypes {
		if err := ValidateText("event_types", eventType, MaxEventTypeRunes); err != nil {
			return GetChangesInput{}, err
		}
		if strings.TrimSpace(eventType) == "" {
			return GetChangesInput{}, validationError("event_types", "REQUIRED", "must not contain empty values")
		}
		if _, exists := seen[eventType]; exists {
			return GetChangesInput{}, validationError("event_types", "DUPLICATE", "must not contain duplicate values")
		}
		seen[eventType] = struct{}{}
	}
	limit := input.Limit
	if limit == 0 {
		limit = 50
	}
	return GetChangesInput{
		SinceEventID: input.SinceEventID,
		IssueID:      issueID,
		EventTypes:   eventTypes,
		Limit:        limit,
	}, nil
}

type ChangesPage struct {
	Events        []IssueEvent
	LatestEventID int64
	HasMore       bool
	NextEventID   int64
}

func CloneChangesPage(page ChangesPage) ChangesPage {
	events := page.Events
	page.Events = make([]IssueEvent, len(events))
	for index, event := range events {
		page.Events[index] = CloneIssueEvent(event)
	}
	if page.Events == nil {
		page.Events = []IssueEvent{}
	}
	return page
}
