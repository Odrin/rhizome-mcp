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

func TestNewBoardServiceRejectsNilDependencies(t *testing.T) {
	issueService, attemptService, reviewService, graphService, source := newBoardServiceDependencies(t, time.Date(2026, 8, 7, 13, 14, 15, 0, time.UTC))

	tests := []struct {
		name     string
		issue    *IssueService
		attempt  *AttemptService
		review   *ReviewService
		graph    *GraphService
		source   clock.Clock
		wantCode string
	}{
		{name: "nil issue service", issue: nil, attempt: attemptService, review: reviewService, graph: graphService, source: source, wantCode: domain.CodeInvalidArgument},
		{name: "nil attempt service", issue: issueService, attempt: nil, review: reviewService, graph: graphService, source: source, wantCode: domain.CodeInvalidArgument},
		{name: "nil review service", issue: issueService, attempt: attemptService, review: nil, graph: graphService, source: source, wantCode: domain.CodeInvalidArgument},
		{name: "nil graph service", issue: issueService, attempt: attemptService, review: reviewService, graph: nil, source: source, wantCode: domain.CodeInvalidArgument},
		{name: "nil clock", issue: issueService, attempt: attemptService, review: reviewService, graph: graphService, source: nil, wantCode: domain.CodeInvalidArgument},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewBoardService(tt.issue, tt.attempt, tt.review, tt.graph, tt.source)
			if !errors.Is(err, &domain.Error{Code: tt.wantCode}) {
				t.Fatalf("NewBoardService() error = %v, want %q", err, tt.wantCode)
			}
		})
	}
}

