package application

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

func TestReviewServiceCreatesAndMutatesRequests(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	issueRepository := &issueRepositoryStub{issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}}
	reviewRepository := &reviewRepositoryStub{}
	fakeClock := clock.NewFakeClock(now)
	generator, err := ids.NewGenerator(fakeClock, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewReviewService(reviewRepository, issueRepository, fakeClock, generator)
	if err != nil {
		t.Fatal(err)
	}

	created, err := service.CreateReviewRequest(context.Background(), CreateReviewRequestInput{
		IssueID:            "ISSUE-1",
		TargetIssueVersion: 2,
		TargetEventID:      9,
		ArtifactIDs:        []string{"artifact-1"},
	})
	if err != nil {
		t.Fatalf("CreateReviewRequest() error = %v", err)
	}
	if created.Request.ID == "" || created.Request.IssueID != "01ARZ3NDEKTSV4RRFFQ69G5FAV" || !created.Claimable {
		t.Fatalf("CreateReviewRequest() = %#v", created)
	}
	if reviewRepository.createCommand.IssueID != "01ARZ3NDEKTSV4RRFFQ69G5FAV" {
		t.Fatalf("create issue id = %q", reviewRepository.createCommand.IssueID)
	}

	got, err := service.GetReviewRequest(context.Background(), created.Request.ID)
	if err != nil {
		t.Fatalf("GetReviewRequest() error = %v", err)
	}
	if got.Request.ID != created.Request.ID || !got.Claimable {
		t.Fatalf("GetReviewRequest() = %#v", got)
	}

	listed, err := service.ListReviewRequests(context.Background(), ListReviewRequestsInput{Status: stringPointer("open"), Claimable: boolPointer(true), Limit: 20})
	if err != nil {
		t.Fatalf("ListReviewRequests() error = %v", err)
	}
	if len(listed.Items) != 1 || !listed.Items[0].Claimable || listed.Items[0].Request.ID != created.Request.ID {
		t.Fatalf("ListReviewRequests() = %#v", listed)
	}

	cancelled, err := service.CancelReviewRequest(context.Background(), ReviewMutationInput{RequestID: created.Request.ID, ExpectedVersion: created.Request.Version})
	if err != nil {
		t.Fatalf("CancelReviewRequest() error = %v", err)
	}
	if cancelled.Request.Status != domain.ReviewRequestStatusCancelled || cancelled.Claimable {
		t.Fatalf("CancelReviewRequest() = %#v", cancelled)
	}
}

