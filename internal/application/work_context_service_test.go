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

type recordingWorkContextRepository struct {
	command ports.GetWorkContextCommand
	result  domain.WorkContext
	err     error
	called  bool
}

func (repository *recordingWorkContextRepository) GetWorkContext(_ context.Context, command ports.GetWorkContextCommand) (domain.WorkContext, error) {
	repository.called = true
	repository.command = command
	return repository.result, repository.err
}

func TestNewWorkContextServiceRejectsMissingRepositoryAndClock(t *testing.T) {
	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	fakeClock := clock.NewFakeClock(now)

	tests := []struct {
		name       string
		repository ports.WorkContextRepository
		source     clock.Clock
	}{
		{name: "missing repository", source: fakeClock},
		{name: "missing clock", repository: &recordingWorkContextRepository{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := application.NewWorkContextService(tt.repository, tt.source); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
				t.Fatalf("NewWorkContextService() error = %v", err)
			}
		})
	}
}

func TestWorkContextServiceNormalizesDelegatesAndClonesResult(t *testing.T) {
	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	repository := &recordingWorkContextRepository{result: domain.WorkContext{
		Warnings: []string{"warn"},
		RecentComments: []domain.Comment{{
			Content:            "hello",
			CreatedBySessionID: workContextStringPointer("session"),
			AuthorLabel:        workContextStringPointer("agent"),
		}},
		ChangesSincePreviousAttempt: []domain.IssueEvent{{Payload: []byte(`{"x":1}`)}},
	}}
	service, err := application.NewWorkContextService(repository, clock.NewFakeClock(now))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.GetWorkContext(context.Background(), domain.GetWorkContextInput{
		IssueID: "issue-7",
		Include: []domain.WorkContextInclude{domain.WorkContextIncludeRecentComments},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !repository.called || repository.command.Input.IssueID != "ISSUE-7" ||
		len(repository.command.Input.Include) != 1 || repository.command.Input.Include[0] != domain.WorkContextIncludeRecentComments ||
		repository.command.Input.Limits[domain.WorkContextIncludeRecentComments] != domain.DefaultWorkContextRecentCommentLimit ||
		!repository.command.Now.Equal(now) || repository.command.Now.Location() != time.UTC {
		t.Fatalf("command = %#v", repository.command)
	}
	result.Warnings[0] = "changed"
	result.RecentComments[0].Content = "mutated"
	*result.RecentComments[0].CreatedBySessionID = "changed"
	result.ChangesSincePreviousAttempt[0].Payload[0] = '['
	if repository.result.Warnings[0] != "warn" || repository.result.RecentComments[0].Content != "hello" ||
		repository.result.RecentComments[0].CreatedBySessionID == nil ||
		*repository.result.RecentComments[0].CreatedBySessionID != "session" ||
		string(repository.result.ChangesSincePreviousAttempt[0].Payload) != `{"x":1}` {
		t.Fatal("service returned repository-owned mutable data")
	}
}

func TestWorkContextServiceDoesNotCallRepositoryForInvalidInput(t *testing.T) {
	repository := &recordingWorkContextRepository{}
	service, err := application.NewWorkContextService(repository, clock.NewFakeClock(time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.GetWorkContext(context.Background(), domain.GetWorkContextInput{IssueID: "bad"})
	if err == nil || repository.called {
		t.Fatalf("error = %v, repository called = %v", err, repository.called)
	}
}

func TestWorkContextServicePropagatesRepositoryErrors(t *testing.T) {
	expected := errors.New("read failed")
	repository := &recordingWorkContextRepository{err: expected}
	service, err := application.NewWorkContextService(repository, clock.NewFakeClock(time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.GetWorkContext(context.Background(), domain.GetWorkContextInput{IssueID: "ISSUE-1"})
	if !errors.Is(err, expected) {
		t.Fatalf("GetWorkContext() error = %v, want %v", err, expected)
	}
}

func workContextStringPointer(value string) *string { return &value }
