package application

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

func TestIssueDetailServiceConstructorAndBranches(t *testing.T) {
	t.Run("constructor rejects nil dependencies", func(t *testing.T) {
		_, err := NewIssueDetailService(nil, nil, nil, nil)
		if err == nil {
			t.Fatal("expected constructor error")
		}
		var domainErr *domain.Error
		if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeInvalidArgument {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("short circuits on dependency errors", func(t *testing.T) {
		cases := []struct {
			name                 string
			issueErr             error
			graphErr             error
			activityErr          error
			reservationErr       error
			wantIssueCalls       int
			wantGraphCalls       int
			wantActivityCalls    int
			wantReservationCalls int
		}{
			{name: "issue error", issueErr: errors.New("issue failed"), wantIssueCalls: 1},
			{name: "graph error", graphErr: errors.New("graph failed"), wantIssueCalls: 1, wantGraphCalls: 1},
			{name: "activity error", activityErr: errors.New("activity failed"), wantIssueCalls: 1, wantGraphCalls: 1, wantActivityCalls: 1},
			{name: "reservation error", reservationErr: errors.New("reservations failed"), wantIssueCalls: 1, wantGraphCalls: 1, wantActivityCalls: 1, wantReservationCalls: 1},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				issueRepo := &recordingIssueDetailIssueService{issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Status: domain.StatusOpen}, err: tc.issueErr}
				graphRepo := &recordingIssueDetailGraphService{graph: domain.GraphResult{}, err: tc.graphErr}
				activityRepo := &recordingIssueDetailActivityService{activity: domain.IssueActivity{}, err: tc.activityErr}
				reservationRepo := &recordingIssueDetailReservationService{err: tc.reservationErr}
				service, err := NewIssueDetailService(issueRepo, graphRepo, activityRepo, reservationRepo)
				if err != nil {
					t.Fatalf("NewIssueDetailService returned error: %v", err)
				}
				_, err = service.GetIssueDetail(context.Background(), "ISSUE-1")
				if err == nil || err.Error() == "" {
					t.Fatalf("expected dependency error, got %v", err)
				}
				if issueRepo.calls != tc.wantIssueCalls {
					t.Fatalf("issue calls = %d, want %d", issueRepo.calls, tc.wantIssueCalls)
				}
				if graphRepo.calls != tc.wantGraphCalls {
					t.Fatalf("graph calls = %d, want %d", graphRepo.calls, tc.wantGraphCalls)
				}
				if activityRepo.calls != tc.wantActivityCalls {
					t.Fatalf("activity calls = %d, want %d", activityRepo.calls, tc.wantActivityCalls)
				}
				if reservationRepo.calls != tc.wantReservationCalls {
					t.Fatalf("reservation calls = %d, want %d", reservationRepo.calls, tc.wantReservationCalls)
				}
			})
		}
	})

	t.Run("uses fallback root projection when graph has no root", func(t *testing.T) {
		issue := domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Status: domain.StatusOpen}
		service, err := NewIssueDetailService(&recordingIssueDetailIssueService{issue: issue}, &recordingIssueDetailGraphService{graph: domain.GraphResult{}}, &recordingIssueDetailActivityService{activity: domain.IssueActivity{}}, &recordingIssueDetailReservationService{})
		if err != nil {
			t.Fatalf("NewIssueDetailService returned error: %v", err)
		}
		detail, err := service.GetIssueDetail(context.Background(), "ISSUE-1")
		if err != nil {
			t.Fatalf("GetIssueDetail returned error: %v", err)
		}
		if detail.RootIssueProjection == nil {
			t.Fatal("expected fallback root issue projection")
		}
		if detail.RootIssueProjection.Issue.ID != issue.ID || detail.RootIssueProjection.EffectiveStatus != domain.EffectiveStatusOpen {
			t.Fatalf("fallback projection = %#v", detail.RootIssueProjection)
		}
	})

	t.Run("returns invalid effective status when issue status is invalid", func(t *testing.T) {
		issue := domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Status: domain.Status("invalid")}
		service, err := NewIssueDetailService(&recordingIssueDetailIssueService{issue: issue}, &recordingIssueDetailGraphService{graph: domain.GraphResult{}}, &recordingIssueDetailActivityService{activity: domain.IssueActivity{}}, &recordingIssueDetailReservationService{})
		if err != nil {
			t.Fatalf("NewIssueDetailService returned error: %v", err)
		}
		_, err = service.GetIssueDetail(context.Background(), "ISSUE-1")
		if err == nil {
			t.Fatal("expected invalid effective status error")
		}
	})

	t.Run("selects the first eligible activity entities", func(t *testing.T) {
		issue := domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Status: domain.StatusOpen}
		activity := domain.IssueActivity{Items: []domain.ActivityItem{
			{EntityType: domain.ActivityEntityTypeComment, EntityID: "01ARZ3NDEKTSV4RRFFQ69G5FB1", IssueID: issue.ID, OccurredAt: time.Unix(10, 0).UTC(), Comment: &domain.Comment{ID: "01ARZ3NDEKTSV4RRFFQ69G5FB1", IssueID: issue.ID, CreatedAt: time.Unix(10, 0).UTC()}},
			{EntityType: domain.ActivityEntityTypeAttempt, EntityID: "01ARZ3NDEKTSV4RRFFQ69G5FB2", IssueID: issue.ID, OccurredAt: time.Unix(11, 0).UTC(), Attempt: &domain.WorkAttempt{ID: "01ARZ3NDEKTSV4RRFFQ69G5FB2", IssueID: issue.ID, StartedAt: time.Unix(11, 0).UTC()}},
			{EntityType: domain.ActivityEntityTypeReview, EntityID: "01ARZ3NDEKTSV4RRFFQ69G5FB3", IssueID: issue.ID, OccurredAt: time.Unix(12, 0).UTC(), Review: &domain.ReviewRequest{ID: "01ARZ3NDEKTSV4RRFFQ69G5FB3", IssueID: issue.ID, Status: domain.ReviewRequestStatusCancelled, CreatedAt: time.Unix(12, 0).UTC()}},
			{EntityType: domain.ActivityEntityTypeAttempt, EntityID: "01ARZ3NDEKTSV4RRFFQ69G5FB4", IssueID: issue.ID, OccurredAt: time.Unix(13, 0).UTC(), Attempt: &domain.WorkAttempt{ID: "01ARZ3NDEKTSV4RRFFQ69G5FB4", IssueID: issue.ID, StartedAt: time.Unix(13, 0).UTC()}},
			{EntityType: domain.ActivityEntityTypeReview, EntityID: "01ARZ3NDEKTSV4RRFFQ69G5FB5", IssueID: issue.ID, OccurredAt: time.Unix(14, 0).UTC(), Review: &domain.ReviewRequest{ID: "01ARZ3NDEKTSV4RRFFQ69G5FB5", IssueID: issue.ID, Status: domain.ReviewRequestStatusOpen, CreatedAt: time.Unix(14, 0).UTC()}},
			{EntityType: domain.ActivityEntityTypeDecision, EntityID: "01ARZ3NDEKTSV4RRFFQ69G5FB6", IssueID: issue.ID, OccurredAt: time.Unix(15, 0).UTC(), Decision: &domain.Decision{ID: "01ARZ3NDEKTSV4RRFFQ69G5FB6", IssueID: &issue.ID, CreatedAt: time.Unix(15, 0).UTC()}},
			{EntityType: domain.ActivityEntityTypeDecision, EntityID: "01ARZ3NDEKTSV4RRFFQ69G5FB7", IssueID: issue.ID, OccurredAt: time.Unix(16, 0).UTC(), Decision: &domain.Decision{ID: "01ARZ3NDEKTSV4RRFFQ69G5FB7", IssueID: &issue.ID, CreatedAt: time.Unix(16, 0).UTC()}},
		}}
		service, err := NewIssueDetailService(&recordingIssueDetailIssueService{issue: issue}, &recordingIssueDetailGraphService{graph: domain.GraphResult{RootIssueID: ptrString(issue.ID)}}, &recordingIssueDetailActivityService{activity: activity}, &recordingIssueDetailReservationService{})
		if err != nil {
			t.Fatalf("NewIssueDetailService returned error: %v", err)
		}
		detail, err := service.GetIssueDetail(context.Background(), "ISSUE-1")
		if err != nil {
			t.Fatalf("GetIssueDetail returned error: %v", err)
		}
		if detail.LatestAttempt == nil || detail.LatestAttempt.ID != "01ARZ3NDEKTSV4RRFFQ69G5FB2" {
			t.Fatalf("latest attempt = %#v", detail.LatestAttempt)
		}
		if detail.OpenReview == nil || detail.OpenReview.ID != "01ARZ3NDEKTSV4RRFFQ69G5FB5" {
			t.Fatalf("open review = %#v", detail.OpenReview)
		}
		if detail.LatestDecision == nil || detail.LatestDecision.ID != "01ARZ3NDEKTSV4RRFFQ69G5FB6" {
			t.Fatalf("latest decision = %#v", detail.LatestDecision)
		}
	})
}

