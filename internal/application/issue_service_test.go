package application_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

type recordingIssueRepository struct {
	command        ports.CreateIssueCommand
	updateCommand  ports.UpdateIssueCommand
	archiveCommand ports.ArchiveIssueCommand
	listCommand    ports.ListIssuesCommand
	issue          domain.Issue
	identifier     domain.IssueIdentifier
	createCalled   bool
	updateCalled   bool
	archiveCalled  bool
	listCalled     bool
	getCalled      bool
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

func (repository *recordingIssueRepository) ArchiveIssue(_ context.Context, command ports.ArchiveIssueCommand) (ports.ArchiveIssueResult, error) {
	repository.archiveCalled = true
	repository.archiveCommand = command
	return ports.ArchiveIssueResult{Issue: repository.issue}, nil
}

func (repository *recordingIssueRepository) GetIssue(_ context.Context, identifier domain.IssueIdentifier) (domain.Issue, error) {
	repository.getCalled = true
	repository.identifier = identifier
	return repository.issue, nil
}

func (repository *recordingIssueRepository) ListLabels(_ context.Context, _ ports.ListLabelsCommand) (domain.LabelList, error) {
	return domain.LabelList{}, nil
}

func (repository *recordingIssueRepository) ListIssues(_ context.Context, command ports.ListIssuesCommand) (domain.IssueList, error) {
	repository.listCalled = true
	repository.listCommand = command
	return domain.IssueList{}, nil
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

func TestIssueServiceListIssuesValidatesCopiesAndDelegates(t *testing.T) {
	repository := &recordingIssueRepository{}
	service, err := application.NewIssueService(repository, clock.NewFakeClock(time.Unix(0, 0)),
		fixedIDGenerator{id: "01ARZ3NDEKTSV4RRFFQ69G5FAV"})
	if err != nil {
		t.Fatal(err)
	}
	labels := []string{" Zebra ", "ALPHA", "alpha"}
	if _, err := service.ListIssues(context.Background(), domain.ListIssuesInput{Labels: labels}); err != nil {
		t.Fatal(err)
	}
	labels[0] = "changed"
	if !repository.listCalled || repository.listCommand.Input.Limit != 20 ||
		!reflect.DeepEqual(repository.listCommand.Input.Labels, []string{"ALPHA", "Zebra"}) {
		t.Fatalf("list command = %#v", repository.listCommand)
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

func TestIssueServiceArchiveIssueNormalizesAndDelegates(t *testing.T) {
	now := time.Date(2026, 7, 14, 8, 9, 10, 0, time.FixedZone("test", 2*60*60))
	repository := &recordingIssueRepository{issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Version: 2}}
	service, err := application.NewIssueService(repository, clock.NewFakeClock(now), fixedIDGenerator{id: "01ARZ3NDEKTSV4RRFFQ69G5FAV"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ArchiveIssue(context.Background(), domain.ArchiveIssueInput{
		IssueID: "issue-7", ExpectedVersion: 2,
	})
	if err != nil {
		t.Fatalf("ArchiveIssue() error = %v", err)
	}
	if !repository.archiveCalled ||
		repository.archiveCommand.Identifier.Kind != domain.IssueIdentifierDisplayID ||
		repository.archiveCommand.Identifier.Value != "ISSUE-7" ||
		repository.archiveCommand.ExpectedVersion != 2 ||
		!repository.archiveCommand.ArchivedAt.Equal(now.UTC()) {
		t.Fatalf("archive command = %#v", repository.archiveCommand)
	}
}

func TestIssueServiceArchiveIssueRejectsInvalidInputBeforeRepository(t *testing.T) {
	tests := []domain.ArchiveIssueInput{
		{IssueID: "", ExpectedVersion: 1},
		{IssueID: "not-an-issue", ExpectedVersion: 1},
		{IssueID: "ISSUE-1", ExpectedVersion: 0},
	}
	for _, input := range tests {
		repository := &recordingIssueRepository{}
		service, err := application.NewIssueService(repository, clock.NewFakeClock(time.Unix(0, 0)), fixedIDGenerator{id: "01ARZ3NDEKTSV4RRFFQ69G5FAV"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = service.ArchiveIssue(context.Background(), input)
		var domainErr *domain.Error
		if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeValidationError {
			t.Fatalf("ArchiveIssue(%#v) error = %v, want VALIDATION_ERROR", input, err)
		}
		if repository.archiveCalled {
			t.Fatal("repository called for invalid archive input")
		}
	}
}
