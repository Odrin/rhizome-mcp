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

type recordingCommentRepository struct {
	command ports.AddCommentCommand
	called  bool
}

func (repository *recordingCommentRepository) AddComment(_ context.Context, command ports.AddCommentCommand) (domain.Comment, error) {
	repository.called = true
	repository.command = command
	return domain.Comment{ID: command.ID, IssueID: command.Input.IssueID, Content: command.Input.Content, CreatedAt: command.OccurredAt}, nil
}

func (repository *recordingCommentRepository) LookupAddComment(context.Context, string, []byte) (domain.Comment, bool, error) {
	return domain.Comment{}, false, nil
}

func TestCommentServiceValidatesAndBuildsCommand(t *testing.T) {
	now := time.Date(2026, 7, 14, 8, 9, 10, 0, time.FixedZone("test", 2*60*60))
	repository := &recordingCommentRepository{}
	service, err := application.NewCommentService(repository, clock.NewFakeClock(now), fixedIDGenerator{id: "01ARZ3NDEKTSV4RRFFQ69G5FAV"})
	if err != nil {
		t.Fatalf("NewCommentService() error = %v", err)
	}
	session := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	comment, err := service.AddComment(context.Background(), domain.AddCommentInput{
		IssueID: "issue-7", Content: "  body  ", SessionID: &session,
	})
	if err != nil {
		t.Fatalf("AddComment() error = %v", err)
	}
	if !repository.called || repository.command.ID != comment.ID || repository.command.Input.IssueID != "ISSUE-7" ||
		repository.command.Input.Content != "  body  " || repository.command.Input.SessionID == &session ||
		!repository.command.OccurredAt.Equal(now.UTC()) {
		t.Fatalf("command = %#v", repository.command)
	}
}

func TestCommentServiceDoesNotGenerateIDForInvalidInput(t *testing.T) {
	repository := &recordingCommentRepository{}
	generator := &countingCommentGenerator{}
	service, err := application.NewCommentService(repository, clock.NewFakeClock(time.Unix(1, 0)), generator)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.AddComment(context.Background(), domain.AddCommentInput{IssueID: "bad", Content: "body"})
	if err == nil || generator.calls != 0 || repository.called {
		t.Fatalf("error = %v, generator calls = %d, repository called = %v", err, generator.calls, repository.called)
	}
}

func TestCommentServiceMapsIDGenerationFailures(t *testing.T) {
	repository := &recordingCommentRepository{}
	service, err := application.NewCommentService(repository, clock.NewFakeClock(time.Unix(1, 0)),
		fixedIDGenerator{err: errors.New("entropy failed")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.AddComment(context.Background(), domain.AddCommentInput{IssueID: "ISSUE-1", Content: "body"})
	if !errors.Is(err, &domain.Error{Code: domain.CodeIDGeneration}) || repository.called {
		t.Fatalf("AddComment() error = %v, repository called = %v", err, repository.called)
	}
}

func TestNewCommentServiceRejectsMissingDependencies(t *testing.T) {
	generator := fixedIDGenerator{id: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}
	clockSource := clock.NewFakeClock(time.Unix(1, 0))
	for _, test := range []struct {
		name  string
		repo  ports.CommentRepository
		clock clock.Clock
		ids   application.IDGenerator
	}{
		{"repository", nil, clockSource, generator},
		{"clock", &recordingCommentRepository{}, nil, generator},
		{"generator", &recordingCommentRepository{}, clockSource, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := application.NewCommentService(test.repo, test.clock, test.ids); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
				t.Fatalf("NewCommentService() error = %v", err)
			}
		})
	}
}

type countingCommentGenerator struct{ calls int }

func (generator *countingCommentGenerator) New() (string, error) {
	generator.calls++
	return "01ARZ3NDEKTSV4RRFFQ69G5FAV", nil
}
