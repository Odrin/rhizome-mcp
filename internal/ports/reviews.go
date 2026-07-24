// Package ports defines application-owned persistence boundaries.
package ports

import (
	"context"
	"time"

	"rhizome-mcp/internal/domain"
)

// CreateReviewRequestCommand captures a review request creation intent.
type CreateReviewRequestCommand struct {
	IssueID            string
	TargetIssueVersion int64
	TargetEventID      int64
	ArtifactIDs        []string
	SupersedesID       *string
	OccurredAt         time.Time
}

// CreateReviewRequestResult is the durable request and target snapshot produced by creation.
type CreateReviewRequestResult struct {
	Request domain.ReviewRequest
	Target  domain.ReviewTarget
}

// ReviewMutationCommand carries the expected version for a mutating review operation.
type ReviewMutationCommand struct {
	RequestID       string
	ExpectedVersion int64
	OccurredAt      time.Time
	ActiveAttemptID *string
	Outcome         *domain.ReviewOutcome
	Reason          *string
}

// ReviewMutationResult is the persisted request after a state transition.
type ReviewMutationResult struct {
	Request domain.ReviewRequest
	Target  domain.ReviewTarget
}

// GetReviewRequestResult is the persisted request and target snapshot for one review request.
type GetReviewRequestResult struct {
	Request domain.ReviewRequest
	Target  domain.ReviewTarget
}

// ListReviewRequestsQuery carries filtering and pagination for review requests.
type ListReviewRequestsQuery struct {
	Status *domain.ReviewRequestStatus
	Limit  int
	Offset int
}

// ListReviewRequestsResult is a deterministic page of review requests.
type ListReviewRequestsResult struct {
	Items      []domain.ReviewRequest
	HasMore    bool
	NextOffset int
}

// ResolveReviewRequestCommand carries the outcome for a reviewed request.
type ResolveReviewRequestCommand struct {
	RequestID       string
	ExpectedVersion int64
	OccurredAt      time.Time
	AttemptID       string
	Outcome         domain.ReviewOutcome
	Reason          *string
}

// ResolveReviewRequestResult is the persisted request, target, and outcome after resolution.
type ResolveReviewRequestResult struct {
	Request domain.ReviewRequest
	Target  domain.ReviewTarget
	Outcome domain.ReviewOutcomeRecord
}

// ReplaceReviewRequestCommand captures a validated atomic replacement: close
// the predecessor and open its successor in one transaction. IssueID is
// resolved from the predecessor by the repository, not supplied by the
// caller.
type ReplaceReviewRequestCommand struct {
	PredecessorRequestID       string
	PredecessorExpectedVersion int64
	TargetIssueVersion         int64
	TargetEventID              int64
	ArtifactIDs                []string
	OccurredAt                 time.Time
	IdempotencyKey             string
	RequestHash                []byte
}

// ReplaceReviewRequestResult is the persisted predecessor and successor
// requests, the successor's target snapshot, and the project-wide latest
// event ID observed in the same transaction.
type ReplaceReviewRequestResult struct {
	Predecessor     domain.ReviewRequest
	Successor       domain.ReviewRequest
	SuccessorTarget domain.ReviewTarget
	LatestEventID   int64
}

// ReviewRepository persists review workflow requests and their state transitions.
type ReviewRepository interface {
	CreateReviewRequest(context.Context, CreateReviewRequestCommand) (CreateReviewRequestResult, error)
	GetReviewRequest(context.Context, string) (GetReviewRequestResult, error)
	ListReviewRequests(context.Context, ListReviewRequestsQuery) (ListReviewRequestsResult, error)
	CancelReviewRequest(context.Context, ReviewMutationCommand) (ReviewMutationResult, error)
	SupersedeReviewRequest(context.Context, ReviewMutationCommand) (ReviewMutationResult, error)
	ClaimReviewRequest(context.Context, ReviewMutationCommand) (ReviewMutationResult, error)
	ResolveReviewRequest(context.Context, ResolveReviewRequestCommand) (ResolveReviewRequestResult, error)
	ReplaceReviewRequest(context.Context, ReplaceReviewRequestCommand) (ReplaceReviewRequestResult, error)
	LookupReplaceReviewRequest(context.Context, string, []byte) (ReplaceReviewRequestResult, bool, error)
}
