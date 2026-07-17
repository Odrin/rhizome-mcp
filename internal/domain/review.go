package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

// ReviewRequestStatus captures persisted review workflow state.
type ReviewRequestStatus string

const (
	ReviewRequestStatusOpen             ReviewRequestStatus = "open"
	ReviewRequestStatusClaimed          ReviewRequestStatus = "claimed"
	ReviewRequestStatusApproved         ReviewRequestStatus = "approved"
	ReviewRequestStatusChangesRequested ReviewRequestStatus = "changes_requested"
	ReviewRequestStatusBlocked          ReviewRequestStatus = "blocked"
	ReviewRequestStatusCancelled        ReviewRequestStatus = "cancelled"
	ReviewRequestStatusSuperseded       ReviewRequestStatus = "superseded"
)

// ParseReviewRequestStatus parses a supported review request status.
func ParseReviewRequestStatus(value string) (ReviewRequestStatus, error) {
	parsed := ReviewRequestStatus(value)
	if !parsed.Valid() {
		return "", invalidEnum("review_request_status", value)
	}
	return parsed, nil
}

// Valid reports whether s is a supported review request status.
func (s ReviewRequestStatus) Valid() bool {
	switch s {
	case ReviewRequestStatusOpen, ReviewRequestStatusClaimed, ReviewRequestStatusApproved,
		ReviewRequestStatusChangesRequested, ReviewRequestStatusBlocked,
		ReviewRequestStatusCancelled, ReviewRequestStatusSuperseded:
		return true
	default:
		return false
	}
}

// ReviewRequest is the durable projection for a review workflow request.
type ReviewRequest struct {
	ID                 string
	IssueID            string
	TargetID           string
	TargetIssueVersion int64
	TargetEventID      int64
	ArtifactIDs        []string
	Status             ReviewRequestStatus
	SupersedesID       *string
	ActiveAttemptID    *string
	Version            int64
	CreatedAt          time.Time
	ResolvedAt         *time.Time
}

// ReviewTarget is the immutable target snapshot for a review request.
type ReviewTarget struct {
	ID            string
	IssueID       string
	IssueVersion  int64
	LatestEventID int64
	ArtifactIDs   []string
	Version       int64
	CreatedAt     time.Time
}

// ReviewOutcomeRecord is the durable review resolution row for a request.
type ReviewOutcomeRecord struct {
	ID        string
	RequestID string
	AttemptID string
	Outcome   ReviewOutcome
	Reason    *string
	Version   int64
	CreatedAt time.Time
}

// ReviewEventType names the append-only review workflow event stream.
type ReviewEventType string

const (
	ReviewEventTypeRequested        ReviewEventType = "review_requested"
	ReviewEventTypeClaimed          ReviewEventType = "review_claimed"
	ReviewEventTypeApproved         ReviewEventType = "review_approved"
	ReviewEventTypeChangesRequested ReviewEventType = "review_changes_requested"
	ReviewEventTypeBlocked          ReviewEventType = "review_blocked"
	ReviewEventTypeCancelled        ReviewEventType = "review_cancelled"
	ReviewEventTypeSuperseded       ReviewEventType = "review_superseded"
)

// ParseReviewEventType parses a supported review workflow event type.
func ParseReviewEventType(value string) (ReviewEventType, error) {
	parsed := ReviewEventType(value)
	if !parsed.Valid() {
		return "", NewError(CodeInvalidArgument, fmt.Sprintf("unsupported review event type %q", value), false)
	}
	return parsed, nil
}

// Valid reports whether t is a supported review workflow event type.
func (t ReviewEventType) Valid() bool {
	switch t {
	case ReviewEventTypeRequested, ReviewEventTypeClaimed, ReviewEventTypeApproved,
		ReviewEventTypeChangesRequested, ReviewEventTypeBlocked,
		ReviewEventTypeCancelled, ReviewEventTypeSuperseded:
		return true
	default:
		return false
	}
}

// ReviewEvent is one append-only review workflow state transition.
type ReviewEvent struct {
	ID        int64
	RequestID string
	TargetID  string
	AttemptID *string
	EventType ReviewEventType
	Payload   json.RawMessage
	CreatedAt time.Time
}