func TestBoardServiceGetBoardAggregatesBoundedCollectionsAndGraph(t *testing.T) {
	now := time.Date(2026, 8, 7, 13, 14, 15, 0, time.FixedZone("test", 2*60*60))
	issueRepo := &boardRecordingIssueRepository{
		countResult: []domain.EffectiveStatusCount{{EffectiveStatus: domain.EffectiveStatusOpen, Count: 2}, {EffectiveStatus: domain.EffectiveStatusInProgress, Count: 1}},
		listResult:  domain.IssueList{Items: []domain.IssueProjection{{Issue: domain.Issue{ID: "issue-1", DisplayID: "ISSUE-1"}, EffectiveStatus: domain.EffectiveStatusBlocked, IsBlocked: true}}},
	}
	attemptRepo := &boardRecordingAttemptRepository{listResult: []domain.ActiveAttemptSummary{{AttemptID: "attempt-1", IssueID: "issue-1", IssueDisplayID: "ISSUE-1", IssueTitle: "Work", Kind: domain.AttemptKindWork}}}
	reviewRepo := &boardRecordingReviewRepository{listResult: ports.ListReviewRequestsResult{Items: []domain.ReviewRequest{{ID: "review-1", IssueID: "issue-1", Status: domain.ReviewRequestStatusOpen}}}}
	graphRepo := &boardRecordingGraphRepository{snapshot: domain.GraphSnapshot{RootIssueID: boardStringPointer("issue-1"), Nodes: []domain.IssueProjection{{Issue: domain.Issue{ID: "issue-1", DisplayID: "ISSUE-1"}}}}}

	issueService, attemptService, reviewService, graphService, source := newBoardServiceDependenciesWithRepos(t, issueRepo, attemptRepo, reviewRepo, graphRepo, now)
	service, err := NewBoardService(issueService, attemptService, reviewService, graphService, source)
	if err != nil {
		t.Fatalf("NewBoardService() error = %v", err)
	}

	result, err := service.GetBoard(context.Background())
	if err != nil {
		t.Fatalf("GetBoard() error = %v", err)
	}

	if !result.GeneratedAt.Equal(now.UTC()) || result.GeneratedAt.Location() != time.UTC {
		t.Fatalf("GeneratedAt = %v, want %v", result.GeneratedAt, now.UTC())
	}
	if !reflect.DeepEqual(result.StatusCounts, []domain.EffectiveStatusCount{{EffectiveStatus: domain.EffectiveStatusOpen, Count: 2}, {EffectiveStatus: domain.EffectiveStatusInProgress, Count: 1}}) {
		t.Fatalf("StatusCounts = %#v", result.StatusCounts)
	}
	if !reflect.DeepEqual(result.BlockedIssues, []domain.IssueProjection{{Issue: domain.Issue{ID: "issue-1", DisplayID: "ISSUE-1"}, EffectiveStatus: domain.EffectiveStatusBlocked, IsBlocked: true}}) {
		t.Fatalf("BlockedIssues = %#v", result.BlockedIssues)
	}
	if !reflect.DeepEqual(result.ActiveAttempts, []domain.ActiveAttemptSummary{{AttemptID: "attempt-1", IssueID: "issue-1", IssueDisplayID: "ISSUE-1", IssueTitle: "Work", Kind: domain.AttemptKindWork}}) {
		t.Fatalf("ActiveAttempts = %#v", result.ActiveAttempts)
	}
	if !reflect.DeepEqual(result.ReviewRequests, []domain.ReviewRequest{{ID: "review-1", IssueID: "issue-1", Status: domain.ReviewRequestStatusOpen}}) {
		t.Fatalf("ReviewRequests = %#v", result.ReviewRequests)
	}
	if result.PlanningGraph.RootIssueID == nil || *result.PlanningGraph.RootIssueID != "issue-1" {
		t.Fatalf("PlanningGraph.RootIssueID = %#v", result.PlanningGraph.RootIssueID)
	}

	if !issueRepo.countCommand.Now.Equal(now.UTC()) || issueRepo.countCommand.Now.Location() != time.UTC {
		t.Fatalf("issue count clock = %v", issueRepo.countCommand.Now)
	}
	if issueRepo.listCommand.Input.IsBlocked == nil || !*issueRepo.listCommand.Input.IsBlocked {
		t.Fatalf("blocked filter = %#v", issueRepo.listCommand.Input.IsBlocked)
	}
	if issueRepo.listCommand.Input.Limit != domain.MaxBoardCollectionLimit {
		t.Fatalf("blocked limit = %d", issueRepo.listCommand.Input.Limit)
	}
	if attemptRepo.listCommand.Limit != domain.MaxBoardCollectionLimit {
		t.Fatalf("active attempt limit = %d", attemptRepo.listCommand.Limit)
	}
	if reviewRepo.listQuery.Status == nil || *reviewRepo.listQuery.Status != domain.ReviewRequestStatusOpen {
		t.Fatalf("review status query = %#v", reviewRepo.listQuery.Status)
	}
	if reviewRepo.listQuery.Limit != domain.MaxBoardCollectionLimit {
		t.Fatalf("review limit = %d", reviewRepo.listQuery.Limit)
	}
	if graphRepo.command.RootIdentifier != nil {
		t.Fatalf("graph root identifier = %#v", graphRepo.command.RootIdentifier)
	}
	if !graphRepo.command.Now.Equal(now.UTC()) || graphRepo.command.Now.Location() != time.UTC {
		t.Fatalf("graph clock = %v", graphRepo.command.Now)
	}
}

