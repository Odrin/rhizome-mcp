package application

import (
	"context"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
)

// BoardService composes the bounded, read-only project status board from
// existing issue, attempt, review, and graph services. It introduces no new
// business rules; it only aggregates already-bounded projections for
// human-facing local status reporting (see the `board` CLI command).
type BoardService struct {
	issueService   *IssueService
	attemptService *AttemptService
	reviewService  *ReviewService
	graphService   *GraphService
	clock          clock.Clock
}

// NewBoardService composes the board use case from the services it aggregates.
func NewBoardService(issueService *IssueService, attemptService *AttemptService, reviewService *ReviewService, graphService *GraphService, source clock.Clock) (*BoardService, error) {
	if issueService == nil || attemptService == nil || reviewService == nil || graphService == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "board dependencies are required", false)
	}
	if source == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "board clock is required", false)
	}
	return &BoardService{issueService: issueService, attemptService: attemptService, reviewService: reviewService, graphService: graphService, clock: source}, nil
}

// GetBoard returns the current bounded status board: issue counts by
// effective status, currently leased attempts, blocked issues with their
// reasons, open review requests, and the project-wide planning graph.
func (service *BoardService) GetBoard(ctx context.Context) (domain.BoardResult, error) {
	statusCounts, err := service.issueService.CountIssuesByEffectiveStatus(ctx)
	if err != nil {
		return domain.BoardResult{}, err
	}

	blocked := true
	blockedPage, err := service.issueService.ListIssues(ctx, domain.ListIssuesInput{
		IsBlocked: &blocked,
		Limit:     domain.MaxBoardCollectionLimit,
	})
	if err != nil {
		return domain.BoardResult{}, err
	}

	activeAttempts, err := service.attemptService.ListActiveAttempts(ctx, domain.MaxBoardCollectionLimit)
	if err != nil {
		return domain.BoardResult{}, err
	}

	openStatus := string(domain.ReviewRequestStatusOpen)
	reviewPage, err := service.reviewService.ListReviewRequests(ctx, ListReviewRequestsInput{
		Status: &openStatus,
		Limit:  domain.MaxBoardCollectionLimit,
	})
	if err != nil {
		return domain.BoardResult{}, err
	}
	reviewRequests := make([]domain.ReviewRequest, len(reviewPage.Items))
	for index, item := range reviewPage.Items {
		reviewRequests[index] = item.Request
	}

	planningGraph, err := service.graphService.GetPlanningGraph(ctx, domain.GetPlanningGraphInput{})
	if err != nil {
		return domain.BoardResult{}, err
	}

	return domain.BoardResult{
		GeneratedAt:    service.clock.Now().UTC(),
		StatusCounts:   statusCounts,
		ActiveAttempts: activeAttempts,
		BlockedIssues:  blockedPage.Items,
		ReviewRequests: reviewRequests,
		PlanningGraph:  planningGraph,
	}, nil
}
