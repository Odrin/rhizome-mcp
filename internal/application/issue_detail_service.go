package application

import (
	"context"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
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
// existing issue, graph, activity, reservation, and workflow-policy services.
type IssueDetailService struct {
	issueService       issueGetter
	graphService       graphGetter
	activityService    activityGetter
	reservationService issueDetailReservationLister
	gateService        issueGateSummaryGetter
	snapshotLoader     ports.IssueDetailSnapshotLoader
	clock              clock.Clock
}

// NewIssueDetailService composes issue detail reads from the existing bounded services.
func NewIssueDetailService(issueService issueGetter, graphService graphGetter, activityService activityGetter, reservationService issueDetailReservationLister, gateService issueGateSummaryGetter) (*IssueDetailService, error) {
	if issueService == nil || graphService == nil || activityService == nil || reservationService == nil || gateService == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "issue detail dependencies are required", false)
	}
	return &IssueDetailService{
		issueService:       issueService,
		graphService:       graphService,
		activityService:    activityService,
		reservationService: reservationService,
		gateService:        gateService,
		clock:              clock.RealClock{},
	}, nil
}

// WithSnapshotLoader configures an optional single-snapshot loader so
// GetIssueDetail reads every section within one consistent snapshot.
func (service *IssueDetailService) WithSnapshotLoader(loader ports.IssueDetailSnapshotLoader) *IssueDetailService {
	if service != nil {
		service.snapshotLoader = loader
	}
	return service
}

// WithClock configures the clock passed to snapshot detail reads.
func (service *IssueDetailService) WithClock(source clock.Clock) *IssueDetailService {
	if service != nil && source != nil {
		service.clock = source
	}
	return service
}

// GetIssueDetail returns a bounded detail projection for one issue.
func (service *IssueDetailService) GetIssueDetail(ctx context.Context, identifier string) (domain.IssueDetail, error) {
	if service.snapshotLoader != nil {
		parsed, err := domain.ParseIssueIdentifier(identifier)
		if err != nil {
			return domain.IssueDetail{}, err
		}
		currentClock := service.clock
		if currentClock == nil {
			currentClock = clock.RealClock{}
		}
		return service.snapshotLoader.LoadIssueDetailSnapshot(ctx, ports.LoadIssueDetailSnapshotCommand{
			Identifier: parsed,
			Now:        currentClock.Now().UTC(),
		})
	}

	issue, err := service.issueService.GetIssue(ctx, identifier)
	if err != nil {
		return domain.IssueDetail{}, err
	}

	var graph domain.GraphResult
	if issue.ArchivedAt != nil {
		// Archived issues cannot root a graph traversal (GetIssueGraph returns
		// CodeIssueArchived), so skip the graph query and fall back to a
		// root-only projection from the issue record.
		graph = domain.GraphResult{
			RootIssueID: &issue.ID,
			Nodes:       []domain.IssueProjection{},
			Edges:       []domain.GraphEdge{},
		}
	} else {
		graph, err = service.graphService.GetIssueGraph(ctx, domain.GetIssueGraphInput{RootIssueID: issue.ID, Depth: nil, Direction: domain.GraphDirectionBoth, RelationTypes: nil, IncludeHierarchy: nil, IncludeTerminal: nil, MaxNodes: nil, View: "compact"})
		if err != nil {
			return domain.IssueDetail{}, err
		}
	}

	activity, err := service.activityService.GetIssueActivity(ctx, domain.GetIssueActivityInput{IssueID: issue.ID, Limit: 20, Order: domain.ActivityOrderNewestFirst})
	if err != nil {
		return domain.IssueDetail{}, err
	}

	reservationPage, err := service.reservationService.ListReservations(ctx, domain.ListResourceReservationsInput{
		IssueID: &issue.ID,
		Limit:   domain.DefaultReservationHistoryLimit,
	})
	if err != nil {
		return domain.IssueDetail{}, err
	}

	gates, err := service.gateService.IssueGateSummary(ctx, issue.ID)
	if err != nil {
		return domain.IssueDetail{}, err
	}

	return domain.AssembleIssueDetail(issue, graph, activity, reservationPage.Items, reservationPage.HasMore, gates)
}