func TestBoardServiceShortCircuitsAtEachDependencyBoundary(t *testing.T) {
	now := time.Date(2026, 8, 7, 13, 14, 15, 0, time.UTC)
	tests := []struct {
		name              string
		configure         func(*boardRecordingIssueRepository, *boardRecordingAttemptRepository, *boardRecordingReviewRepository, *boardRecordingGraphRepository)
		wantCountCalled   bool
		wantListCalled    bool
		wantAttemptCalled bool
		wantReviewCalled  bool
		wantGraphCalled   bool
	}{
		{
			name: "count issues",
			configure: func(issueRepo *boardRecordingIssueRepository, _ *boardRecordingAttemptRepository, _ *boardRecordingReviewRepository, _ *boardRecordingGraphRepository) {
				issueRepo.countErr = errors.New("count failed")
			},
			wantCountCalled: true,
		},
		{
			name: "list issues",
			configure: func(issueRepo *boardRecordingIssueRepository, _ *boardRecordingAttemptRepository, _ *boardRecordingReviewRepository, _ *boardRecordingGraphRepository) {
				issueRepo.listErr = errors.New("list failed")
			},
			wantCountCalled: true,
			wantListCalled:  true,
		},
		{
			name: "active attempts",
			configure: func(_ *boardRecordingIssueRepository, attemptRepo *boardRecordingAttemptRepository, _ *boardRecordingReviewRepository, _ *boardRecordingGraphRepository) {
				attemptRepo.listErr = errors.New("attempts failed")
			},
			wantCountCalled:   true,
			wantListCalled:    true,
			wantAttemptCalled: true,
		},
		{
			name: "review requests",
			configure: func(_ *boardRecordingIssueRepository, _ *boardRecordingAttemptRepository, reviewRepo *boardRecordingReviewRepository, _ *boardRecordingGraphRepository) {
				reviewRepo.listErr = errors.New("reviews failed")
			},
			wantCountCalled:   true,
			wantListCalled:    true,
			wantAttemptCalled: true,
			wantReviewCalled:  true,
		},
		{
			name: "planning graph",
			configure: func(_ *boardRecordingIssueRepository, _ *boardRecordingAttemptRepository, _ *boardRecordingReviewRepository, graphRepo *boardRecordingGraphRepository) {
				graphRepo.err = errors.New("graph failed")
			},
			wantCountCalled:   true,
			wantListCalled:    true,
			wantAttemptCalled: true,
			wantReviewCalled:  true,
			wantGraphCalled:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueRepo := &boardRecordingIssueRepository{}
			attemptRepo := &boardRecordingAttemptRepository{}
			reviewRepo := &boardRecordingReviewRepository{}
			graphRepo := &boardRecordingGraphRepository{}
			tt.configure(issueRepo, attemptRepo, reviewRepo, graphRepo)

			issueService, attemptService, reviewService, graphService, source := newBoardServiceDependenciesWithRepos(t, issueRepo, attemptRepo, reviewRepo, graphRepo, now)
			service, err := NewBoardService(issueService, attemptService, reviewService, graphService, source)
			if err != nil {
				t.Fatalf("NewBoardService() error = %v", err)
			}

			_, err = service.GetBoard(context.Background())
			if err == nil {
				t.Fatal("GetBoard() error = nil, want failure")
			}

			if issueRepo.countCalled != tt.wantCountCalled || issueRepo.listCalled != tt.wantListCalled || attemptRepo.listCalled != tt.wantAttemptCalled || reviewRepo.listCalled != tt.wantReviewCalled || graphRepo.called != tt.wantGraphCalled {
				t.Fatalf("calls = count:%t list:%t attempts:%t reviews:%t graph:%t", issueRepo.countCalled, issueRepo.listCalled, attemptRepo.listCalled, reviewRepo.listCalled, graphRepo.called)
			}
		})
	}
}

func newBoardServiceDependencies(t *testing.T, now time.Time) (*IssueService, *AttemptService, *ReviewService, *GraphService, clock.Clock) {
	t.Helper()
	return newBoardServiceDependenciesWithRepos(t, &boardRecordingIssueRepository{}, &boardRecordingAttemptRepository{}, &boardRecordingReviewRepository{}, &boardRecordingGraphRepository{}, now)
}

func newBoardServiceDependenciesWithRepos(t *testing.T, issueRepo ports.IssueRepository, attemptRepo ports.AttemptRepository, reviewRepo ports.ReviewRepository, graphRepo ports.GraphRepository, now time.Time) (*IssueService, *AttemptService, *ReviewService, *GraphService, clock.Clock) {
	t.Helper()
	issueService, err := NewIssueService(issueRepo, clock.NewFakeClock(now), testIDGenerator{id: "01ARZ3NDEKTSV4RRFFQ69G5FAV"})
	if err != nil {
		t.Fatalf("NewIssueService() error = %v", err)
	}
	attemptService, err := NewAttemptService(attemptRepo, clock.NewFakeClock(now), testIDGenerator{id: "01ARZ3NDEKTSV4RRFFQ69G5FAV"})
	if err != nil {
		t.Fatalf("NewAttemptService() error = %v", err)
	}
	reviewService, err := NewReviewService(reviewRepo, issueRepo, clock.NewFakeClock(now))
	if err != nil {
		t.Fatalf("NewReviewService() error = %v", err)
	}
	graphService, err := NewGraphService(graphRepo, clock.NewFakeClock(now))
	if err != nil {
		t.Fatalf("NewGraphService() error = %v", err)
	}
	return issueService, attemptService, reviewService, graphService, clock.NewFakeClock(now)
}

