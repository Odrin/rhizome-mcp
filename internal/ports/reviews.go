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

// ReviewRepository persists review workflow requests and their state transitions.
type ReviewRepository interface {
	CreateReviewRequest(context.Context, CreateReviewRequestCommand) (CreateReviewRequestResult, error)
	CancelReviewRequest(context.Context, ReviewMutationCommand) (ReviewMutationResult, error)
	SupersedeReviewRequest(context.Context, ReviewMutationCommand) (ReviewMutationResult, error)
	ClaimReviewRequest(context.Context, ReviewMutationCommand) (ReviewMutationResult, error)
	ResolveReviewRequest(context.Context, ResolveReviewRequestCommand) (ResolveReviewRequestResult, error)
}
