package domain

import (
	"encoding/json"
	"fmt"
	"sort"
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
	Purposes           []string
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
	Purposes      []string
	Version       int64
	CreatedAt     time.Time
}

// ReviewApproval is one immutable, purpose-scoped approval record (ISSUE-173,
// docs/02 §17.5): proof that an approved, non-stale review request granted
// requirement.Purpose for the issue at TargetIssueVersion. A request grants
// one approval per purpose it covers, written in the same transaction that
// resolves the request to approved; approvals are never updated or deleted.
type ReviewApproval struct {
	ID                 string
	IssueID            string
	TargetID           string
	RequestID          string
	AttemptID          string
	Purpose            string
	TargetIssueVersion int64
	TargetEventID      int64
	Version            int64
	CreatedAt          time.Time
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
// or replacement, enforced at every request creation and replacement path.
const MaxReviewArtifactIDs = 20

// MaxReviewPurposes bounds the purposes list on a review request or target,
// matching the CHECK constraint migration 012 adds to both tables.
const MaxReviewPurposes = 10

// DefaultReviewPurposes returns a fresh compatibility-default purposes list
// (docs/02 §17.5) for a caller that names no purpose at all. Returns a new
// slice on every call so callers can freely mutate or store the result.
func DefaultReviewPurposes() []string { return []string{"implementation"} }

// ValidateReviewPurposes trims, bounds, deduplicates, and sorts a
// caller-supplied purposes list. Normalization matches
// PolicyRequirement.Purpose's own (trim only, case preserved) so a review
// request's purposes compare equal to policy-declared requirement purposes
// by ordinary string equality -- this is deliberately not full label-style
// case-folding. An empty input is rejected rather than silently defaulted;
// callers wanting the compatibility default pass DefaultReviewPurposes()
// explicitly.
func ValidateReviewPurposes(purposes []string) ([]string, error) {
	if len(purposes) == 0 {
		return nil, validationError("purposes", "REQUIRED", "must include at least one purpose")
	}
	if len(purposes) > MaxReviewPurposes {
		return nil, NewError(CodeLimitExceeded, fmt.Sprintf("purposes exceeds the maximum count of %d", MaxReviewPurposes), false,
			Detail{Field: "purposes", Code: "MAX_ITEMS", Message: fmt.Sprintf("maximum %d", MaxReviewPurposes)})
	}
	normalized := make([]string, len(purposes))
	seen := make(map[string]bool, len(purposes))
	for index, purpose := range purposes {
		if err := ValidateText("purposes", purpose, MaxPolicyKeyRunes); err != nil {
			return nil, err
		}
		trimmed := strings.TrimSpace(purpose)
		if trimmed == "" {
			return nil, validationError("purposes", "REQUIRED", "must not contain a blank purpose")
		}
		if seen[trimmed] {
			return nil, NewError(CodeInvalidArgument, fmt.Sprintf("purpose %q is repeated", trimmed), false,
				Detail{Field: "purposes", Code: "DUPLICATE", Message: trimmed})
		}
		seen[trimmed] = true
		normalized[index] = trimmed
	}
	sort.Strings(normalized)
	return normalized, nil
}

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
	// Purposes is optional: nil or empty means "inherit the predecessor's
	// purposes", resolved by the repository once it has loaded the
	// predecessor (docs/02 §17.5 says nothing about a successor changing
	// scope, and the predecessor is the only source of that default this
	// request-shape validator has no access to). A non-empty list is
	// validated and normalized here like any other purposes list.
	Purposes       []string
	IdempotencyKey string
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
	var purposes []string
	if len(input.Purposes) > 0 {
		purposes, err = ValidateReviewPurposes(input.Purposes)
		if err != nil {
			return ReplaceReviewRequestInput{}, err
		}
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
		Purposes:                   purposes,
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
		Purposes                   []string `json:"purposes,omitempty"`
	}{
		PredecessorRequestID:       input.PredecessorRequestID,
		PredecessorExpectedVersion: input.PredecessorExpectedVersion,
		TargetIssueVersion:         input.TargetIssueVersion,
		TargetEventID:              input.TargetEventID,
		ArtifactIDs:                input.ArtifactIDs,
		Purposes:                   input.Purposes,
	}
	return json.Marshal(request)
}