type testIDGenerator struct {
	id  string
	err error
}

func (generator testIDGenerator) New() (string, error) { return generator.id, generator.err }

type boardRecordingIssueRepository struct {
	countCommand ports.CountIssuesByEffectiveStatusCommand
	listCommand  ports.ListIssuesCommand
	countResult  []domain.EffectiveStatusCount
	listResult   domain.IssueList
	countErr     error
	listErr      error
	countCalled  bool
	listCalled   bool
}

func (repository *boardRecordingIssueRepository) CreateIssue(context.Context, ports.CreateIssueCommand) (domain.Issue, error) {
	return domain.Issue{}, nil
}

func (repository *boardRecordingIssueRepository) LookupCreateIssue(context.Context, string, []byte) (domain.Issue, bool, error) {
	return domain.Issue{}, false, nil
}

func (repository *boardRecordingIssueRepository) UpdateIssue(context.Context, ports.UpdateIssueCommand) (ports.UpdateIssueResult, error) {
	return ports.UpdateIssueResult{}, nil
}

func (repository *boardRecordingIssueRepository) LookupUpdateIssue(context.Context, string, []byte) (ports.UpdateIssueResult, bool, error) {
	return ports.UpdateIssueResult{}, false, nil
}

func (repository *boardRecordingIssueRepository) ArchiveIssue(context.Context, ports.ArchiveIssueCommand) (ports.ArchiveIssueResult, error) {
	return ports.ArchiveIssueResult{}, nil
}

func (repository *boardRecordingIssueRepository) LookupArchiveIssue(context.Context, string, []byte) (ports.ArchiveIssueResult, bool, error) {
	return ports.ArchiveIssueResult{}, false, nil
}

func (repository *boardRecordingIssueRepository) GetIssue(context.Context, domain.IssueIdentifier) (domain.Issue, error) {
	return domain.Issue{}, nil
}

func (repository *boardRecordingIssueRepository) GetIssueProjection(context.Context, ports.GetIssueProjectionCommand) (domain.IssueProjection, error) {
	return domain.IssueProjection{}, nil
}

func (repository *boardRecordingIssueRepository) ListLabels(context.Context, ports.ListLabelsCommand) (domain.LabelList, error) {
	return domain.LabelList{}, nil
}

func (repository *boardRecordingIssueRepository) ListIssues(_ context.Context, command ports.ListIssuesCommand) (domain.IssueList, error) {
	repository.listCalled = true
	repository.listCommand = command
	return repository.listResult, repository.listErr
}

func (repository *boardRecordingIssueRepository) CountIssuesByEffectiveStatus(_ context.Context, command ports.CountIssuesByEffectiveStatusCommand) ([]domain.EffectiveStatusCount, error) {
	repository.countCalled = true
	repository.countCommand = command
	return repository.countResult, repository.countErr
}

type boardRecordingAttemptRepository struct {
	listCommand ports.ListActiveAttemptsCommand
	listResult  []domain.ActiveAttemptSummary
	listErr     error
	listCalled  bool
}

func (repository *boardRecordingAttemptRepository) ClaimIssue(context.Context, ports.ClaimIssueCommand) (ports.ClaimIssueResult, error) {
	return ports.ClaimIssueResult{}, nil
}

func (repository *boardRecordingAttemptRepository) RenewAttempt(context.Context, ports.RenewAttemptCommand) (ports.RenewAttemptResult, error) {
	return ports.RenewAttemptResult{}, nil
}

func (repository *boardRecordingAttemptRepository) SaveAttemptNote(context.Context, ports.SaveAttemptNoteCommand) (ports.SaveAttemptNoteResult, error) {
	return ports.SaveAttemptNoteResult{}, nil
}

func (repository *boardRecordingAttemptRepository) LookupSaveAttemptNote(context.Context, string, []byte) (ports.SaveAttemptNoteResult, bool, error) {
	return ports.SaveAttemptNoteResult{}, false, nil
}

func (repository *boardRecordingAttemptRepository) LookupFinishedAttempt(context.Context, string, []byte) (ports.FinishAttemptResult, bool, error) {
	return ports.FinishAttemptResult{}, false, nil
}

