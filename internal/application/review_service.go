// Package application contains use cases composed from domain rules and ports.
package application

import (
	"context"
	"crypto/sha256"
	"strconv"
	"strings"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

// ReviewService manages review-request lifecycle transitions for MCP tools.
type ReviewService struct {
	repository      ports.ReviewRepository
	issueRepository ports.IssueRepository
	clock           clock.Clock
	ids             IDGenerator
}

// CreateReviewRequestInput captures the review-request creation intent.
type CreateReviewRequestInput struct {
	IssueID            string
	TargetIssueVersion int64
	TargetEventID      int64
	ArtifactIDs        []string
	// Purposes is optional: empty defaults to domain.DefaultReviewPurposes()
	// (docs/02 §17.5's compatibility default). A non-empty list is validated
	// and normalized by domain.ValidateReviewPurposes.
	Purposes     []string
	SupersedesID *string
}

// CreateReviewRequestResult contains the persisted review request and claimability state.
type CreateReviewRequestResult struct {
	Request   domain.ReviewRequest
	Claimable bool
}

// GetReviewRequestResult contains one review request and its claimability state.
type GetReviewRequestResult struct {
	Request   domain.ReviewRequest
	Claimable bool
}

// ListReviewRequestsInput carries status and pagination for review request listings.
type ListReviewRequestsInput struct {
	Status    *string
	Claimable *bool
	Limit     int
	Cursor    *string
}

// Validate applies the shared collection page-size policy: a zero limit takes
// the default and anything above the maximum is rejected rather than silently
// clamped, so a caller asking for more than it can have is told so instead of
// quietly receiving a short page.
func (input ListReviewRequestsInput) Validate() (ListReviewRequestsInput, error) {
	if input.Limit < 0 || input.Limit > domain.MaxCollectionLimit {
		return ListReviewRequestsInput{}, domain.NewError(domain.CodeInvalidArgument,
			"limit must be 0 (default) or between 1 and 100", false,
			domain.Detail{Field: "limit", Code: "OUT_OF_RANGE", Message: "must be 0 (default) or between 1 and 100"})
	}
	limit := input.Limit
	if limit == 0 {
		limit = domain.DefaultIssueListLimit
	}
	return ListReviewRequestsInput{Status: input.Status, Claimable: input.Claimable, Limit: limit, Cursor: input.Cursor}, nil
}

// ReviewRequestListItem is one review request entry together with its derived claimability.
type ReviewRequestListItem struct {
	Request   domain.ReviewRequest
	Claimable bool
}

// ListReviewRequestsResult is a paginated review request page.
type ListReviewRequestsResult struct {
	Items      []ReviewRequestListItem
	NextCursor *string
	HasMore    bool
}

// ReviewMutationInput carries the optimistic version precondition for mutations.
type ReviewMutationInput struct {
	RequestID       string
	ExpectedVersion int64
}

// ReviewMutationResult contains the updated request and claimability state.
type ReviewMutationResult struct {
	Request   domain.ReviewRequest
	Claimable bool
}

// NewReviewService composes the review use case from the required repositories.
func NewReviewService(repository ports.ReviewRepository, issueRepository ports.IssueRepository, source clock.Clock, generator IDGenerator) (*ReviewService, error) {
	if repository == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "review repository is required", false)
	}
	if issueRepository == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "issue repository is required", false)
	}
	if source == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "review clock is required", false)
	}
	if generator == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "review generator is required", false)
	}
	return &ReviewService{repository: repository, issueRepository: issueRepository, clock: source, ids: generator}, nil
}