func TestReviewServiceReplaceReviewRequestDelegatesAndValidates(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	issueRepository := &issueRepositoryStub{issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}}
	reviewRepository := &reviewRepositoryStub{request: domain.ReviewRequest{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", IssueID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Status: domain.ReviewRequestStatusOpen, Version: 3}}
	fakeClock := clock.NewFakeClock(now)
	generator, err := ids.NewGenerator(fakeClock, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewReviewService(reviewRepository, issueRepository, fakeClock, generator)
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.ReplaceReviewRequest(context.Background(), ReplaceReviewRequestInput{
		PredecessorRequestID:       "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		PredecessorExpectedVersion: 3,
		TargetIssueVersion:         4,
		TargetEventID:              12,
		ArtifactIDs:                []string{"artifact-2"},
		IdempotencyKey:             "replace-key-1",
	})
	if err != nil {
		t.Fatalf("ReplaceReviewRequest() error = %v", err)
	}
	if result.Predecessor.Status != domain.ReviewRequestStatusSuperseded {
		t.Fatalf("predecessor status = %q, want superseded", result.Predecessor.Status)
	}
	if result.Successor.Status != domain.ReviewRequestStatusOpen || result.Successor.SupersedesID == nil || *result.Successor.SupersedesID != "01ARZ3NDEKTSV4RRFFQ69G5FAV" {
		t.Fatalf("successor = %+v", result.Successor)
	}
	if result.LatestEventID != 42 {
		t.Fatalf("latest_event_id = %d, want 42", result.LatestEventID)
	}
	if reviewRepository.replaceCommand.PredecessorRequestID != "01ARZ3NDEKTSV4RRFFQ69G5FAV" || reviewRepository.replaceCommand.IdempotencyKey != "replace-key-1" {
		t.Fatalf("replace command = %+v", reviewRepository.replaceCommand)
	}

	// A reused idempotency key must replay without invoking the writer again.
	reviewRepository.replaceReplay = reviewRepository.replaceResult
	reviewRepository.replaceCommand = ports.ReplaceReviewRequestCommand{}
	replayed, err := service.ReplaceReviewRequest(context.Background(), ReplaceReviewRequestInput{
		PredecessorRequestID:       "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		PredecessorExpectedVersion: 3,
		TargetIssueVersion:         4,
		TargetEventID:              12,
		ArtifactIDs:                []string{"artifact-2"},
		IdempotencyKey:             "replace-key-1",
	})
	if err != nil {
		t.Fatalf("replayed ReplaceReviewRequest() error = %v", err)
	}
	if replayed.Successor.ID != result.Successor.ID || reviewRepository.replaceCommand.PredecessorRequestID != "" {
		t.Fatalf("replay unexpectedly invoked the writer: command = %+v", reviewRepository.replaceCommand)
	}

	if _, err := service.ReplaceReviewRequest(context.Background(), ReplaceReviewRequestInput{PredecessorRequestID: "", PredecessorExpectedVersion: 1, TargetIssueVersion: 1, IdempotencyKey: "k"}); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("blank predecessor_request_id error = %v, want INVALID_ARGUMENT", err)
	}
	if _, err := service.ReplaceReviewRequest(context.Background(), ReplaceReviewRequestInput{PredecessorRequestID: "req", PredecessorExpectedVersion: 1, TargetIssueVersion: 1, IdempotencyKey: ""}); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("blank idempotency_key error = %v, want INVALID_ARGUMENT", err)
	}
}

func TestReviewServiceListFiltersByStatusAndClaimability(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFakeClock(now)
	generator, _ := ids.NewGenerator(fakeClock, rand.Reader)
	service, err := NewReviewService(&reviewRepositoryStub{request: domain.ReviewRequest{ID: "req-1", Status: domain.ReviewRequestStatusApproved}}, &issueRepositoryStub{}, fakeClock, generator)
	if err != nil {
		t.Fatal(err)
	}

	listed, err := service.ListReviewRequests(context.Background(), ListReviewRequestsInput{Status: stringPointer("approved"), Claimable: boolPointer(false), Limit: 20})
	if err != nil {
		t.Fatalf("ListReviewRequests() error = %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].Request.ID != "req-1" || listed.Items[0].Claimable {
		t.Fatalf("ListReviewRequests() = %#v", listed)
	}
}

// TestReviewServiceListAppliesCollectionLimitPolicy pins the ISSUE-203 AC6
// policy. The limit rule used to be duplicated as a silent clamp in both the
// service and the SQLite repository, so asking for 101 quietly returned 100
// instead of saying no. The rule now lives once, in Validate, and matches
// ListIssuesInput: zero means the default, above the maximum is an error.
func TestReviewServiceListAppliesCollectionLimitPolicy(t *testing.T) {
	newService := func(t *testing.T) (*ReviewService, *reviewRepositoryStub) {
		t.Helper()
		fakeClock := clock.NewFakeClock(time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC))
		generator, _ := ids.NewGenerator(fakeClock, rand.Reader)
		repository := &reviewRepositoryStub{request: domain.ReviewRequest{ID: "req-1", Status: domain.ReviewRequestStatusOpen}}
		service, err := NewReviewService(repository, &issueRepositoryStub{}, fakeClock, generator)
		if err != nil {
			t.Fatal(err)
		}
		return service, repository
	}

	t.Run("zero limit takes the default", func(t *testing.T) {
		service, repository := newService(t)
		if _, err := service.ListReviewRequests(context.Background(), ListReviewRequestsInput{Limit: 0}); err != nil {
			t.Fatalf("ListReviewRequests(limit 0) error = %v", err)
		}
		if repository.listQuery.Limit != domain.DefaultIssueListLimit {
			t.Fatalf("repository limit = %d, want the default %d", repository.listQuery.Limit, domain.DefaultIssueListLimit)
		}
	})

	t.Run("maximum limit is accepted unchanged", func(t *testing.T) {
		service, repository := newService(t)
		if _, err := service.ListReviewRequests(context.Background(), ListReviewRequestsInput{Limit: domain.MaxCollectionLimit}); err != nil {
			t.Fatalf("ListReviewRequests(limit %d) error = %v", domain.MaxCollectionLimit, err)
		}
		if repository.listQuery.Limit != domain.MaxCollectionLimit {
			t.Fatalf("repository limit = %d, want %d", repository.listQuery.Limit, domain.MaxCollectionLimit)
		}
	})

	t.Run("above the maximum is rejected, not clamped", func(t *testing.T) {
		service, repository := newService(t)
		_, err := service.ListReviewRequests(context.Background(), ListReviewRequestsInput{Limit: domain.MaxCollectionLimit + 1})
		if err == nil {
			t.Fatal("ListReviewRequests(limit 101) succeeded, want an out-of-range error")
		}
		var domainErr *domain.Error
		if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeInvalidArgument {
			t.Fatalf("error = %v, want a domain INVALID_ARGUMENT", err)
		}
		if repository.listQuery.Limit != 0 {
			t.Fatalf("repository was queried with limit %d; a rejected request must not reach storage", repository.listQuery.Limit)
		}
	})

	t.Run("negative limit is rejected", func(t *testing.T) {
		service, _ := newService(t)
		if _, err := service.ListReviewRequests(context.Background(), ListReviewRequestsInput{Limit: -1}); err == nil {
			t.Fatal("ListReviewRequests(limit -1) succeeded, want an out-of-range error")
		}
	})
}

