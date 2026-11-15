package application

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

type recordingIssueService struct {
	calls int
	id    string
	issue domain.Issue
	err   error
}

func (s *recordingIssueService) GetIssue(ctx context.Context, identifier string) (domain.Issue, error) {
	s.calls++
	s.id = identifier
	return s.issue, s.err
}

type recordingGraphService struct {
	calls int
	input domain.GetIssueGraphInput
	graph domain.GraphResult
	err   error
}

func (s *recordingGraphService) GetIssueGraph(ctx context.Context, input domain.GetIssueGraphInput) (domain.GraphResult, error) {
	s.calls++
	s.input = input
	return s.graph, s.err
}

type recordingActivityService struct {
	calls    int
	input    domain.GetIssueActivityInput
	activity domain.IssueActivity
	err      error
}

func (s *recordingActivityService) GetIssueActivity(ctx context.Context, input domain.GetIssueActivityInput) (domain.IssueActivity, error) {
	s.calls++
	s.input = input
	return s.activity, s.err
}

type recordingReservationLister struct {
	calls int
	input domain.ListResourceReservationsInput
	list  domain.ReservationList
	err   error
}

func (s *recordingReservationLister) ListReservations(ctx context.Context, input domain.ListResourceReservationsInput) (domain.ReservationList, error) {
	s.calls++
	s.input = input
	return s.list, s.err
}

type stubGateSummaryService struct {
	calls   []string
	summary domain.WorkContextGateSummary
	err     error
}

func (s *stubGateSummaryService) IssueGateSummary(_ context.Context, issueID string) (domain.WorkContextGateSummary, error) {
	s.calls = append(s.calls, issueID)
	if s.err != nil {
		return domain.WorkContextGateSummary{}, s.err
	}
	return s.summary, nil
}