func TestReviewServiceValidationAndDelegation(t *testing.T) {
	fixedTime := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)

	t.Run("resolves display IDs and copies artifact IDs", func(t *testing.T) {
		issueRepo := &recordingIssueRepository{resolvedIssue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}}
		reviewRepo := &recordingReviewRepository{}
		fakeClock := clock.NewFakeClock(fixedTime)
		generator, _ := ids.NewGenerator(fakeClock, rand.Reader)
		service, err := NewReviewService(reviewRepo, issueRepo, fakeClock, generator)
		if err != nil {
			t.Fatalf("NewReviewService returned error: %v", err)
		}

		artifactIDs := []string{"artifact-1"}
		_, err = service.CreateReviewRequest(context.Background(), CreateReviewRequestInput{
			IssueID:            "ISSUE-7",
			TargetIssueVersion: 2,
			TargetEventID:      9,
			ArtifactIDs:        artifactIDs,
		})
		if err != nil {
			t.Fatalf("CreateReviewRequest returned error: %v", err)
		}
		if issueRepo.calls != 1 || issueRepo.lastIdentifier.Kind != domain.IssueIdentifierDisplayID {
			t.Fatalf("issue repository calls = %d, last identifier = %#v", issueRepo.calls, issueRepo.lastIdentifier)
		}
		if reviewRepo.createCalls != 1 {
			t.Fatalf("create calls = %d", reviewRepo.createCalls)
		}
		if reviewRepo.lastCreateCommand.IssueID != "01ARZ3NDEKTSV4RRFFQ69G5FAV" {
			t.Fatalf("create issue id = %q", reviewRepo.lastCreateCommand.IssueID)
		}
		if !reviewRepo.lastCreateCommand.OccurredAt.Equal(fixedTime.UTC()) {
			t.Fatalf("create occurred at = %v, want %v", reviewRepo.lastCreateCommand.OccurredAt, fixedTime.UTC())
		}
		artifactIDs[0] = "artifact-2"
		if got := reviewRepo.lastCreateCommand.ArtifactIDs; len(got) != 1 || got[0] != "artifact-1" {
			t.Fatalf("create artifact ids = %v, want [artifact-1]", got)
		}

		_, err = service.CreateReviewRequest(context.Background(), CreateReviewRequestInput{IssueID: "   ", TargetIssueVersion: 1, TargetEventID: 0})
		if err == nil {
			t.Fatal("expected validation error")
		}
	})

	t.Run("rejects invalid list cursor and status before repository calls", func(t *testing.T) {
		reviewRepo := &recordingReviewRepository{}
		fakeClock := clock.NewFakeClock(fixedTime)
		generator, _ := ids.NewGenerator(fakeClock, rand.Reader)
		service, err := NewReviewService(reviewRepo, &recordingIssueRepository{}, fakeClock, generator)
		if err != nil {
			t.Fatalf("NewReviewService returned error: %v", err)
		}

		cursor := "not-a-number"
		_, err = service.ListReviewRequests(context.Background(), ListReviewRequestsInput{Cursor: &cursor})
		if err == nil {
			t.Fatal("expected cursor validation error")
		}
		if reviewRepo.listCalls != 0 {
			t.Fatalf("repository list calls = %d, want 0", reviewRepo.listCalls)
		}

		status := "not-a-status"
		_, err = service.ListReviewRequests(context.Background(), ListReviewRequestsInput{Status: &status})
		if err == nil {
			t.Fatal("expected status validation error")
		}
		if reviewRepo.listCalls != 0 {
			t.Fatalf("repository list calls = %d, want 0", reviewRepo.listCalls)
		}
	})

	t.Run("filters list results and derives next cursor", func(t *testing.T) {
		reviewRepo := &recordingReviewRepository{listItems: []domain.ReviewRequest{{ID: "req-open", Status: domain.ReviewRequestStatusOpen}, {ID: "req-claimed", Status: domain.ReviewRequestStatusClaimed}, {ID: "req-approved", Status: domain.ReviewRequestStatusApproved}}, listHasMore: true, nextOffset: 7}
		fakeClock := clock.NewFakeClock(fixedTime)
		generator, _ := ids.NewGenerator(fakeClock, rand.Reader)
		service, err := NewReviewService(reviewRepo, &recordingIssueRepository{}, fakeClock, generator)
		if err != nil {
			t.Fatalf("NewReviewService returned error: %v", err)
		}

		claimable := true
		cursor := "3"
		result, err := service.ListReviewRequests(context.Background(), ListReviewRequestsInput{Status: stringPointer("open"), Claimable: &claimable, Limit: 20, Cursor: &cursor})
		if err != nil {
			t.Fatalf("ListReviewRequests returned error: %v", err)
		}
		if reviewRepo.lastListQuery.Status == nil || *reviewRepo.lastListQuery.Status != domain.ReviewRequestStatusOpen {
			t.Fatalf("list status = %#v", reviewRepo.lastListQuery.Status)
		}
		if reviewRepo.lastListQuery.Offset != 3 || reviewRepo.lastListQuery.Limit != 20 {
			t.Fatalf("list query = %#v", reviewRepo.lastListQuery)
		}
		if len(result.Items) != 1 || result.Items[0].Request.ID != "req-open" || !result.Items[0].Claimable {
			t.Fatalf("filtered items = %#v", result.Items)
		}
		if result.NextCursor == nil || *result.NextCursor != "7" || !result.HasMore {
			t.Fatalf("next cursor = %#v, has more = %v", result.NextCursor, result.HasMore)
		}
	})

	t.Run("uses utc clock for cancel and supersede mutations", func(t *testing.T) {
		reviewRepo := &recordingReviewRepository{}
		fakeClock := clock.NewFakeClock(fixedTime)
		generator, _ := ids.NewGenerator(fakeClock, rand.Reader)
		service, err := NewReviewService(reviewRepo, &recordingIssueRepository{}, fakeClock, generator)
		if err != nil {
			t.Fatalf("NewReviewService returned error: %v", err)
		}

		cancelled, err := service.CancelReviewRequest(context.Background(), ReviewMutationInput{RequestID: "req-1", ExpectedVersion: 2})
		if err != nil {
			t.Fatalf("CancelReviewRequest returned error: %v", err)
		}
		if cancelled.Claimable {
			t.Fatal("cancelled result should not be claimable")
		}
		if !reviewRepo.lastMutationCommand.OccurredAt.Equal(fixedTime.UTC()) {
			t.Fatalf("cancel occurred at = %v", reviewRepo.lastMutationCommand.OccurredAt)
		}
	})

	t.Run("uses canonical hash for replace and replays writes", func(t *testing.T) {
		reviewRepo := &recordingReviewRepository{}
		fakeClock := clock.NewFakeClock(fixedTime)
		generator, _ := ids.NewGenerator(fakeClock, rand.Reader)
		service, err := NewReviewService(reviewRepo, &recordingIssueRepository{}, fakeClock, generator)
		if err != nil {
			t.Fatalf("NewReviewService returned error: %v", err)
		}

		artifactIDs := []string{"artifact-1"}
		_, err = service.ReplaceReviewRequest(context.Background(), ReplaceReviewRequestInput{
			PredecessorRequestID:       "req-1",
			PredecessorExpectedVersion: 2,
			TargetIssueVersion:         3,
			TargetEventID:              11,
			ArtifactIDs:                artifactIDs,
			IdempotencyKey:             "replace-key",
		})
		if err != nil {
			t.Fatalf("ReplaceReviewRequest returned error: %v", err)
		}
		artifactIDs[0] = "artifact-2"
		if got := reviewRepo.lastReplaceCommand.ArtifactIDs; len(got) != 1 || got[0] != "artifact-1" {
			t.Fatalf("replace artifact ids = %v, want [artifact-1]", got)
		}

		canonical, err := domain.CanonicalReplaceReviewRequestRequest(domain.ReplaceReviewRequestInput{
			PredecessorRequestID:       "req-1",
			PredecessorExpectedVersion: 2,
			TargetIssueVersion:         3,
			TargetEventID:              11,
			ArtifactIDs:                []string{"artifact-1"},
			IdempotencyKey:             "replace-key",
		})
		if err != nil {
			t.Fatalf("CanonicalReplaceReviewRequestRequest returned error: %v", err)
		}
		expectedHash := sha256.Sum256(canonical)
		if !bytes.Equal(reviewRepo.lookupHash, expectedHash[:]) {
			t.Fatalf("lookup hash = %x, want %x", reviewRepo.lookupHash, expectedHash[:])
		}
		if reviewRepo.lookupKey != "replace-key" {
			t.Fatalf("lookup key = %q", reviewRepo.lookupKey)
		}

		reviewRepo.replayResult = &ports.ReplaceReviewRequestResult{Predecessor: domain.ReviewRequest{ID: "req-1", Status: domain.ReviewRequestStatusSuperseded}, Successor: domain.ReviewRequest{ID: "req-2", Status: domain.ReviewRequestStatusOpen}, LatestEventID: 99}
		result, err := service.ReplaceReviewRequest(context.Background(), ReplaceReviewRequestInput{
			PredecessorRequestID:       "req-1",
			PredecessorExpectedVersion: 2,
			TargetIssueVersion:         3,
			TargetEventID:              11,
			ArtifactIDs:                []string{"artifact-1"},
			IdempotencyKey:             "replace-key",
		})
		if err != nil {
			t.Fatalf("replay ReplaceReviewRequest returned error: %v", err)
		}
		if result.Successor.ID != "req-2" || result.LatestEventID != 99 {
			t.Fatalf("replay result = %#v", result)
		}
		if reviewRepo.writeCalls != 1 {
			t.Fatalf("write calls = %d, want 1", reviewRepo.writeCalls)
		}
	})
}

