package application

import (
	"context"
	"testing"
	"time"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

func TestReviewServiceCreatesAndMutatesRequests(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	issueRepository := &issueRepositoryStub{issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}}
	reviewRepository := &reviewRepositoryStub{}
	service, err := NewReviewService(reviewRepository, issueRepository, clock.NewFakeClock(now))
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

	superseded, err := service.SupersedeReviewRequest(context.Background(), ReviewMutationInput{RequestID: created.Request.ID, ExpectedVersion: created.Request.Version + 1})
	if err != nil {
		t.Fatalf("SupersedeReviewRequest() error = %v", err)
	}
	if superseded.Request.Status != domain.ReviewRequestStatusSuperseded || superseded.Claimable {
		t.Fatalf("SupersedeReviewRequest() = %#v", superseded)
	}
}

func TestReviewServiceListFiltersByStatusAndClaimability(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	service, err := NewReviewService(&reviewRepositoryStub{request: domain.ReviewRequest{ID: "req-1", Status: domain.ReviewRequestStatusApproved}}, &issueRepositoryStub{}, clock.NewFakeClock(now))
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

func (stub *issueRepositoryStub) ArchiveIssue(context.Context, ports.ArchiveIssueCommand) (ports.ArchiveIssueResult, error) {
	return ports.ArchiveIssueResult{}, nil
}

func (stub *issueRepositoryStub) GetIssue(_ context.Context, identifier domain.IssueIdentifier) (domain.Issue, error) {
	return stub.issue, nil
}

func (stub *issueRepositoryStub) ListLabels(context.Context, ports.ListLabelsCommand) (domain.LabelList, error) {
	return domain.LabelList{}, nil
}

func (stub *issueRepositoryStub) ListIssues(context.Context, ports.ListIssuesCommand) (domain.IssueList, error) {
	return domain.IssueList{}, nil
}

type reviewRepositoryStub struct {
	createCommand ports.CreateReviewRequestCommand
	request       domain.ReviewRequest
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
	return ports.ListReviewRequestsResult{Items: []domain.ReviewRequest{stub.request}, HasMore: false, NextOffset: 0}, nil
}

func (stub *reviewRepositoryStub) CancelReviewRequest(_ context.Context, command ports.ReviewMutationCommand) (ports.ReviewMutationResult, error) {
	stub.request.Status = domain.ReviewRequestStatusCancelled
	stub.request.Version = command.ExpectedVersion + 1
	return ports.ReviewMutationResult{Request: stub.request}, nil
}

func (stub *reviewRepositoryStub) SupersedeReviewRequest(_ context.Context, command ports.ReviewMutationCommand) (ports.ReviewMutationResult, error) {
	stub.request.Status = domain.ReviewRequestStatusSuperseded
	stub.request.Version = command.ExpectedVersion + 1
	return ports.ReviewMutationResult{Request: stub.request}, nil
}

func (stub *reviewRepositoryStub) ClaimReviewRequest(context.Context, ports.ReviewMutationCommand) (ports.ReviewMutationResult, error) {
	return ports.ReviewMutationResult{}, nil
}

func (stub *reviewRepositoryStub) ResolveReviewRequest(context.Context, ports.ResolveReviewRequestCommand) (ports.ResolveReviewRequestResult, error) {
	return ports.ResolveReviewRequestResult{}, nil
}

func stringPointer(value string) *string { return &value }
func boolPointer(value bool) *bool       { return &value }