type issueRepositoryStub struct {
	issue domain.Issue
}

func (stub *issueRepositoryStub) CreateIssue(context.Context, ports.CreateIssueCommand) (domain.Issue, error) {
	return domain.Issue{}, nil
}

func (stub *issueRepositoryStub) LookupCreateIssue(context.Context, string, []byte) (domain.Issue, bool, error) {
	return domain.Issue{}, false, nil
}

func (stub *issueRepositoryStub) UpdateIssue(context.Context, ports.UpdateIssueCommand) (ports.UpdateIssueResult, error) {
	return ports.UpdateIssueResult{}, nil
}

func (stub *issueRepositoryStub) LookupUpdateIssue(context.Context, string, []byte) (ports.UpdateIssueResult, bool, error) {
	return ports.UpdateIssueResult{}, false, nil
}

func (stub *issueRepositoryStub) ArchiveIssue(context.Context, ports.ArchiveIssueCommand) (ports.ArchiveIssueResult, error) {
	return ports.ArchiveIssueResult{}, nil
}

func (stub *issueRepositoryStub) LookupArchiveIssue(context.Context, string, []byte) (ports.ArchiveIssueResult, bool, error) {
	return ports.ArchiveIssueResult{}, false, nil
}

func (stub *issueRepositoryStub) GetIssue(_ context.Context, identifier domain.IssueIdentifier) (domain.Issue, error) {
	return stub.issue, nil
}

func (stub *issueRepositoryStub) GetIssueProjection(_ context.Context, command ports.GetIssueProjectionCommand) (domain.IssueProjection, error) {
	return domain.IssueProjection{Issue: stub.issue}, nil
}

func (stub *issueRepositoryStub) ListLabels(context.Context, ports.ListLabelsCommand) (domain.LabelList, error) {
	return domain.LabelList{}, nil
}

func (stub *issueRepositoryStub) ListIssues(context.Context, ports.ListIssuesCommand) (domain.IssueList, error) {
	return domain.IssueList{}, nil
}

func (stub *issueRepositoryStub) CountIssuesByEffectiveStatus(context.Context, ports.CountIssuesByEffectiveStatusCommand) ([]domain.EffectiveStatusCount, error) {
	return nil, nil
}

