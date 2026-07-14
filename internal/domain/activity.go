package domain

import (
	"encoding/json"
	"strconv"
	"time"

	"rhizome-mcp/internal/ids"
)

// ActivityCategory identifies one filterable activity collection.
type ActivityCategory string

const (
	ActivityCategoryComments     ActivityCategory = "comments"
	ActivityCategoryDecisions    ActivityCategory = "decisions"
	ActivityCategoryAttempts     ActivityCategory = "attempts"
	ActivityCategoryAttemptNotes ActivityCategory = "attempt_notes"
	ActivityCategoryEvents       ActivityCategory = "events"
	ActivityCategoryArtifacts    ActivityCategory = "artifacts"
)

func (value ActivityCategory) Valid() bool {
	switch value {
	case ActivityCategoryComments, ActivityCategoryDecisions, ActivityCategoryAttempts,
		ActivityCategoryAttemptNotes, ActivityCategoryEvents, ActivityCategoryArtifacts:
		return true
	default:
		return false
	}
}

// AllActivityCategories is the canonical order used by the default filter.
// Callers must copy it before retaining or modifying the values.
var AllActivityCategories = []ActivityCategory{
	ActivityCategoryComments,
	ActivityCategoryDecisions,
	ActivityCategoryAttempts,
	ActivityCategoryAttemptNotes,
	ActivityCategoryEvents,
	ActivityCategoryArtifacts,
}

// ActivityEntityType identifies the concrete entity represented by an item.
type ActivityEntityType string

const (
	ActivityEntityTypeComment     ActivityEntityType = "comment"
	ActivityEntityTypeDecision    ActivityEntityType = "decision"
	ActivityEntityTypeAttempt     ActivityEntityType = "attempt"
	ActivityEntityTypeAttemptNote ActivityEntityType = "attempt_note"
	ActivityEntityTypeEvent       ActivityEntityType = "event"
	ActivityEntityTypeArtifact    ActivityEntityType = "artifact"
)

func (value ActivityEntityType) Valid() bool {
	switch value {
	case ActivityEntityTypeComment, ActivityEntityTypeDecision, ActivityEntityTypeAttempt,
		ActivityEntityTypeAttemptNote, ActivityEntityTypeEvent, ActivityEntityTypeArtifact:
		return true
	default:
		return false
	}
}

// ActivityOrder identifies the deterministic activity ordering.
type ActivityOrder string

const ActivityOrderNewestFirst ActivityOrder = "newest_first"

func (value ActivityOrder) Valid() bool { return value == ActivityOrderNewestFirst }

