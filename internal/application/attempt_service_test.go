package application

import (
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"testing"
	"time"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

func TestAttemptServiceSaveNoteGeneratesIDHashesTokenAndUsesClock(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.FixedZone("test", 2*60*60))
	repository := &recordingAttemptRepository{}
	service, err := NewAttemptService(repository, clock.NewFakeClock(now), fixedAttemptIDGenerator("01ARZ3NDEKTSV4RRFFQ69G5FAX"))
	if err != nil {
		t.Fatal(err)
	}

	note, err := service.SaveAttemptNote(context.Background(), domain.SaveAttemptNoteInput{
		AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "opaque-token", Kind: domain.AttemptNoteKindFinding,
		Content: "finding", NextSteps: []string{"act"}, Important: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	expectedHash := sha256.Sum256([]byte("opaque-token"))
	if repository.command.NoteID != "01ARZ3NDEKTSV4RRFFQ69G5FAX" ||
		!reflect.DeepEqual(repository.command.TokenHash, expectedHash[:]) ||
		repository.command.OccurredAt.Location() != time.UTC ||
		!repository.command.OccurredAt.Equal(now.UTC()) ||
		note.ID != repository.command.NoteID {
		t.Fatalf("command = %#v, note = %#v", repository.command, note)
	}
}

func TestAttemptServiceSaveNoteRejectsInvalidInputBeforeRepository(t *testing.T) {
	repository := &recordingAttemptRepository{}
	service, err := NewAttemptService(repository, clock.NewFakeClock(time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)), fixedAttemptIDGenerator("01ARZ3NDEKTSV4RRFFQ69G5FAX"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.SaveAttemptNote(context.Background(), domain.SaveAttemptNoteInput{
		AttemptID: "bad", LeaseToken: "token", Kind: domain.AttemptNoteKindProgress, Content: "note",
	})
	if !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) || repository.called {
		t.Fatalf("error = %v, repository called = %t", err, repository.called)
	}
}

func TestAttemptServiceFinishHashesTokenAndUsesUTCClock(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.FixedZone("test", 2*60*60))
	repository := &recordingAttemptRepository{}
	service, err := NewAttemptService(repository, clock.NewFakeClock(now), fixedAttemptIDGenerator("01ARZ3NDEKTSV4RRFFQ69G5FAX"))
	if err != nil {
		t.Fatal(err)
	}
	summary := "summary"
	result, err := service.FinishAttempt(context.Background(), domain.FinishAttemptInput{
		AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "opaque-token",
		Outcome: domain.AttemptOutcomeFailed, ResultSummary: summary,
		FailureReasonCode: failureReasonPointer(domain.FailureReasonOther),
	})
	if err != nil {
		t.Fatal(err)
	}
	expectedHash := sha256.Sum256([]byte("opaque-token"))
	if !reflect.DeepEqual(repository.finishCommand.TokenHash, expectedHash[:]) ||
		repository.finishCommand.AttemptID != "01ARZ3NDEKTSV4RRFFQ69G5FAV" ||
		!repository.finishCommand.OccurredAt.Equal(now.UTC()) || repository.finishCommand.OccurredAt.Location() != time.UTC ||
		repository.finishCommand.Input.ResultSummary != summary || result.LatestEventID != repository.finishResult.LatestEventID {
		t.Fatalf("finish command = %#v, result = %#v", repository.finishCommand, result)
	}
}

func TestAttemptServiceFinishRejectsInvalidInputBeforeRepository(t *testing.T) {
	repository := &recordingAttemptRepository{}
	service, err := NewAttemptService(repository, clock.NewFakeClock(time.Now()), fixedAttemptIDGenerator("01ARZ3NDEKTSV4RRFFQ69G5FAX"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.FinishAttempt(context.Background(), domain.FinishAttemptInput{AttemptID: "bad", LeaseToken: "token", Outcome: domain.AttemptOutcomeFailed, ResultSummary: "summary"})
	if !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) || repository.finishCalled {
		t.Fatalf("error = %v, repository called = %t", err, repository.finishCalled)
	}
}

type recordingAttemptRepository struct {
	command       ports.SaveAttemptNoteCommand
	finishCommand ports.FinishAttemptCommand
	finishResult  ports.FinishAttemptResult
	called        bool
	finishCalled  bool
}

func (repository *recordingAttemptRepository) ClaimIssue(context.Context, ports.ClaimIssueCommand) (ports.ClaimIssueResult, error) {
	return ports.ClaimIssueResult{}, nil
}

func (repository *recordingAttemptRepository) RenewAttempt(context.Context, ports.RenewAttemptCommand) (ports.RenewAttemptResult, error) {
	return ports.RenewAttemptResult{}, nil
}

func (repository *recordingAttemptRepository) SaveAttemptNote(_ context.Context, command ports.SaveAttemptNoteCommand) (ports.SaveAttemptNoteResult, error) {
	repository.called = true
	repository.command = command
	return ports.SaveAttemptNoteResult{Note: domain.AttemptNote{ID: command.NoteID}}, nil
}

func (repository *recordingAttemptRepository) FinishAttempt(_ context.Context, command ports.FinishAttemptCommand) (ports.FinishAttemptResult, error) {
	repository.finishCalled = true
	repository.finishCommand = command
	repository.finishResult = ports.FinishAttemptResult{LatestEventID: 7}
	return repository.finishResult, nil
}

func failureReasonPointer(value domain.FailureReasonCode) *domain.FailureReasonCode { return &value }

type fixedAttemptIDGenerator string

func (generator fixedAttemptIDGenerator) New() (string, error) { return string(generator), nil }