// CreateReviewRequest validates a request, resolves the issue identifier, and persists the request.
func (service *ReviewService) CreateReviewRequest(ctx context.Context, input CreateReviewRequestInput) (CreateReviewRequestResult, error) {
	if strings.TrimSpace(input.IssueID) == "" {
		return CreateReviewRequestResult{}, domain.NewError(domain.CodeInvalidArgument, "issue_id is required", false)
	}
	if input.TargetIssueVersion < 1 {
		return CreateReviewRequestResult{}, domain.NewError(domain.CodeInvalidArgument, "target_issue_version must be >= 1", false)
	}
	if input.TargetEventID < 0 {
		return CreateReviewRequestResult{}, domain.NewError(domain.CodeInvalidArgument, "target_event_id must be >= 0", false)
	}
	if len(input.ArtifactIDs) > 20 {
		return CreateReviewRequestResult{}, domain.NewError(domain.CodeLimitExceeded, "artifact_ids exceeds the maximum size of 20", false)
	}
	purposes := input.Purposes
	if len(purposes) == 0 {
		purposes = domain.DefaultReviewPurposes()
	} else {
		var err error
		purposes, err = domain.ValidateReviewPurposes(purposes)
		if err != nil {
			return CreateReviewRequestResult{}, err
		}
	}
	identifier, err := domain.ParseIssueIdentifier(input.IssueID)
	if err != nil {
		return CreateReviewRequestResult{}, err
	}
	issueID := identifier.Value
	if identifier.Kind == domain.IssueIdentifierDisplayID {
		issue, err := service.issueRepository.GetIssue(ctx, identifier)
		if err != nil {
			return CreateReviewRequestResult{}, err
		}
		issueID = issue.ID
	}
	requestID, err := service.ids.New()
	if err != nil {
		return CreateReviewRequestResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate review request identifier", false)
	}
	targetID, err := service.ids.New()
	if err != nil {
		return CreateReviewRequestResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate review target identifier", false)
	}
	result, err := service.repository.CreateReviewRequest(ctx, ports.CreateReviewRequestCommand{
		RequestID:          requestID,
		TargetID:           targetID,
		IssueID:            issueID,
		TargetIssueVersion: input.TargetIssueVersion,
		TargetEventID:      input.TargetEventID,
		ArtifactIDs:        append([]string(nil), input.ArtifactIDs...),
		Purposes:           purposes,
		SupersedesID:       copyOptionalString(input.SupersedesID),
		OccurredAt:         service.clock.Now().UTC(),
	})
	if err != nil {
		return CreateReviewRequestResult{}, err
	}
	return CreateReviewRequestResult{Request: result.Request, Claimable: result.Request.Status == domain.ReviewRequestStatusOpen}, nil
}

// GetReviewRequest returns a single review request with derived claimability.
func (service *ReviewService) GetReviewRequest(ctx context.Context, requestID string) (GetReviewRequestResult, error) {
	if strings.TrimSpace(requestID) == "" {
		return GetReviewRequestResult{}, domain.NewError(domain.CodeInvalidArgument, "review_request_id is required", false)
	}
	result, err := service.repository.GetReviewRequest(ctx, requestID)
	if err != nil {
		return GetReviewRequestResult{}, err
	}
	return GetReviewRequestResult{Request: result.Request, Claimable: reviewRequestClaimable(result.Request.Status, result.TargetStale)}, nil
}

// ListReviewRequests returns a deterministic page of review requests with claimability.
func (service *ReviewService) ListReviewRequests(ctx context.Context, input ListReviewRequestsInput) (ListReviewRequestsResult, error) {
	normalized, err := input.Validate()
	if err != nil {
		return ListReviewRequestsResult{}, err
	}
	input = normalized
	limit := input.Limit
	offset := 0
	if input.Cursor != nil {
		cursorValue := strings.TrimSpace(*input.Cursor)
		if cursorValue != "" {
			parsed, err := strconv.Atoi(cursorValue)
			if err != nil || parsed < 0 {
				return ListReviewRequestsResult{}, domain.NewError(domain.CodeInvalidArgument, "cursor must be a non-negative integer", false)
			}
			offset = parsed
		}
	}
	var status *domain.ReviewRequestStatus
	if input.Status != nil {
		parsed, err := domain.ParseReviewRequestStatus(strings.TrimSpace(*input.Status))
		if err != nil {
			return ListReviewRequestsResult{}, err
		}
		status = &parsed
	}
	result, err := service.repository.ListReviewRequests(ctx, ports.ListReviewRequestsQuery{Status: status, Limit: limit, Offset: offset})
	if err != nil {
		return ListReviewRequestsResult{}, err
	}
	items := make([]ReviewRequestListItem, 0, len(result.Items))
	for _, request := range result.Items {
		claimable := reviewRequestClaimable(request.Status, result.StaleTargets[request.ID])
		if input.Claimable != nil && claimable != *input.Claimable {
			continue
		}
		items = append(items, ReviewRequestListItem{Request: request, Claimable: claimable})
	}
	nextCursor := (*string)(nil)
	if result.HasMore {
		value := strconv.Itoa(result.NextOffset)
		nextCursor = &value
	}
	return ListReviewRequestsResult{Items: items, NextCursor: nextCursor, HasMore: result.HasMore}, nil
}

// reviewRequestClaimable is the single derivation of review claimability
// (docs/09): a request is claimable while it is open and its frozen target
// still matches the issue. A stale target is reported as not claimable so a
// reviewer is not sent to work that can only end in STALE_REVIEW_TARGET
// (ISSUE-188).
func reviewRequestClaimable(status domain.ReviewRequestStatus, targetStale bool) bool {
	return status == domain.ReviewRequestStatusOpen && !targetStale
}