type reviewRepositoryStub struct {
	listQuery      ports.ListReviewRequestsQuery
	createCommand  ports.CreateReviewRequestCommand
	request        domain.ReviewRequest
	replaceCommand ports.ReplaceReviewRequestCommand
	replaceResult  *ports.ReplaceReviewRequestResult
	replaceReplay  *ports.ReplaceReviewRequestResult
	replaceErr     error
}

func (stub *reviewRepositoryStub) CreateReviewRequest(_ context.Context, command ports.CreateReviewRequestCommand) (ports.CreateReviewRequestResult, error) {
	stub.createCommand = command
	stub.request = domain.ReviewRequest{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", IssueID: command.IssueID, TargetIssueVersion: command.TargetIssueVersion, TargetEventID: command.TargetEventID, ArtifactIDs: append([]string(nil), command.ArtifactIDs...), Status: domain.ReviewRequestStatusOpen, Version: 1}
	return ports.CreateReviewRequestResult{Request: stub.request}, nil
}

func (stub *reviewRepositoryStub) GetReviewRequest(_ context.Context, requestID string) (ports.GetReviewRequestResult, error) {
	return ports.GetReviewRequestResult{Request: stub.request}, nil
}

func (stub *reviewRepositoryStub) ListReviewRequests(_ context.Context, query ports.ListReviewRequestsQuery) (ports.ListReviewRequestsResult, error) {
	stub.listQuery = query
	return ports.ListReviewRequestsResult{Items: []domain.ReviewRequest{stub.request}, HasMore: false, NextOffset: 0}, nil
}

func (stub *reviewRepositoryStub) CancelReviewRequest(_ context.Context, command ports.ReviewMutationCommand) (ports.ReviewMutationResult, error) {
	stub.request.Status = domain.ReviewRequestStatusCancelled
	stub.request.Version = command.ExpectedVersion + 1
	return ports.ReviewMutationResult{Request: stub.request}, nil
}

func (stub *reviewRepositoryStub) ClaimReviewRequest(context.Context, ports.ReviewMutationCommand) (ports.ReviewMutationResult, error) {
	return ports.ReviewMutationResult{}, nil
}

func (stub *reviewRepositoryStub) ResolveReviewRequest(context.Context, ports.ResolveReviewRequestCommand) (ports.ResolveReviewRequestResult, error) {
	return ports.ResolveReviewRequestResult{}, nil
}

func (stub *reviewRepositoryStub) LookupReplaceReviewRequest(context.Context, string, []byte) (ports.ReplaceReviewRequestResult, bool, error) {
	if stub.replaceReplay != nil {
		return *stub.replaceReplay, true, nil
	}
	return ports.ReplaceReviewRequestResult{}, false, nil
}

func (stub *reviewRepositoryStub) ReplaceReviewRequest(_ context.Context, command ports.ReplaceReviewRequestCommand) (ports.ReplaceReviewRequestResult, error) {
	stub.replaceCommand = command
	if stub.replaceErr != nil {
		return ports.ReplaceReviewRequestResult{}, stub.replaceErr
	}
	predecessor := stub.request
	predecessor.Status = domain.ReviewRequestStatusSuperseded
	predecessor.Version = command.PredecessorExpectedVersion + 1
	successorID := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	supersedesID := predecessor.ID
	successor := domain.ReviewRequest{
		ID: successorID, IssueID: predecessor.IssueID, TargetIssueVersion: command.TargetIssueVersion,
		TargetEventID: command.TargetEventID, ArtifactIDs: append([]string(nil), command.ArtifactIDs...),
		Status: domain.ReviewRequestStatusOpen, SupersedesID: &supersedesID, Version: 1,
	}
	result := ports.ReplaceReviewRequestResult{Predecessor: predecessor, Successor: successor, LatestEventID: 42}
	stub.replaceResult = &result
	return result, nil
}

func stringPointer(value string) *string { return &value }
func boolPointer(value bool) *bool       { return &value }
