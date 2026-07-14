package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

type recordingActivityRepository struct {
	command ports.GetIssueActivityCommand
	result  domain.IssueActivity
	err     error
	called  bool
}

func (repository *recordingActivityRepository) GetIssueActivity(_ context.Context, command ports.GetIssueActivityCommand) (domain.IssueActivity, error) {
	repository.called = true
	repository.command = command
	return repository.result, repository.err
}

func TestNewActivityServiceRejectsMissingRepository(t *testing.T) {
	if _, err := application.NewActivityService(nil); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("NewActivityService() error = %v", err)
	}
}

func TestActivityServiceNormalizesDelegatesAndClonesResult(t *testing.T) {
	now := time.Date(2026, 7, 14, 17, 0, 0, 0, time.UTC)
	repository := &recordingActivityRepository{result: domain.IssueActivity{
		Items: []domain.ActivityItem{{
			EntityType: domain.ActivityEntityTypeEvent, EntityID: "7",
			IssueID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", OccurredAt: now,
			Event: &domain.IssueEvent{ID: 7, IssueID: stringPointer("01ARZ3NDEKTSV4RRFFQ69G5FAV"),
				Payload: []byte(`{"x":1}`), CreatedAt: now},
		}},
		NextCursor: stringPointer("next"),
	}}
	service, err := application.NewActivityService(repository)
	if err != nil {
		t.Fatal(err)
	}
	types := []domain.ActivityCategory{domain.ActivityCategoryEvents}
	result, err := service.GetIssueActivity(context.Background(), domain.GetIssueActivityInput{
		IssueID: "issue-7", Types: types, Cursor: "opaque",
	})
	if err != nil {
		t.Fatal(err)
	}
	types[0] = domain.ActivityCategoryComments
	if !repository.called || repository.command.Input.IssueID != "ISSUE-7" ||
		repository.command.Input.Limit != 20 ||
		repository.command.Input.Order != domain.ActivityOrderNewestFirst ||
		repository.command.Input.Types[0] != domain.ActivityCategoryEvents {
		t.Fatalf("command = %#v", repository.command)
	}
	result.Items[0].Event.Payload[0] = '['
	*result.NextCursor = "changed"
	if string(repository.result.Items[0].Event.Payload) != `{"x":1}` || *repository.result.NextCursor != "next" {
		t.Fatal("service returned repository-owned mutable data")
	}
}

func TestActivityServiceDoesNotCallRepositoryForInvalidInput(t *testing.T) {
	repository := &recordingActivityRepository{}
	service, err := application.NewActivityService(repository)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.GetIssueActivity(context.Background(), domain.GetIssueActivityInput{IssueID: "bad"})
	if err == nil || repository.called {
		t.Fatalf("error = %v, repository called = %v", err, repository.called)
	}
}

func TestActivityServicePropagatesRepositoryErrors(t *testing.T) {
	expected := errors.New("read failed")
	repository := &recordingActivityRepository{err: expected}
	service, err := application.NewActivityService(repository)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.GetIssueActivity(context.Background(), domain.GetIssueActivityInput{IssueID: "ISSUE-1"})
	if !errors.Is(err, expected) {
		t.Fatalf("GetIssueActivity() error = %v, want %v", err, expected)
	}
}

func stringPointer(value string) *string { return &value }
