package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"rhizome-mcp/internal/domain"
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

	service, err := NewIssueDetailService(issueService, graphService, activityService)
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
		name              string
		issueErr          error
		graphErr          error
		activityErr       error
		wantErrText       string
		wantIssueCalls    int
		wantGraphCalls    int
		wantActivityCalls int
	}{
		{name: "issue error", issueErr: errors.New("issue failure"), wantErrText: "issue failure", wantIssueCalls: 1, wantGraphCalls: 0, wantActivityCalls: 0},
		{name: "graph error", graphErr: errors.New("graph failure"), wantErrText: "graph failure", wantIssueCalls: 1, wantGraphCalls: 1, wantActivityCalls: 0},
		{name: "activity error", activityErr: errors.New("activity failure"), wantErrText: "activity failure", wantIssueCalls: 1, wantGraphCalls: 1, wantActivityCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueService := &recordingIssueService{issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", DisplayID: "ISSUE-10"}, err: tt.issueErr}
			graphService := &recordingGraphService{graph: domain.GraphResult{RootIssueID: ptrString("01ARZ3NDEKTSV4RRFFQ69G5FAV")}, err: tt.graphErr}
			activityService := &recordingActivityService{activity: domain.IssueActivity{}, err: tt.activityErr}
			service, err := NewIssueDetailService(issueService, graphService, activityService)
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
		})
	}
}

func ptrString(value string) *string {
	return &value
}