func TestProjectServiceConstructorAndImportValidation(t *testing.T) {
	t.Run("constructor rejects nil repository", func(t *testing.T) {
		_, err := NewProjectService(nil, fixedAttemptIDGenerator("01ARZ3NDEKTSV4RRFFQ69G5FA1"))
		if err == nil {
			t.Fatal("expected constructor error")
		}
		var domainErr *domain.Error
		if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeInvalidArgument {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("returns parse errors before checking destination content", func(t *testing.T) {
		repo := &recordingProjectRepository{}
		service, err := NewProjectService(repo, fixedAttemptIDGenerator("01ARZ3NDEKTSV4RRFFQ69G5FA1"))
		if err != nil {
			t.Fatalf("NewProjectService returned error: %v", err)
		}
		_, err = service.ValidateLogicalProjectImport(context.Background(), []byte("not-json"))
		if err == nil {
			t.Fatal("expected parse error")
		}
		if repo.hasDestinationContentCalls != 0 {
			t.Fatalf("destination checks = %d, want 0", repo.hasDestinationContentCalls)
		}
	})

	t.Run("adds empty-destination conflicts when destination is occupied", func(t *testing.T) {
		repo := &recordingProjectRepository{hasDestinationContent: true}
		service, err := NewProjectService(repo, fixedAttemptIDGenerator("01ARZ3NDEKTSV4RRFFQ69G5FA1"))
		if err != nil {
			t.Fatalf("NewProjectService returned error: %v", err)
		}
		dryRun, err := service.ValidateLogicalProjectImport(context.Background(), validLogicalProjectDocument())
		if err != nil {
			t.Fatalf("ValidateLogicalProjectImport returned error: %v", err)
		}
		if repo.hasDestinationContentCalls != 1 {
			t.Fatalf("destination checks = %d, want 1", repo.hasDestinationContentCalls)
		}
		if len(dryRun.Conflicts) != 1 || dryRun.Conflicts[0].Code != "empty_destination_required" || dryRun.Conflicts[0].Field != "$.destination" {
			t.Fatalf("dry run conflicts = %#v", dryRun.Conflicts)
		}
	})
}

func validLogicalProjectDocument() []byte {
	return []byte(`{"format":"rhizome-logical-project","version":1,"exported_at":"2026-07-01T00:00:00Z","project":{"id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","name":"demo","instructions":"","created_at":"2026-07-01T00:00:00Z","updated_at":"2026-07-01T00:00:00Z"},"issues":[],"labels":[],"issue_labels":[],"relations":[],"comments":[],"decisions":[],"attempts":[],"attempt_notes":[],"artifacts":[],"events":[]}`)
}

type recordingIssueDetailIssueService struct {
	calls int
	issue domain.Issue
	err   error
}

func (s *recordingIssueDetailIssueService) GetIssue(context.Context, string) (domain.Issue, error) {
	s.calls++
	return s.issue, s.err
}

type recordingIssueDetailGraphService struct {
	calls int
	graph domain.GraphResult
	err   error
}

func (s *recordingIssueDetailGraphService) GetIssueGraph(context.Context, domain.GetIssueGraphInput) (domain.GraphResult, error) {
	s.calls++
	return s.graph, s.err
}

type recordingIssueDetailActivityService struct {
	calls    int
	activity domain.IssueActivity
	err      error
}

func (s *recordingIssueDetailActivityService) GetIssueActivity(context.Context, domain.GetIssueActivityInput) (domain.IssueActivity, error) {
	s.calls++
	return s.activity, s.err
}

type recordingIssueDetailReservationService struct {
	calls int
	list  domain.ReservationList
	err   error
}

func (s *recordingIssueDetailReservationService) ListReservations(context.Context, domain.ListResourceReservationsInput) (domain.ReservationList, error) {
	s.calls++
	return s.list, s.err
}

type recordingIssueRepository struct {
	calls           int
	lastIdentifier  domain.IssueIdentifier
	resolvedIssue   domain.Issue
	resolveErr      error
	createCalls     int
	lastCreateInput ports.CreateIssueCommand
}

func (r *recordingIssueRepository) CreateIssue(context.Context, ports.CreateIssueCommand) (domain.Issue, error) {
	r.createCalls++
	return domain.Issue{}, nil
}

func (r *recordingIssueRepository) LookupCreateIssue(context.Context, string, []byte) (domain.Issue, bool, error) {
	return domain.Issue{}, false, nil
}

func (r *recordingIssueRepository) UpdateIssue(context.Context, ports.UpdateIssueCommand) (ports.UpdateIssueResult, error) {
	return ports.UpdateIssueResult{}, nil
}

func (r *recordingIssueRepository) LookupUpdateIssue(context.Context, string, []byte) (ports.UpdateIssueResult, bool, error) {
	return ports.UpdateIssueResult{}, false, nil
}

func (r *recordingIssueRepository) ArchiveIssue(context.Context, ports.ArchiveIssueCommand) (ports.ArchiveIssueResult, error) {
	return ports.ArchiveIssueResult{}, nil
}

func (r *recordingIssueRepository) LookupArchiveIssue(context.Context, string, []byte) (ports.ArchiveIssueResult, bool, error) {
	return ports.ArchiveIssueResult{}, false, nil
}

func (r *recordingIssueRepository) GetIssue(_ context.Context, identifier domain.IssueIdentifier) (domain.Issue, error) {
	r.calls++
	r.lastIdentifier = identifier
	if r.resolveErr != nil {
		return domain.Issue{}, r.resolveErr
	}
	return r.resolvedIssue, nil
}

func (r *recordingIssueRepository) GetIssueProjection(_ context.Context, command ports.GetIssueProjectionCommand) (domain.IssueProjection, error) {
	r.calls++
	r.lastIdentifier = command.Identifier
	if r.resolveErr != nil {
		return domain.IssueProjection{}, r.resolveErr
	}
	return domain.IssueProjection{Issue: r.resolvedIssue}, nil
}

func (r *recordingIssueRepository) ListLabels(context.Context, ports.ListLabelsCommand) (domain.LabelList, error) {
	return domain.LabelList{}, nil
}

func (r *recordingIssueRepository) ListIssues(context.Context, ports.ListIssuesCommand) (domain.IssueList, error) {
	return domain.IssueList{}, nil
}

func (r *recordingIssueRepository) CountIssuesByEffectiveStatus(context.Context, ports.CountIssuesByEffectiveStatusCommand) ([]domain.EffectiveStatusCount, error) {
	return nil, nil
}

type recordingReviewRepository struct {
	createCalls         int
	lastCreateCommand   ports.CreateReviewRequestCommand
	listCalls           int
	lastListQuery       ports.ListReviewRequestsQuery
	listItems           []domain.ReviewRequest
	listHasMore         bool
	nextOffset          int
	mutationCalls       int
	lastMutationCommand ports.ReviewMutationCommand
	writeCalls          int
	lastReplaceCommand  ports.ReplaceReviewRequestCommand
	lookupKey           string
	lookupHash          []byte
	replayResult        *ports.ReplaceReviewRequestResult
}

func (r *recordingReviewRepository) CreateReviewRequest(_ context.Context, command ports.CreateReviewRequestCommand) (ports.CreateReviewRequestResult, error) {
	r.createCalls++
	r.lastCreateCommand = command
	return ports.CreateReviewRequestResult{Request: domain.ReviewRequest{ID: "req-1", IssueID: command.IssueID, Status: domain.ReviewRequestStatusOpen, Version: 1}}, nil
}

func (r *recordingReviewRepository) GetReviewRequest(context.Context, string) (ports.GetReviewRequestResult, error) {
	return ports.GetReviewRequestResult{}, nil
}

func (r *recordingReviewRepository) ListReviewRequests(_ context.Context, query ports.ListReviewRequestsQuery) (ports.ListReviewRequestsResult, error) {
	r.listCalls++
	r.lastListQuery = query
	return ports.ListReviewRequestsResult{Items: r.listItems, HasMore: r.listHasMore, NextOffset: r.nextOffset}, nil
}

func (r *recordingReviewRepository) CancelReviewRequest(_ context.Context, command ports.ReviewMutationCommand) (ports.ReviewMutationResult, error) {
	r.mutationCalls++
	r.lastMutationCommand = command
	return ports.ReviewMutationResult{Request: domain.ReviewRequest{ID: command.RequestID, Status: domain.ReviewRequestStatusCancelled, Version: command.ExpectedVersion + 1}}, nil
}

func (r *recordingReviewRepository) ClaimReviewRequest(context.Context, ports.ReviewMutationCommand) (ports.ReviewMutationResult, error) {
	return ports.ReviewMutationResult{}, nil
}

func (r *recordingReviewRepository) ResolveReviewRequest(context.Context, ports.ResolveReviewRequestCommand) (ports.ResolveReviewRequestResult, error) {
	return ports.ResolveReviewRequestResult{}, nil
}

func (r *recordingReviewRepository) ReplaceReviewRequest(_ context.Context, command ports.ReplaceReviewRequestCommand) (ports.ReplaceReviewRequestResult, error) {
	r.writeCalls++
	r.lastReplaceCommand = command
	return ports.ReplaceReviewRequestResult{Predecessor: domain.ReviewRequest{ID: command.PredecessorRequestID, Status: domain.ReviewRequestStatusSuperseded}, Successor: domain.ReviewRequest{ID: "req-2", Status: domain.ReviewRequestStatusOpen}, LatestEventID: 42}, nil
}

func (r *recordingReviewRepository) LookupReplaceReviewRequest(_ context.Context, key string, hash []byte) (ports.ReplaceReviewRequestResult, bool, error) {
	r.lookupKey = key
	r.lookupHash = append([]byte(nil), hash...)
	if r.replayResult != nil {
		return *r.replayResult, true, nil
	}
	return ports.ReplaceReviewRequestResult{}, false, nil
}

type recordingProjectRepository struct {
	hasDestinationContent      bool
	hasDestinationContentCalls int
}

func (r *recordingProjectRepository) GetProject(context.Context) (domain.Project, error) {
	return domain.Project{}, nil
}

func (r *recordingProjectRepository) ExportLogicalProject(context.Context) (domain.LogicalProjectDocument, error) {
	return domain.LogicalProjectDocument{}, nil
}

func (r *recordingProjectRepository) HasLogicalProjectImportDestinationContent(context.Context) (bool, error) {
	r.hasDestinationContentCalls++
	return r.hasDestinationContent, nil
}

func (r *recordingProjectRepository) ApplyLogicalProjectImport(context.Context, domain.LogicalProjectImportPlan) (domain.LogicalProjectImportApplyResult, error) {
	return domain.LogicalProjectImportApplyResult{}, nil
}
