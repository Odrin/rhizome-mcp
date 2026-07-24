package domain

import (
	"encoding/json"
	"fmt"
	"strings"
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

// MaxReviewArtifactIDs bounds the artifact set carried by one review request
// or replacement, matching the existing create_review_request bound.
const MaxReviewArtifactIDs = 20

// ReplaceReviewRequestInput is a caller-owned request to atomically supersede
// a predecessor review request and create its open successor in one
// transaction. The predecessor determines the issue scope: there is no
// separate issue_id, since the successor always belongs to the predecessor's
// issue. idempotency_key is mandatory (not optional) because a bare repeat
// would otherwise create a second successor from the same predecessor.
type ReplaceReviewRequestInput struct {
	PredecessorRequestID       string
	PredecessorExpectedVersion int64
	TargetIssueVersion         int64
	TargetEventID              int64
	ArtifactIDs                []string
	IdempotencyKey             string
}

// Validate checks request-local replacement rules and returns a normalized
// copy. It cannot check predecessor existence, status, or target
// consistency: those require storage and are enforced atomically by the
// repository.
func (input ReplaceReviewRequestInput) Validate() (ReplaceReviewRequestInput, error) {
	requestID := strings.TrimSpace(input.PredecessorRequestID)
	if requestID == "" {
		return ReplaceReviewRequestInput{}, validationError("predecessor_request_id", "REQUIRED", "must not be blank")
	}
	if input.PredecessorExpectedVersion < 1 {
		return ReplaceReviewRequestInput{}, validationError("predecessor_expected_version", "INVALID", "must be >= 1")
	}
	if input.TargetIssueVersion < 1 {
		return ReplaceReviewRequestInput{}, validationError("target_issue_version", "INVALID", "must be >= 1")
	}
	if input.TargetEventID < 0 {
		return ReplaceReviewRequestInput{}, validationError("target_event_id", "INVALID", "must be >= 0")
	}
	artifactIDs, err := CopyBounded("artifact_ids", input.ArtifactIDs, MaxReviewArtifactIDs)
	if err != nil {
		return ReplaceReviewRequestInput{}, err
	}
	key := strings.TrimSpace(input.IdempotencyKey)
	if key == "" {
		return ReplaceReviewRequestInput{}, validationError("idempotency_key", "REQUIRED", "must not be blank")
	}
	if err := ValidateText("idempotency_key", key, MaxIdempotencyKeyRunes); err != nil {
		return ReplaceReviewRequestInput{}, err
	}
	return ReplaceReviewRequestInput{
		PredecessorRequestID:       requestID,
		PredecessorExpectedVersion: input.PredecessorExpectedVersion,
		TargetIssueVersion:         input.TargetIssueVersion,
		TargetEventID:              input.TargetEventID,
		ArtifactIDs:                artifactIDs,
		IdempotencyKey:             key,
	}, nil
}

// CanonicalReplaceReviewRequestRequest returns deterministic JSON for a
// normalized replacement request. The idempotency key is intentionally
// excluded, matching every other Canonical*Request function in this package.
func CanonicalReplaceReviewRequestRequest(input ReplaceReviewRequestInput) ([]byte, error) {
	request := struct {
		PredecessorRequestID       string   `json:"predecessor_request_id"`
		PredecessorExpectedVersion int64    `json:"predecessor_expected_version"`
		TargetIssueVersion         int64    `json:"target_issue_version"`
		TargetEventID              int64    `json:"target_event_id"`
		ArtifactIDs                []string `json:"artifact_ids"`
	}{
		PredecessorRequestID:       input.PredecessorRequestID,
		PredecessorExpectedVersion: input.PredecessorExpectedVersion,
		TargetIssueVersion:         input.TargetIssueVersion,
		TargetEventID:              input.TargetEventID,
		ArtifactIDs:                input.ArtifactIDs,
	}
	return json.Marshal(request)
}
