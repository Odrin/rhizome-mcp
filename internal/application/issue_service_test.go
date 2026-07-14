package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

type recordingIssueRepository struct {
	command       ports.CreateIssueCommand
	updateCommand ports.UpdateIssueCommand
	issue         domain.Issue
	identifier    domain.IssueIdentifier
	createCalled  bool
	updateCalled  bool
	getCalled     bool
}

func (repository *recordingIssueRepository) CreateIssue(_ context.Context, command ports.CreateIssueCommand) (domain.Issue, error) {
	repository.createCalled = true
	repository.command = command
	return domain.Issue{
		ID:         command.ID,
		DisplayID:  "ISSUE-7",
		SequenceNo: 7,
	}, nil
}

func (repository *recordingIssueRepository) UpdateIssue(_ context.Context, command ports.UpdateIssueCommand) (ports.UpdateIssueResult, error) {
	repository.updateCalled = true
	repository.updateCommand = command
	return ports.UpdateIssueResult{Issue: repository.issue, ChangedFields: []string{"title"}}, nil
}

func (repository *recordingIssueRepository) GetIssue(_ context.Context, identifier domain.IssueIdentifier) (domain.Issue, error) {
	repository.getCalled = true
	repository.identifier = identifier
	return repository.issue, nil
}

type fixedIDGenerator struct {
	id  string
	err error
}

func (generator fixedIDGenerator) New() (string, error) { return generator.id, generator.err }

func TestIssueServiceCreateIssueValidatesGeneratesAndDelegates(t *testing.T) {
	now := time.Date(2026, 7, 14, 8, 9, 10, 0, time.FixedZone("test", 2*60*60))
	repository := &recordingIssueRepository{}
	service, err := application.NewIssueService(repository, clock.NewFakeClock(now), fixedIDGenerator{id: "01ARZ3NDEKTSV4RRFFQ69G5FAV"})
	if err != nil {
		t.Fatalf("NewIssueService() error = %v", err)
	}

	result, err := service.CreateIssue(context.Background(), domain.CreateIssueInput{Type: domain.TypeTask, Title: "create"})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}
	if !repository.createCalled || repository.command.Input.Status != domain.StatusOpen || repository.command.Input.Priority != domain.PriorityMedium {
		t.Fatalf("delegated command = %#v", repository.command)
	}
	if !repository.command.CreatedAt.Equal(now.UTC()) {
		t.Fatalf("command time = %v, want %v", repository.command.CreatedAt, now.UTC())
	}
	if result.ID != repository.command.ID || result.DisplayID != "ISSUE-7" || result.SequenceNo != 7 {
		t.Fatalf("result = %#v", result)
	}
}

func TestIssueServiceCreateIssueReturnsStructuredGeneratorFailure(t *testing.T) {
	repository := &recordingIssueRepository{}
	service, err := application.NewIssueService(repository, clock.NewFakeClock(time.Unix(0, 0)), fixedIDGenerator{err: errors.New("entropy failed")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateIssue(context.Background(), domain.CreateIssueInput{Type: domain.TypeTask, Title: "create"})
	if !errors.Is(err, &domain.Error{Code: domain.CodeIDGeneration}) {
		t.Fatalf("CreateIssue() error = %v, want ID_GENERATION_FAILED", err)
	}
	if repository.createCalled {
		t.Fatal("repository called after ID generation failure")
	}
}

func TestIssueServiceGetIssueNormalizesBeforeDelegating(t *testing.T) {
	repository := &recordingIssueRepository{issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", DisplayID: "ISSUE-7"}}
	service, err := application.NewIssueService(repository, clock.NewFakeClock(time.Unix(0, 0)), fixedIDGenerator{id: "01ARZ3NDEKTSV4RRFFQ69G5FAV"})
	if err != nil {
		t.Fatal(err)
	}

	issue, err := service.GetIssue(context.Background(), "issue-7")
	if err != nil {
		t.Fatalf("GetIssue() error = %v", err)
	}
	if !repository.getCalled || repository.identifier.Kind != domain.IssueIdentifierDisplayID ||
		repository.identifier.Value != "ISSUE-7" || repository.identifier.SequenceNo != 7 {
		t.Fatalf("normalized identifier = %#v", repository.identifier)
	}
	if issue.DisplayID != "ISSUE-7" {
		t.Fatalf("issue = %#v", issue)
	}
}

func TestIssueServiceGetIssueRejectsInvalidIdentifierBeforeRepository(t *testing.T) {
	repository := &recordingIssueRepository{}
	service, err := application.NewIssueService(repository, clock.NewFakeClock(time.Unix(0, 0)), fixedIDGenerator{id: "01ARZ3NDEKTSV4RRFFQ69G5FAV"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.GetIssue(context.Background(), "42")
	if !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("GetIssue() error = %v, want INVALID_ARGUMENT", err)
	}
	if repository.getCalled {
		t.Fatal("repository called for invalid identifier")
	}
}