// IssueEvent is one append-only project or issue event.
type IssueEvent struct {
	ID        int64           `json:"id"`
	IssueID   *string         `json:"issue_id"`
	EventType string          `json:"event_type"`
	SessionID *string         `json:"session_id"`
	AttemptID *string         `json:"attempt_id"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// CloneIssueEvent returns an event with no shared pointer or byte-slice data.
func CloneIssueEvent(event IssueEvent) IssueEvent {
	event.IssueID = copyOptionalString(event.IssueID)
	event.SessionID = copyOptionalString(event.SessionID)
	event.AttemptID = copyOptionalString(event.AttemptID)
	event.Payload = append(json.RawMessage(nil), event.Payload...)
	return event
}

// GetIssueActivityInput requests one cursor-paginated issue activity page.
type GetIssueActivityInput struct {
	IssueID string
	Types   []ActivityCategory
	Limit   int
	Cursor  string
	Order   ActivityOrder
}

// Validate validates and defensively copies an activity request.
func (input GetIssueActivityInput) Validate() (GetIssueActivityInput, error) {
	identifier, err := ParseIssueIdentifier(input.IssueID)
	if err != nil {
		return GetIssueActivityInput{}, err
	}
	if input.Limit < 0 || input.Limit > 100 {
		return GetIssueActivityInput{}, validationError("limit", "OUT_OF_RANGE", "must be 0 (default) or between 1 and 100")
	}

	types := append([]ActivityCategory(nil), input.Types...)
	if len(types) == 0 {
		types = append([]ActivityCategory(nil), AllActivityCategories...)
	} else {
		seen := make(map[ActivityCategory]struct{}, len(types))
		for _, value := range types {
			if !value.Valid() {
				return GetIssueActivityInput{}, invalidEnum("types", string(value))
			}
			if _, exists := seen[value]; exists {
				return GetIssueActivityInput{}, validationError("types", "DUPLICATE", "must not contain duplicate values")
			}
			seen[value] = struct{}{}
		}
	}

	order := input.Order
	if order == "" {
		order = ActivityOrderNewestFirst
	} else if !order.Valid() {
		return GetIssueActivityInput{}, validationError("order", "UNSUPPORTED", "only newest_first is supported")
	}
	if input.Cursor != "" {
		if err := ValidateText("cursor", input.Cursor, 4096); err != nil {
			return GetIssueActivityInput{}, err
		}
	}
	limit := input.Limit
	if limit == 0 {
		limit = 20
	}
	return GetIssueActivityInput{
		IssueID: identifier.Value,
		Types:   types,
		Limit:   limit,
		Cursor:  input.Cursor,
		Order:   order,
	}, nil
}

// ActivityItem is one typed, heterogeneous activity result.
type ActivityItem struct {
	EntityType  ActivityEntityType `json:"entity_type"`
	EntityID    string             `json:"entity_id"`
	IssueID     string             `json:"issue_id"`
	OccurredAt  time.Time          `json:"occurred_at"`
	Comment     *Comment           `json:"comment,omitempty"`
	Decision    *Decision          `json:"decision,omitempty"`
	Attempt     *WorkAttempt       `json:"attempt,omitempty"`
	AttemptNote *AttemptNote       `json:"attempt_note,omitempty"`
	Event       *IssueEvent        `json:"event,omitempty"`
	Artifact    *Artifact          `json:"artifact,omitempty"`
}

// IssueActivity is one cursor-paginated activity result.
type IssueActivity struct {
	Items      []ActivityItem
	NextCursor *string
	HasMore    bool
}

// ValidateActivityItem validates the discriminated payload and identity, scope,
// and occurrence invariants for adapter-produced activity data.
func ValidateActivityItem(item ActivityItem) error {
	if !item.EntityType.Valid() {
		return validationError("entity_type", "INVALID_ENUM", "is invalid")
	}
	if item.IssueID == "" {
		return validationError("issue_id", "INVALID_ULID", "must be a canonical ULID")
	}
	if _, err := ids.ParseStrict(item.IssueID); err != nil {
		return validationError("issue_id", "INVALID_ULID", "must be a canonical ULID")
	}
	if item.OccurredAt.IsZero() || item.OccurredAt.Location() != time.UTC {
		return validationError("occurred_at", "INVALID_TIMESTAMP", "must be a nonzero UTC timestamp")
	}

	payloads := 0
	if item.Comment != nil {
		payloads++
	}
	if item.Decision != nil {
		payloads++
	}
	if item.Attempt != nil {
		payloads++
	}
	if item.AttemptNote != nil {
		payloads++
	}
	if item.Event != nil {
		payloads++
	}
	if item.Artifact != nil {
		payloads++
	}
	if payloads != 1 {
		return validationError("payload", "INVALID_SHAPE", "must contain exactly one activity entity")
	}

	switch item.EntityType {
	case ActivityEntityTypeComment:
		if item.Comment == nil {
			return validationError("comment", "REQUIRED", "is required for comment activity")
		}
		if err := validateActivityULID("entity_id", item.EntityID); err != nil {
			return err
		}
		if item.Comment.ID != item.EntityID || item.Comment.IssueID != item.IssueID ||
			!item.Comment.CreatedAt.Equal(item.OccurredAt) {
			return validationError("comment", "MISMATCH", "does not match activity identity, scope, or timestamp")
		}
	case ActivityEntityTypeDecision:
		if item.Decision == nil {
			return validationError("decision", "REQUIRED", "is required for decision activity")
		}
		if err := validateActivityULID("entity_id", item.EntityID); err != nil {
			return err
		}
		if item.Decision.ID != item.EntityID || item.Decision.IssueID == nil ||
			*item.Decision.IssueID != item.IssueID ||
			!item.Decision.CreatedAt.Equal(item.OccurredAt) {
			return validationError("decision", "MISMATCH", "does not match activity identity, scope, or timestamp")
		}
	case ActivityEntityTypeAttempt:
		if item.Attempt == nil {
			return validationError("attempt", "REQUIRED", "is required for attempt activity")
		}
		if err := validateActivityULID("entity_id", item.EntityID); err != nil {
			return err
		}
		if item.Attempt.ID != item.EntityID || item.Attempt.IssueID != item.IssueID ||
			!item.Attempt.StartedAt.Equal(item.OccurredAt) {
			return validationError("attempt", "MISMATCH", "does not match activity identity, scope, or timestamp")
		}
	case ActivityEntityTypeAttemptNote:
		if item.AttemptNote == nil {
			return validationError("attempt_note", "REQUIRED", "is required for attempt_note activity")
		}
		if err := validateActivityULID("entity_id", item.EntityID); err != nil {
			return err
		}
		if item.AttemptNote.ID != item.EntityID ||
			!item.AttemptNote.CreatedAt.Equal(item.OccurredAt) {
			return validationError("attempt_note", "MISMATCH", "does not match activity identity or timestamp")
		}
	case ActivityEntityTypeEvent:
		if item.Event == nil {
			return validationError("event", "REQUIRED", "is required for event activity")
		}
		if err := validateActivityEventID(item.EntityID); err != nil {
			return err
		}
		if item.Event.ID <= 0 || strconv.FormatInt(item.Event.ID, 10) != item.EntityID ||
			item.Event.IssueID == nil || *item.Event.IssueID != item.IssueID ||
			!item.Event.CreatedAt.Equal(item.OccurredAt) {
			return validationError("event", "MISMATCH", "does not match activity identity, scope, or timestamp")
		}
	case ActivityEntityTypeArtifact:
		if item.Artifact == nil {
			return validationError("artifact", "REQUIRED", "is required for artifact activity")
		}
		if err := validateActivityULID("entity_id", item.EntityID); err != nil {
			return err
		}
		if item.Artifact.ID != item.EntityID || item.Artifact.IssueID != item.IssueID ||
			!item.Artifact.CreatedAt.Equal(item.OccurredAt) {
			return validationError("artifact", "MISMATCH", "does not match activity identity, scope, or timestamp")
		}
	}
	return nil
}

func validateActivityULID(field, value string) error {
	if _, err := ids.ParseStrict(value); err != nil {
		return validationError(field, "INVALID_ULID", "must be a canonical ULID")
	}
	return nil
}

func validateActivityEventID(value string) error {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
		return validationError("entity_id", "INVALID_EVENT_ID", "must be a positive decimal integer")
	}
	return nil
}

// CloneActivityItem returns an activity item without shared nested mutable data.
func CloneActivityItem(item ActivityItem) ActivityItem {
	item.Comment = cloneActivityComment(item.Comment)
	item.Decision = cloneActivityDecision(item.Decision)
	item.Attempt = cloneActivityAttempt(item.Attempt)
	item.AttemptNote = cloneActivityNote(item.AttemptNote)
	item.Event = cloneActivityEvent(item.Event)
	item.Artifact = cloneActivityArtifact(item.Artifact)
	return item
}

func cloneActivityComment(value *Comment) *Comment {
	if value == nil {
		return nil
	}
	result := CloneComment(*value)
	return &result
}

func cloneActivityDecision(value *Decision) *Decision {
	if value == nil {
		return nil
	}
	result := CloneDecision(*value)
	return &result
}

func cloneActivityAttempt(value *WorkAttempt) *WorkAttempt {
	if value == nil {
		return nil
	}
	result := *value
	result.SessionID = copyOptionalString(value.SessionID)
	result.AgentLabel = copyOptionalString(value.AgentLabel)
	result.FinishedAt = cloneActivityTime(value.FinishedAt)
	result.ResultSummary = copyOptionalString(value.ResultSummary)
	result.NextSteps = append([]string(nil), value.NextSteps...)
	result.Verification = append([]string(nil), value.Verification...)
	result.FailureReasonCode = cloneActivityFailure(value.FailureReasonCode)
	result.InterruptionReasonCode = cloneActivityInterruption(value.InterruptionReasonCode)
	result.ReasonDetails = copyOptionalString(value.ReasonDetails)
	return &result
}

func cloneActivityFailure(value *FailureReasonCode) *FailureReasonCode {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneActivityInterruption(value *InterruptionReasonCode) *InterruptionReasonCode {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneActivityTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneActivityNote(value *AttemptNote) *AttemptNote {
	if value == nil {
		return nil
	}
	result := *value
	result.NextSteps = append([]string(nil), value.NextSteps...)
	return &result
}

func cloneActivityEvent(value *IssueEvent) *IssueEvent {
	if value == nil {
		return nil
	}
	result := CloneIssueEvent(*value)
	return &result
}

func cloneActivityArtifact(value *Artifact) *Artifact {
	if value == nil {
		return nil
	}
	result := CloneArtifact(*value)
	return &result
}

// CloneIssueActivity returns a page with no shared nested mutable data.
// Its item slice is always non-nil, including when the source is empty.
func CloneIssueActivity(activity IssueActivity) IssueActivity {
	items := activity.Items
	activity.Items = make([]ActivityItem, len(items))
	for index, item := range items {
		activity.Items[index] = CloneActivityItem(item)
	}
	activity.NextCursor = copyOptionalString(activity.NextCursor)
	return activity
}