func TestIssueDetailServiceBuildsBoundedProjection(t *testing.T) {
	issue := domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", DisplayID: "ISSUE-10", Status: domain.StatusOpen, Priority: domain.PriorityHigh}
	graphRootID := issue.ID
	graph := domain.GraphResult{RootIssueID: &graphRootID, Nodes: []domain.IssueProjection{{Issue: issue, EffectiveStatus: domain.EffectiveStatusOpen}}}
	var now = time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	activity := domain.IssueActivity{
		HasMore: true,
		Items: []domain.ActivityItem{
			{EntityType: domain.ActivityEntityTypeComment, EntityID: "01ARZ3NDEKTSV4RRFFQ69G5FB1", IssueID: issue.ID, OccurredAt: now.Add(1 * time.Minute), Comment: &domain.Comment{ID: "01ARZ3NDEKTSV4RRFFQ69G5FB1", IssueID: issue.ID, CreatedAt: now.Add(1 * time.Minute)}},
			{EntityType: domain.ActivityEntityTypeAttempt, EntityID: "01ARZ3NDEKTSV4RRFFQ69G5FB2", IssueID: issue.ID, OccurredAt: now.Add(2 * time.Minute), Attempt: &domain.WorkAttempt{ID: "01ARZ3NDEKTSV4RRFFQ69G5FB2", IssueID: issue.ID, StartedAt: now.Add(2 * time.Minute)}},
			{EntityType: domain.ActivityEntityTypeReview, EntityID: "01ARZ3NDEKTSV4RRFFQ69G5FB3", IssueID: issue.ID, OccurredAt: now.Add(3 * time.Minute), Review: &domain.ReviewRequest{ID: "01ARZ3NDEKTSV4RRFFQ69G5FB3", IssueID: issue.ID, Status: domain.ReviewRequestStatusOpen, CreatedAt: now.Add(3 * time.Minute)}},
			{EntityType: domain.ActivityEntityTypeDecision, EntityID: "01ARZ3NDEKTSV4RRFFQ69G5FB4", IssueID: issue.ID, OccurredAt: now.Add(4 * time.Minute), Decision: &domain.Decision{ID: "01ARZ3NDEKTSV4RRFFQ69G5FB4", IssueID: &issue.ID, CreatedAt: now.Add(4 * time.Minute)}},
		},
	}
	issueService := &recordingIssueService{issue: issue}
	graphService := &recordingGraphService{graph: graph}
	activityService := &recordingActivityService{activity: activity}
	reservations := domain.ReservationList{Items: []domain.Reservation{{ID: "01ARZ3NDEKTSV4RRFFQ69G5FB5", IssueID: issue.ID, AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FB2", Kind: domain.ResourceKindFile, DisplayValue: "a.go", Status: domain.ReservationStatusActive}}, HasMore: true}
	reservationService := &recordingReservationLister{list: reservations}

	service, err := NewIssueDetailService(issueService, graphService, activityService, reservationService, &stubGateSummaryService{})
	if err != nil {
		t.Fatalf("NewIssueDetailService returned error: %v", err)
	}

	detail, err := service.GetIssueDetail(context.Background(), "issue-10")
	if err != nil {
		t.Fatalf("GetIssueDetail returned error: %v", err)
	}
	if issueService.calls != 1 {
		t.Fatalf("issue service calls = %d, want 1", issueService.calls)
	}
	if graphService.calls != 1 {
		t.Fatalf("graph service calls = %d, want 1", graphService.calls)
	}
	if activityService.calls != 1 {
		t.Fatalf("activity service calls = %d, want 1", activityService.calls)
	}
	if reservationService.calls != 1 {
		t.Fatalf("reservation service calls = %d, want 1", reservationService.calls)
	}
	if reservationService.input.IssueID == nil || *reservationService.input.IssueID != issue.ID {
		t.Fatalf("reservation issue id = %#v, want %q", reservationService.input.IssueID, issue.ID)
	}
	if reservationService.input.Active != nil {
		t.Fatalf("reservation active filter = %#v, want nil (both current and historical rows)", reservationService.input.Active)
	}
	if reservationService.input.Limit != domain.DefaultReservationHistoryLimit {
		t.Fatalf("reservation limit = %d, want %d", reservationService.input.Limit, domain.DefaultReservationHistoryLimit)
	}
	if len(detail.Reservations) != 1 || detail.Reservations[0].DisplayValue != "a.go" {
		t.Fatalf("detail reservations = %+v, want one a.go", detail.Reservations)
	}
	if !detail.HasMoreReservations {
		t.Fatal("expected HasMoreReservations to be retained from the reservation page")
	}
	if issueService.id != "issue-10" {
		t.Fatalf("issue service identifier = %q, want %q", issueService.id, "issue-10")
	}
	if graphService.input.RootIssueID != issue.ID {
		t.Fatalf("graph root issue id = %q, want %q", graphService.input.RootIssueID, issue.ID)
	}
	if graphService.input.Depth != nil || graphService.input.Direction != domain.GraphDirectionBoth || graphService.input.RelationTypes != nil || graphService.input.IncludeHierarchy != nil || graphService.input.IncludeTerminal != nil || graphService.input.MaxNodes != nil || graphService.input.View != "compact" {
		t.Fatalf("unexpected graph input: %#v", graphService.input)
	}
	if activityService.input.IssueID != issue.ID {
		t.Fatalf("activity issue id = %q, want %q", activityService.input.IssueID, issue.ID)
	}
	if activityService.input.Limit != 20 {
		t.Fatalf("activity limit = %d, want 20", activityService.input.Limit)
	}
	if activityService.input.Order != domain.ActivityOrderNewestFirst {
		t.Fatalf("activity order = %q, want %q", activityService.input.Order, domain.ActivityOrderNewestFirst)
	}
	if detail.Activity.HasMore != true {
		t.Fatal("expected activity HasMore to be retained")
	}
	if detail.LatestAttempt == nil || detail.LatestAttempt.ID != "01ARZ3NDEKTSV4RRFFQ69G5FB2" {
		t.Fatalf("latest attempt = %#v, want attempt id %q", detail.LatestAttempt, "01ARZ3NDEKTSV4RRFFQ69G5FB2")
	}
	if detail.OpenReview == nil || detail.OpenReview.ID != "01ARZ3NDEKTSV4RRFFQ69G5FB3" {
		t.Fatalf("open review = %#v, want review id %q", detail.OpenReview, "01ARZ3NDEKTSV4RRFFQ69G5FB3")
	}
	if detail.LatestDecision == nil || detail.LatestDecision.ID != "01ARZ3NDEKTSV4RRFFQ69G5FB4" {
		t.Fatalf("latest decision = %#v, want decision id %q", detail.LatestDecision, "01ARZ3NDEKTSV4RRFFQ69G5FB4")
	}
	if detail.RootIssueProjection == nil || detail.RootIssueProjection.ID != issue.ID {
		t.Fatalf("root issue projection = %#v, want root id %q", detail.RootIssueProjection, issue.ID)
	}
}

func TestIssueDetailServiceStopsOnErrors(t *testing.T) {
	tests := []struct {
		name                 string
		issueErr             error
		graphErr             error
		activityErr          error
		reservationErr       error
		wantErrText          string
		wantIssueCalls       int
		wantGraphCalls       int
		wantActivityCalls    int
		wantReservationCalls int
	}{
		{name: "issue error", issueErr: errors.New("issue failure"), wantErrText: "issue failure", wantIssueCalls: 1, wantGraphCalls: 0, wantActivityCalls: 0, wantReservationCalls: 0},
		{name: "graph error", graphErr: errors.New("graph failure"), wantErrText: "graph failure", wantIssueCalls: 1, wantGraphCalls: 1, wantActivityCalls: 0, wantReservationCalls: 0},
		{name: "activity error", activityErr: errors.New("activity failure"), wantErrText: "activity failure", wantIssueCalls: 1, wantGraphCalls: 1, wantActivityCalls: 1, wantReservationCalls: 0},
		{name: "reservation error", reservationErr: errors.New("reservation failure"), wantErrText: "reservation failure", wantIssueCalls: 1, wantGraphCalls: 1, wantActivityCalls: 1, wantReservationCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueService := &recordingIssueService{issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", DisplayID: "ISSUE-10", Status: domain.StatusOpen}, err: tt.issueErr}
			graphService := &recordingGraphService{graph: domain.GraphResult{RootIssueID: ptrString("01ARZ3NDEKTSV4RRFFQ69G5FAV")}, err: tt.graphErr}
			activityService := &recordingActivityService{activity: domain.IssueActivity{}, err: tt.activityErr}
			reservationService := &recordingReservationLister{err: tt.reservationErr}
			service, err := NewIssueDetailService(issueService, graphService, activityService, reservationService, &stubGateSummaryService{})
			if err != nil {
				t.Fatalf("NewIssueDetailService returned error: %v", err)
			}
			_, err = service.GetIssueDetail(context.Background(), "ISSUE-10")
			if err == nil || err.Error() != tt.wantErrText {
				t.Fatalf("GetIssueDetail error = %v, want %q", err, tt.wantErrText)
			}
			if issueService.calls != tt.wantIssueCalls {
				t.Fatalf("issue service calls = %d, want %d", issueService.calls, tt.wantIssueCalls)
			}
			if graphService.calls != tt.wantGraphCalls {
				t.Fatalf("graph service calls = %d, want %d", graphService.calls, tt.wantGraphCalls)
			}
			if activityService.calls != tt.wantActivityCalls {
				t.Fatalf("activity service calls = %d, want %d", activityService.calls, tt.wantActivityCalls)
			}
			if reservationService.calls != tt.wantReservationCalls {
				t.Fatalf("reservation service calls = %d, want %d", reservationService.calls, tt.wantReservationCalls)
			}
		})
	}
}

func ptrString(value string) *string {
	return &value
}

// TestIssueDetailServiceIncludesGateSummary pins ISSUE-175 AC2: the detail
// projection carries the same compact summary get_work_context reports,
// requested by the issue's resolved internal ID.
func TestIssueDetailServiceIncludesGateSummary(t *testing.T) {
	issue := domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", DisplayID: "ISSUE-10", Status: domain.StatusOpen}
	gateService := &stubGateSummaryService{summary: domain.WorkContextGateSummary{
		Point:            domain.EnforcementPointClaimWork,
		RequirementCount: 1,
		Unmet:            []domain.WorkContextUnmetRequirement{{PolicyID: "policy-1", RequirementKey: "acceptance_criteria", Reason: "issue field \"acceptance_criteria\" is blank"}},
		NextActions:      []string{"set a non-blank acceptance_criteria on the issue with update_issue"},
	}}
	service, err := NewIssueDetailService(&recordingIssueService{issue: issue}, &recordingGraphService{}, &recordingActivityService{}, &recordingReservationLister{}, gateService)
	if err != nil {
		t.Fatalf("NewIssueDetailService returned error: %v", err)
	}

	detail, err := service.GetIssueDetail(context.Background(), "ISSUE-10")
	if err != nil {
		t.Fatalf("GetIssueDetail returned error: %v", err)
	}
	if len(gateService.calls) != 1 || gateService.calls[0] != issue.ID {
		t.Fatalf("gate service calls = %#v, want one call with the resolved internal ID", gateService.calls)
	}
	if !reflect.DeepEqual(detail.Gates, gateService.summary) {
		t.Fatalf("Gates = %#v, want the gate service's summary", detail.Gates)
	}
}

// TestIssueDetailServiceFailsWhenGateSummaryFails: a gate read failure fails
// the detail read like any other collaborator.
func TestIssueDetailServiceFailsWhenGateSummaryFails(t *testing.T) {
	issue := domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Status: domain.StatusOpen}
	gateService := &stubGateSummaryService{err: domain.NewError(domain.CodeStorageUnavailable, "gate read failed", false)}
	service, err := NewIssueDetailService(&recordingIssueService{issue: issue}, &recordingGraphService{}, &recordingActivityService{}, &recordingReservationLister{}, gateService)
	if err != nil {
		t.Fatalf("NewIssueDetailService returned error: %v", err)
	}
	if _, err := service.GetIssueDetail(context.Background(), "ISSUE-10"); !errors.Is(err, &domain.Error{Code: domain.CodeStorageUnavailable}) {
		t.Fatalf("GetIssueDetail error = %v, want the gate service failure", err)
	}
}

// TestIssueDetailServiceSkipsGraphForArchivedIssue pins ISSUE-341: archived
// issues must not call GetIssueGraph (which rejects archived roots with
// CodeIssueArchived) while still returning the root projection, activity,
// reservations, and gate summary.
func TestIssueDetailServiceSkipsGraphForArchivedIssue(t *testing.T) {
	archivedAt := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	issue := domain.Issue{
		ID:         "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		DisplayID:  "ISSUE-42",
		Title:      "Archived issue",
		Type:       domain.TypeTask,
		Status:     domain.StatusDone,
		Priority:   domain.PriorityMedium,
		Version:    2,
		CreatedAt:  archivedAt.Add(-time.Hour),
		UpdatedAt:  archivedAt,
		ArchivedAt: &archivedAt,
	}
	activity := domain.IssueActivity{
		Items: []domain.ActivityItem{
			{
				EntityType: domain.ActivityEntityTypeComment,
				EntityID:   "01ARZ3NDEKTSV4RRFFQ69G5FB1",
				IssueID:    issue.ID,
				OccurredAt: archivedAt.Add(-10 * time.Minute),
				Comment:    &domain.Comment{ID: "01ARZ3NDEKTSV4RRFFQ69G5FB1", IssueID: issue.ID, Content: "archived context", CreatedAt: archivedAt.Add(-10 * time.Minute)},
			},
		},
	}
	reservations := domain.ReservationList{
		Items: []domain.Reservation{
			{ID: "01ARZ3NDEKTSV4RRFFQ69G5FB5", IssueID: issue.ID, AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FB2", Kind: domain.ResourceKindFile, DisplayValue: "archived.go", Status: domain.ReservationStatusReleased, CreatedAt: archivedAt.Add(-30 * time.Minute), ReleasedAt: &archivedAt},
		},
		HasMore: true,
	}
	gateService := &stubGateSummaryService{summary: domain.WorkContextGateSummary{
		Point:            domain.EnforcementPointClaimWork,
		RequirementCount: 1,
		SatisfiedCount:   1,
		Unmet:            []domain.WorkContextUnmetRequirement{},
		NextActions:      []string{},
	}}
	issueService := &recordingIssueService{issue: issue}
	graphService := &recordingGraphService{err: domain.NewError(domain.CodeIssueArchived, "issue is archived", false)}
	activityService := &recordingActivityService{activity: activity}
	reservationService := &recordingReservationLister{list: reservations}

	service, err := NewIssueDetailService(issueService, graphService, activityService, reservationService, gateService)
	if err != nil {
		t.Fatalf("NewIssueDetailService returned error: %v", err)
	}

	detail, err := service.GetIssueDetail(context.Background(), "ISSUE-42")
	if err != nil {
		t.Fatalf("GetIssueDetail(archived) returned error: %v", err)
	}
	if issueService.calls != 1 {
		t.Fatalf("issue service calls = %d, want 1", issueService.calls)
	}
	if graphService.calls != 0 {
		t.Fatalf("graph service calls = %d, want 0 for archived issue", graphService.calls)
	}
	if activityService.calls != 1 || activityService.input.IssueID != issue.ID {
		t.Fatalf("activity service calls = %d input = %#v, want 1 call for %q", activityService.calls, activityService.input, issue.ID)
	}
	if reservationService.calls != 1 || reservationService.input.IssueID == nil || *reservationService.input.IssueID != issue.ID {
		t.Fatalf("reservation service calls = %d input = %#v, want 1 call for %q", reservationService.calls, reservationService.input, issue.ID)
	}
	if len(gateService.calls) != 1 || gateService.calls[0] != issue.ID {
		t.Fatalf("gate service calls = %#v, want [%q]", gateService.calls, issue.ID)
	}
	if detail.Issue.ArchivedAt == nil || !detail.Issue.ArchivedAt.Equal(archivedAt) {
		t.Fatalf("detail.Issue.ArchivedAt = %v, want %v", detail.Issue.ArchivedAt, archivedAt)
	}
	if detail.RootIssueProjection == nil {
		t.Fatal("expected RootIssueProjection to be populated")
	}
	if detail.RootIssueProjection.ID != issue.ID {
		t.Fatalf("RootIssueProjection.ID = %q, want %q", detail.RootIssueProjection.ID, issue.ID)
	}
	if detail.RootIssueProjection.ArchivedAt == nil || !detail.RootIssueProjection.ArchivedAt.Equal(archivedAt) {
		t.Fatalf("RootIssueProjection.ArchivedAt = %v, want %v", detail.RootIssueProjection.ArchivedAt, archivedAt)
	}
	if detail.RootIssueProjection.EffectiveStatus != domain.EffectiveStatusDone {
		t.Fatalf("RootIssueProjection.EffectiveStatus = %q, want %q", detail.RootIssueProjection.EffectiveStatus, domain.EffectiveStatusDone)
	}
	if detail.Graph.RootIssueID == nil || *detail.Graph.RootIssueID != issue.ID {
		t.Fatalf("Graph.RootIssueID = %#v, want %q", detail.Graph.RootIssueID, issue.ID)
	}
	if detail.Graph.Nodes == nil || len(detail.Graph.Nodes) != 0 {
		t.Fatalf("Graph.Nodes = %#v, want non-nil empty slice", detail.Graph.Nodes)
	}
	if detail.Graph.Edges == nil || len(detail.Graph.Edges) != 0 {
		t.Fatalf("Graph.Edges = %#v, want non-nil empty slice", detail.Graph.Edges)
	}
	if len(detail.Activity.Items) != 1 || detail.Activity.Items[0].EntityID != "01ARZ3NDEKTSV4RRFFQ69G5FB1" {
		t.Fatalf("Activity = %#v, want 1 item", detail.Activity)
	}
	if len(detail.Reservations) != 1 || detail.Reservations[0].DisplayValue != "archived.go" || !detail.HasMoreReservations {
		t.Fatalf("Reservations = %#v HasMore=%v, want archived.go with HasMore=true", detail.Reservations, detail.HasMoreReservations)
	}
	if !reflect.DeepEqual(detail.Gates, gateService.summary) {
		t.Fatalf("Gates = %#v, want %#v", detail.Gates, gateService.summary)
	}
}

type stubIssueDetailSnapshotLoader struct {
	calls   int
	command ports.LoadIssueDetailSnapshotCommand
	detail  domain.IssueDetail
	err     error
}

func (stub *stubIssueDetailSnapshotLoader) LoadIssueDetailSnapshot(_ context.Context, command ports.LoadIssueDetailSnapshotCommand) (domain.IssueDetail, error) {
	stub.calls++
	stub.command = command
	if stub.err != nil {
		return domain.IssueDetail{}, stub.err
	}
	return stub.detail, nil
}

func TestIssueDetailServiceWithSnapshotLoaderDelegatesToSingleSnapshot(t *testing.T) {
	localNow := time.Date(2026, 8, 25, 13, 0, 0, 0, time.FixedZone("UTC+2", 2*3600))
	expectedNow := localNow.UTC()
	fallbackErr := errors.New("unexpected fallback call")

	issueService := &recordingIssueService{err: fallbackErr}
	graphService := &recordingGraphService{err: fallbackErr}
	activityService := &recordingActivityService{err: fallbackErr}
	reservationService := &recordingReservationLister{err: fallbackErr}
	gateService := &stubGateSummaryService{err: fallbackErr}

	service, err := NewIssueDetailService(issueService, graphService, activityService, reservationService, gateService)
	if err != nil {
		t.Fatalf("NewIssueDetailService() error = %v", err)
	}

	want := domain.IssueDetail{
		Issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FB1", SequenceNo: 42, Title: "Archived detail", Status: domain.StatusDone},
	}
	loader := &stubIssueDetailSnapshotLoader{detail: want}
	if returned := service.WithSnapshotLoader(loader).WithClock(clock.NewFakeClock(localNow)); returned != service {
		t.Fatal("WithSnapshotLoader / WithClock did not return receiver for chaining")
	}

	if _, err := service.GetIssueDetail(context.Background(), "not-an-id"); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("GetIssueDetail(invalid) error = %v, want %s", err, domain.CodeInvalidArgument)
	}
	if loader.calls != 0 {
		t.Fatalf("snapshot loader calls after invalid id = %d, want 0", loader.calls)
	}

	got, err := service.GetIssueDetail(context.Background(), "ISSUE-42")
	if err != nil {
		t.Fatalf("GetIssueDetail() error = %v", err)
	}
	if loader.calls != 1 {
		t.Fatalf("snapshot loader calls = %d, want 1", loader.calls)
	}
	if loader.command.Identifier.Kind != domain.IssueIdentifierDisplayID || loader.command.Identifier.SequenceNo != 42 {
		t.Fatalf("command.Identifier = %#v, want display ISSUE-42", loader.command.Identifier)
	}
	if !loader.command.Now.Equal(expectedNow) || loader.command.Now.Location() != time.UTC {
		t.Fatalf("command.Now = %v (%v), want %v UTC", loader.command.Now, loader.command.Now.Location(), expectedNow)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetIssueDetail() = %#v, want %#v", got, want)
	}
	if issueService.calls != 0 || graphService.calls != 0 || activityService.calls != 0 || reservationService.calls != 0 || len(gateService.calls) != 0 {
		t.Fatalf("unexpected fallback calls: issue=%d graph=%d activity=%d reservation=%d gate=%d",
			issueService.calls, graphService.calls, activityService.calls, reservationService.calls, len(gateService.calls))
	}

	loader.err = errors.New("snapshot failure")
	if _, err := service.GetIssueDetail(context.Background(), "ISSUE-42"); !errors.Is(err, loader.err) {
		t.Fatalf("GetIssueDetail() error = %v, want %v", err, loader.err)
	}
}