// CancelReviewRequest transitions an open or claimed request to cancelled.
func (service *ReviewService) CancelReviewRequest(ctx context.Context, input ReviewMutationInput) (ReviewMutationResult, error) {
	if strings.TrimSpace(input.RequestID) == "" {
		return ReviewMutationResult{}, domain.NewError(domain.CodeInvalidArgument, "review_request_id is required", false)
	}
	if input.ExpectedVersion < 1 {
		return ReviewMutationResult{}, domain.NewError(domain.CodeInvalidArgument, "expected_version must be >= 1", false)
	}
	result, err := service.repository.CancelReviewRequest(ctx, ports.ReviewMutationCommand{RequestID: input.RequestID, ExpectedVersion: input.ExpectedVersion, OccurredAt: service.clock.Now().UTC()})
	if err != nil {
		return ReviewMutationResult{}, err
	}
	return ReviewMutationResult{Request: result.Request, Claimable: false}, nil
}

// ReplaceReviewRequestInput captures the atomic replacement intent: close
// predecessor, open successor, in one transaction.
type ReplaceReviewRequestInput struct {
	PredecessorRequestID       string
	PredecessorExpectedVersion int64
	TargetIssueVersion         int64
	TargetEventID              int64
	ArtifactIDs                []string
	// Purposes is optional: empty means "inherit the predecessor's
	// purposes", resolved by the repository. A non-empty list is validated
	// and normalized by domain.ReplaceReviewRequestInput.Validate.
	Purposes       []string
	IdempotencyKey string
}

// ReplaceReviewRequestResult contains the persisted predecessor and successor
// requests plus the project-wide latest event ID observed in the same
// transaction, so the caller has enough position information to continue.
type ReplaceReviewRequestResult struct {
	Predecessor   domain.ReviewRequest
	Successor     domain.ReviewRequest
	LatestEventID int64
}

// ReplaceReviewRequest validates the request, replays a matching prior
// result for a reused idempotency key, and otherwise delegates one atomic
// supersede-and-create transaction to storage.
func (service *ReviewService) ReplaceReviewRequest(ctx context.Context, input ReplaceReviewRequestInput) (ReplaceReviewRequestResult, error) {
	normalized, err := domain.ReplaceReviewRequestInput{
		PredecessorRequestID:       input.PredecessorRequestID,
		PredecessorExpectedVersion: input.PredecessorExpectedVersion,
		TargetIssueVersion:         input.TargetIssueVersion,
		TargetEventID:              input.TargetEventID,
		ArtifactIDs:                input.ArtifactIDs,
		Purposes:                   input.Purposes,
		IdempotencyKey:             input.IdempotencyKey,
	}.Validate()
	if err != nil {
		return ReplaceReviewRequestResult{}, err
	}

	canonical, err := domain.CanonicalReplaceReviewRequestRequest(normalized)
	if err != nil {
		return ReplaceReviewRequestResult{}, domain.WrapError(err, domain.CodeStorageFailure, "cannot encode replace review request", false)
	}
	hash := sha256.Sum256(canonical)
	requestHash := hash[:]

	if replay, found, err := service.repository.LookupReplaceReviewRequest(ctx, normalized.IdempotencyKey, requestHash); err != nil {
		return ReplaceReviewRequestResult{}, err
	} else if found {
		return ReplaceReviewRequestResult{Predecessor: replay.Predecessor, Successor: replay.Successor, LatestEventID: replay.LatestEventID}, nil
	}

	successorID, err := service.ids.New()
	if err != nil {
		return ReplaceReviewRequestResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate successor request identifier", false)
	}
	successorTargetID, err := service.ids.New()
	if err != nil {
		return ReplaceReviewRequestResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate successor target identifier", false)
	}
	result, err := service.repository.ReplaceReviewRequest(ctx, ports.ReplaceReviewRequestCommand{
		PredecessorRequestID:       normalized.PredecessorRequestID,
		PredecessorExpectedVersion: normalized.PredecessorExpectedVersion,
		SuccessorID:                successorID,
		SuccessorTargetID:          successorTargetID,
		TargetIssueVersion:         normalized.TargetIssueVersion,
		TargetEventID:              normalized.TargetEventID,
		ArtifactIDs:                normalized.ArtifactIDs,
		Purposes:                   normalized.Purposes,
		OccurredAt:                 service.clock.Now().UTC(),
		IdempotencyKey:             normalized.IdempotencyKey,
		RequestHash:                requestHash,
	})
	if err != nil {
		return ReplaceReviewRequestResult{}, err
	}
	return ReplaceReviewRequestResult{Predecessor: result.Predecessor, Successor: result.Successor, LatestEventID: result.LatestEventID}, nil
}

func copyOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}
