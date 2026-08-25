package application

import (
	"context"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
)

// issueGateSummaryGetter reads one issue's compact workflow-gate summary --
// the same projection get_work_context carries (satisfied by
// *WorkflowPolicyService).
type issueGateSummaryGetter interface {
	IssueGateSummary(context.Context, string) (domain.WorkContextGateSummary, error)
}

// BoardService composes the bounded, read-only project status board from
// existing issue, attempt, reservation, review, graph, and workflow-policy
// services. It introduces no new business rules; it only aggregates
// already-bounded projections for human-facing local status reporting (see
// the `board` CLI command).
type BoardService struct {
	issueService       *IssueService
	attemptService     *AttemptService
	reservationService *ReservationService
	reviewService      *ReviewService
	graphService       *GraphService
	gateService        issueGateSummaryGetter
	clock              clock.Clock
}

// NewBoardService composes the board use case from the services it aggregates.
func NewBoardService(issueService *IssueService, attemptService *AttemptService, reservationService *ReservationService, reviewService *ReviewService, graphService *GraphService, gateService issueGateSummaryGetter, source clock.Clock) (*BoardService, error) {
	if issueService == nil || attemptService == nil || reservationService == nil || reviewService == nil || graphService == nil || gateService == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "board dependencies are required", false)
	}
	if source == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "board clock is required", false)
	}
	return &BoardService{
		issueService: issueService, attemptService: attemptService, reservationService: reservationService,
		reviewService: reviewService, graphService: graphService, gateService: gateService, clock: source,
	}, nil
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

	activeAttemptList, err := service.attemptService.ListActiveAttempts(ctx, domain.MaxBoardCollectionLimit)
	if err != nil {
		return domain.BoardResult{}, err
	}
	activeAttempts := activeAttemptList.Items

	active := true
	reservationPage, err := service.reservationService.ListReservations(ctx, domain.ListResourceReservationsInput{
		Active: &active,
		Limit:  domain.MaxBoardCollectionLimit,
	})
	if err != nil {
		return domain.BoardResult{}, err
	}
	// Truncation.ActiveReservations is set from the pre-filter page's HasMore;
	// this is deliberate (per D2) to detect truncation before the filter runs.
	// A truncated reservation list may show fewer than 100 entries after
	// filtering, and orphaned rows awaiting sweep can push a live reservation
	// out of the window.
	activeReservations := filterReservationsByActiveAttempts(reservationPage.Items, activeAttempts)

	// One gate-progress row per active attempt (ISSUE-175 AC2): the gate the
	// attempt holder will actually hit, evaluated against the attempt's
	// frozen claim-time snapshot. Bounded by ActiveAttempts' own limit. Every
	// other issue's summary is one click away on its detail page.
	attemptGates := make([]domain.AttemptGateProgress, 0, len(activeAttempts))
	for _, attempt := range activeAttempts {
		summary, err := service.gateService.IssueGateSummary(ctx, attempt.IssueID)
		if err != nil {
			return domain.BoardResult{}, err
		}
		attemptGates = append(attemptGates, domain.AttemptGateProgress{
			AttemptID:      attempt.AttemptID,
			IssueID:        attempt.IssueID,
			IssueDisplayID: attempt.IssueDisplayID,
			Gates:          summary,
		})
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

	// The board answers "what can I work on", so finished work (done/cancelled)
	// must not consume the node budget. Request the graph without terminal nodes.
	includeTerminal := false
	planningGraph, err := service.graphService.GetPlanningGraph(ctx, domain.GetPlanningGraphInput{
		IncludeTerminal: &includeTerminal,
	})
	if err != nil {
		return domain.BoardResult{}, err
	}

	return domain.BoardResult{
		GeneratedAt:        service.clock.Now().UTC(),
		StatusCounts:       statusCounts,
		ActiveAttempts:     activeAttempts,
		AttemptGates:       attemptGates,
		ActiveReservations: activeReservations,
		BlockedIssues:      blockedPage.Items,
		ReviewRequests:     reviewRequests,
		PlanningGraph:      planningGraph,
		Truncation: domain.BoardTruncation{
			BlockedIssues:      blockedPage.HasMore,
			ActiveAttempts:     activeAttemptList.HasMore,
			ActiveReservations: reservationPage.HasMore,
			ReviewRequests:     reviewPage.HasMore,
		},
	}, nil
}

// filterReservationsByActiveAttempts drops any reservation whose owning
// attempt is not in activeAttempts. resource_reservations.status='active'
// alone is not sufficient: ListActiveAttempts additionally requires
// lease_expires_at > now, so an attempt whose lease has technically expired
// but has not yet been swept by ExpireAttempts still owns rows with status
// 'active'. Without this filter such a reservation would be an orphan on
// the board -- present in ActiveReservations with no matching
// ActiveAttempts row to attribute owner/session/lease-expiry to, which the
// HTML view happens to hide (it only renders reservations grouped under a
// known attempt) but the JSON API and CLI table would otherwise show
// as if it were still legitimately held.
func filterReservationsByActiveAttempts(reservations []domain.Reservation, activeAttempts []domain.ActiveAttemptSummary) []domain.Reservation {
	attemptIDs := make(map[string]struct{}, len(activeAttempts))
	for _, attempt := range activeAttempts {
		attemptIDs[attempt.AttemptID] = struct{}{}
	}
	filtered := make([]domain.Reservation, 0, len(reservations))
	for _, reservation := range reservations {
		if _, ok := attemptIDs[reservation.AttemptID]; ok {
			filtered = append(filtered, reservation)
		}
	}
	return filtered
}
