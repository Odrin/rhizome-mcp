package application

import (
	"context"

	"rhizome-mcp/internal/domain"
)

type issueGetter interface {
	GetIssue(context.Context, string) (domain.Issue, error)
}

type graphGetter interface {
	GetIssueGraph(context.Context, domain.GetIssueGraphInput) (domain.GraphResult, error)
}

type activityGetter interface {
	GetIssueActivity(context.Context, domain.GetIssueActivityInput) (domain.IssueActivity, error)
}

type issueDetailReservationLister interface {
	ListReservations(context.Context, domain.ListResourceReservationsInput) (domain.ReservationList, error)
}

// IssueDetailService composes the bounded issue-detail use case from the
// existing issue, graph, activity, and reservation services.
type IssueDetailService struct {
	issueService       issueGetter
	graphService       graphGetter
	activityService    activityGetter
	reservationService issueDetailReservationLister
}

// NewIssueDetailService composes issue detail reads from the existing bounded services.
func NewIssueDetailService(issueService issueGetter, graphService graphGetter, activityService activityGetter, reservationService issueDetailReservationLister) (*IssueDetailService, error) {
	if issueService == nil || graphService == nil || activityService == nil || reservationService == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "issue detail dependencies are required", false)
	}
	return &IssueDetailService{issueService: issueService, graphService: graphService, activityService: activityService, reservationService: reservationService}, nil
}

// GetIssueDetail returns a bounded detail projection for one issue.
func (service *IssueDetailService) GetIssueDetail(ctx context.Context, identifier string) (domain.IssueDetail, error) {
	issue, err := service.issueService.GetIssue(ctx, identifier)
	if err != nil {
		return domain.IssueDetail{}, err
	}

	graph, err := service.graphService.GetIssueGraph(ctx, domain.GetIssueGraphInput{RootIssueID: issue.ID, Depth: nil, Direction: domain.GraphDirectionBoth, RelationTypes: nil, IncludeHierarchy: nil, IncludeTerminal: nil, MaxNodes: nil, View: "compact"})
	if err != nil {
		return domain.IssueDetail{}, err
	}

	activity, err := service.activityService.GetIssueActivity(ctx, domain.GetIssueActivityInput{IssueID: issue.ID, Limit: 20, Order: domain.ActivityOrderNewestFirst})
	if err != nil {
		return domain.IssueDetail{}, err
	}

	var rootProjection *domain.IssueProjection
	if graph.RootIssueID != nil {
		for _, node := range graph.Nodes {
			if node.ID == *graph.RootIssueID {
				projection := node
				rootProjection = &projection
				break
			}
		}
	}
	if rootProjection == nil {
		effectiveStatus, err := domain.EffectiveStatusFor(issue.Status, false)
		if err != nil {
			return domain.IssueDetail{}, err
		}
		rootProjection = &domain.IssueProjection{Issue: issue, EffectiveStatus: effectiveStatus}
	}

	reservationPage, err := service.reservationService.ListReservations(ctx, domain.ListResourceReservationsInput{
		IssueID: &issue.ID,
		Limit:   domain.DefaultReservationHistoryLimit,
	})
	if err != nil {
		return domain.IssueDetail{}, err
	}

	var latestAttempt *domain.WorkAttempt
	var openReview *domain.ReviewRequest
	var latestDecision *domain.Decision
	for _, item := range activity.Items {
		switch {
		case item.Attempt != nil && latestAttempt == nil:
			latestAttempt = item.Attempt
		case item.Review != nil && openReview == nil && item.Review.Status == domain.ReviewRequestStatusOpen:
			openReview = item.Review
		case item.Decision != nil && latestDecision == nil:
			latestDecision = item.Decision
		}
	}

	return domain.IssueDetail{
		Issue:               issue,
		RootIssueProjection: rootProjection,
		Graph:               graph,
		Activity:            activity,
		LatestAttempt:       latestAttempt,
		OpenReview:          openReview,
		LatestDecision:      latestDecision,
		Reservations:        domain.SummarizeReservations(reservationPage.Items),
		HasMoreReservations: reservationPage.HasMore,
	}, nil
}