func (repository *boardRecordingAttemptRepository) FinishAttempt(context.Context, ports.FinishAttemptCommand) (ports.FinishAttemptResult, error) {
	return ports.FinishAttemptResult{}, nil
}

func (repository *boardRecordingAttemptRepository) ForceReleaseAttempt(context.Context, ports.ForceReleaseAttemptCommand) (ports.ForceReleaseAttemptResult, error) {
	return ports.ForceReleaseAttemptResult{}, nil
}

func (repository *boardRecordingAttemptRepository) ExpireAttempts(context.Context, ports.ExpireAttemptsCommand) (ports.ExpireAttemptsResult, error) {
	return ports.ExpireAttemptsResult{}, nil
}

func (repository *boardRecordingAttemptRepository) ListActiveAttempts(_ context.Context, command ports.ListActiveAttemptsCommand) ([]domain.ActiveAttemptSummary, error) {
	repository.listCalled = true
	repository.listCommand = command
	return repository.listResult, repository.listErr
}

func (repository *boardRecordingAttemptRepository) SubmitGateEvidence(context.Context, ports.SubmitGateEvidenceCommand) (ports.SubmitGateEvidenceResult, error) {
	return ports.SubmitGateEvidenceResult{}, nil
}

func (repository *boardRecordingAttemptRepository) LookupSubmitGateEvidence(context.Context, string, []byte) (ports.SubmitGateEvidenceResult, bool, error) {
	return ports.SubmitGateEvidenceResult{}, false, nil
}

func (repository *boardRecordingAttemptRepository) ListAttemptEvidence(context.Context, ports.ListAttemptEvidenceCommand) ([]domain.AttemptEvidence, error) {
	return nil, nil
}

type boardRecordingReviewRepository struct {
	listQuery  ports.ListReviewRequestsQuery
	listResult ports.ListReviewRequestsResult
	listErr    error
	listCalled bool
}

func (repository *boardRecordingReviewRepository) CreateReviewRequest(context.Context, ports.CreateReviewRequestCommand) (ports.CreateReviewRequestResult, error) {
	return ports.CreateReviewRequestResult{}, nil
}

func (repository *boardRecordingReviewRepository) GetReviewRequest(context.Context, string) (ports.GetReviewRequestResult, error) {
	return ports.GetReviewRequestResult{}, nil
}

func (repository *boardRecordingReviewRepository) ListReviewRequests(_ context.Context, query ports.ListReviewRequestsQuery) (ports.ListReviewRequestsResult, error) {
	repository.listCalled = true
	repository.listQuery = query
	return repository.listResult, repository.listErr
}

func (repository *boardRecordingReviewRepository) CancelReviewRequest(context.Context, ports.ReviewMutationCommand) (ports.ReviewMutationResult, error) {
	return ports.ReviewMutationResult{}, nil
}

func (repository *boardRecordingReviewRepository) ClaimReviewRequest(context.Context, ports.ReviewMutationCommand) (ports.ReviewMutationResult, error) {
	return ports.ReviewMutationResult{}, nil
}

func (repository *boardRecordingReviewRepository) ResolveReviewRequest(context.Context, ports.ResolveReviewRequestCommand) (ports.ResolveReviewRequestResult, error) {
	return ports.ResolveReviewRequestResult{}, nil
}

func (repository *boardRecordingReviewRepository) ReplaceReviewRequest(context.Context, ports.ReplaceReviewRequestCommand) (ports.ReplaceReviewRequestResult, error) {
	return ports.ReplaceReviewRequestResult{}, nil
}

func (repository *boardRecordingReviewRepository) LookupReplaceReviewRequest(context.Context, string, []byte) (ports.ReplaceReviewRequestResult, bool, error) {
	return ports.ReplaceReviewRequestResult{}, false, nil
}

type boardRecordingGraphRepository struct {
	command  ports.LoadGraphCommand
	snapshot domain.GraphSnapshot
	err      error
	called   bool
}

func (repository *boardRecordingGraphRepository) LoadGraph(_ context.Context, command ports.LoadGraphCommand) (domain.GraphSnapshot, error) {
	repository.called = true
	repository.command = command
	return repository.snapshot, repository.err
}

func boardStringPointer(value string) *string {
	copy := value
	return &copy
}
