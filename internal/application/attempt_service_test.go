package application

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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

	result, err := service.SaveAttemptNote(context.Background(), domain.SaveAttemptNoteInput{
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
		result.Note.ID != repository.command.NoteID {
		t.Fatalf("command = %#v, result = %#v", repository.command, result)
	}
}

func TestAttemptServiceExpireAttemptsUsesUTCClock(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.FixedZone("test", 2*60*60))
	repository := &recordingAttemptRepository{
		expireResult: ports.ExpireAttemptsResult{ExpiredAttemptCount: 3},
	}
	service, err := NewAttemptService(repository, clock.NewFakeClock(now), fixedAttemptIDGenerator("01ARZ3NDEKTSV4RRFFQ69G5FAX"))
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.ExpireAttempts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !repository.expireCalled ||
		!repository.expireCommand.OccurredAt.Equal(now.UTC()) ||
		repository.expireCommand.OccurredAt.Location() != time.UTC ||
		result.ExpiredAttemptCount != 3 {
		t.Fatalf("cleanup command = %#v, result = %#v", repository.expireCommand, result)
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

func TestAttemptServiceSaveNoteGeneratesAndPropagatesArtifacts(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.FixedZone("test", 2*60*60))
	repository := &recordingAttemptRepository{}
	generator := sequenceAttemptIDGenerator{ids: []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAX",
		"01ARZ3NDEKTSV4RRFFQ69G5FAY",
		"01ARZ3NDEKTSV4RRFFQ69G5FAZ",
	}}
	service, err := NewAttemptService(repository, clock.NewFakeClock(now), &generator)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.SaveAttemptNote(context.Background(), domain.SaveAttemptNoteInput{
		AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token",
		Kind: domain.AttemptNoteKindCheckpoint, Content: "checkpoint",
		Artifacts: []domain.ArtifactInput{
			{Type: domain.ArtifactTypeFile, URI: "internal/application/attempt_service.go", Metadata: json.RawMessage(`{ "language": "go" }`)},
			{Type: domain.ArtifactTypeURL, URI: "https://example.invalid/build/42"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.command.Artifacts) != 2 || len(result.Artifacts) != 0 ||
		repository.command.Artifacts[0].ID != generator.ids[1] ||
		repository.command.Artifacts[1].ID != generator.ids[2] ||
		repository.command.Artifacts[0].IssueID != "" || repository.command.Artifacts[0].AttemptID != nil ||
		string(repository.command.Artifacts[0].Metadata) != `{"language":"go"}` ||
		!repository.command.Artifacts[0].CreatedAt.Equal(now.UTC()) ||
		repository.command.OccurredAt.Location() != time.UTC || !repository.command.OccurredAt.Equal(now.UTC()) {
		t.Fatalf("command = %#v, result = %#v", repository.command, result)
	}
}

func TestAttemptServiceSaveNoteRejectsInvalidGeneratedArtifactIDBeforeRepository(t *testing.T) {
	repository := &recordingAttemptRepository{}
	service, err := NewAttemptService(repository, clock.NewFakeClock(time.Now()), &sequenceAttemptIDGenerator{ids: []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAX", "bad",
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SaveAttemptNote(context.Background(), domain.SaveAttemptNoteInput{
		AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token",
		Kind: domain.AttemptNoteKindProgress, Content: "note",
		Artifacts: []domain.ArtifactInput{{Type: domain.ArtifactTypeOther, URI: "ref"}},
	})
	if !errors.Is(err, &domain.Error{Code: domain.CodeIDGeneration}) || repository.called {
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

func TestAttemptServicePropagatesSessionIDsAndRejectsInvalidOnes(t *testing.T) {
	sessionID := "01BX5ZZKBKACTAV9WEVGEMMVRZ"
	repository := &recordingAttemptRepository{}
	generator := sequenceAttemptIDGenerator{ids: []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAX",
		"01ARZ3NDEKTSV4RRFFQ69G5FAY",
	}}
	service, err := NewAttemptService(repository, clock.NewFakeClock(time.Now()), &generator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ClaimIssue(context.Background(), domain.ClaimIssueInput{IssueID: "ISSUE-1", SessionID: &sessionID}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RenewAttempt(context.Background(), domain.RenewAttemptInput{
		AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token", SessionID: &sessionID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveAttemptNote(context.Background(), domain.SaveAttemptNoteInput{
		AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token",
		Kind: domain.AttemptNoteKindProgress, Content: "note", SessionID: &sessionID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.FinishAttempt(context.Background(), domain.FinishAttemptInput{
		AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token",
		Outcome: domain.AttemptOutcomeFailed, ResultSummary: "failed",
		FailureReasonCode: failureReasonPointer(domain.FailureReasonOther), SessionID: &sessionID,
	}); err != nil {
		t.Fatal(err)
	}
	sessionID = "01BX5ZZKBKACTAV9WEVGEMMVS0"
	for name, value := range map[string]*string{
		"claim": repository.claimCommand.SessionID, "renew": repository.renewCommand.SessionID,
		"note": repository.command.SessionID, "finish": repository.finishCommand.SessionID,
	} {
		if value == nil || *value != "01BX5ZZKBKACTAV9WEVGEMMVRZ" {
			t.Fatalf("%s command session = %#v", name, value)
		}
	}
	invalid := "bad"
	repository.claimCalled, repository.renewCalled, repository.called, repository.finishCalled = false, false, false, false
	if _, err := service.ClaimIssue(context.Background(), domain.ClaimIssueInput{IssueID: "ISSUE-1", SessionID: &invalid}); err == nil || repository.claimCalled {
		t.Fatalf("invalid claim session error = %v, called = %t", err, repository.claimCalled)
	}
	if _, err := service.RenewAttempt(context.Background(), domain.RenewAttemptInput{
		AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token", SessionID: &invalid,
	}); err == nil || repository.renewCalled {
		t.Fatalf("invalid renew session error = %v, called = %t", err, repository.renewCalled)
	}
	if _, err := service.SaveAttemptNote(context.Background(), domain.SaveAttemptNoteInput{
		AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token",
		Kind: domain.AttemptNoteKindProgress, Content: "note", SessionID: &invalid,
	}); err == nil || repository.called {
		t.Fatalf("invalid note session error = %v, called = %t", err, repository.called)
	}
	if _, err := service.FinishAttempt(context.Background(), domain.FinishAttemptInput{
		AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token",
		Outcome: domain.AttemptOutcomeFailed, ResultSummary: "failed",
		FailureReasonCode: failureReasonPointer(domain.FailureReasonOther), SessionID: &invalid,
	}); err == nil || repository.finishCalled {
		t.Fatalf("invalid finish session error = %v, called = %t", err, repository.finishCalled)
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

func TestAttemptServiceFinishGeneratesAndPropagatesArtifacts(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.FixedZone("test", 2*60*60))
	repository := &recordingAttemptRepository{}
	generator := sequenceAttemptIDGenerator{ids: []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAY",
		"01ARZ3NDEKTSV4RRFFQ69G5FAZ",
	}}
	service, err := NewAttemptService(repository, clock.NewFakeClock(now), &generator)
	if err != nil {
		t.Fatal(err)
	}
	title := "result"
	metadata := json.RawMessage(`{ "kind": "result" }`)
	target := domain.StatusDone
	_, err = service.FinishAttempt(context.Background(), domain.FinishAttemptInput{
		AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token",
		Outcome: domain.AttemptOutcomeCompleted, ResultSummary: "summary", TargetIssueStatus: &target,
		Artifacts: []domain.ArtifactInput{
			{Type: domain.ArtifactTypeFile, URI: "build/result.txt", Title: &title, Metadata: metadata},
			{Type: domain.ArtifactTypeURL, URI: "https://example.invalid/build/42"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	expectedHash := sha256.Sum256([]byte("token"))
	if !reflect.DeepEqual(repository.finishCommand.TokenHash, expectedHash[:]) ||
		len(repository.finishCommand.Artifacts) != 2 ||
		repository.finishCommand.Artifacts[0].ID != generator.ids[0] ||
		repository.finishCommand.Artifacts[1].ID != generator.ids[1] ||
		repository.finishCommand.Artifacts[0].IssueID != "" || repository.finishCommand.Artifacts[0].AttemptID != nil ||
		string(repository.finishCommand.Artifacts[0].Metadata) != `{"kind":"result"}` ||
		!repository.finishCommand.Artifacts[0].CreatedAt.Equal(now.UTC()) ||
		!repository.finishCommand.OccurredAt.Equal(now.UTC()) || repository.finishCommand.OccurredAt.Location() != time.UTC {
		t.Fatalf("finish command = %#v", repository.finishCommand)
	}
	title = "changed"
	metadata[2] = 'x'
	if *repository.finishCommand.Artifacts[0].Title != "result" ||
		string(repository.finishCommand.Artifacts[0].Metadata) != `{"kind":"result"}` {
		t.Fatal("finish artifact propagation was not defensive")
	}
}

func TestAttemptServiceFinishRejectsInvalidGeneratedArtifactIDBeforeRepository(t *testing.T) {
	repository := &recordingAttemptRepository{}
	service, err := NewAttemptService(repository, clock.NewFakeClock(time.Now()), &sequenceAttemptIDGenerator{ids: []string{"bad"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.FinishAttempt(context.Background(), domain.FinishAttemptInput{
		AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token",
		Outcome: domain.AttemptOutcomeFailed, ResultSummary: "summary",
		FailureReasonCode: failureReasonPointer(domain.FailureReasonOther),
		Artifacts:         []domain.ArtifactInput{{Type: domain.ArtifactTypeOther, URI: "result"}},
	})
	if !errors.Is(err, &domain.Error{Code: domain.CodeIDGeneration}) || repository.finishCalled {
		t.Fatalf("error = %v, repository called = %t", err, repository.finishCalled)
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || len(domainErr.Details) != 1 ||
		domainErr.Details[0].Field != "artifacts[0].id" {
		t.Fatalf("generated ID details = %#v", err)
	}
}

type recordingAttemptRepository struct {
	claimCommand  ports.ClaimIssueCommand
	renewCommand  ports.RenewAttemptCommand
	command       ports.SaveAttemptNoteCommand
	finishCommand ports.FinishAttemptCommand
	finishResult  ports.FinishAttemptResult
	expireCommand ports.ExpireAttemptsCommand
	expireResult  ports.ExpireAttemptsResult
	called        bool
	claimCalled   bool
	renewCalled   bool
	finishCalled  bool
	expireCalled  bool
	lookupCalled  bool
	lookupKey     string
	lookupHash    []byte
	lookupResult  ports.FinishAttemptResult
	lookupFound   bool
	lookupError   error
}

func (repository *recordingAttemptRepository) ClaimIssue(_ context.Context, command ports.ClaimIssueCommand) (ports.ClaimIssueResult, error) {
	repository.claimCalled = true
	repository.claimCommand = command
	return ports.ClaimIssueResult{}, nil
}

func (repository *recordingAttemptRepository) RenewAttempt(_ context.Context, command ports.RenewAttemptCommand) (ports.RenewAttemptResult, error) {
	repository.renewCalled = true
	repository.renewCommand = command
	return ports.RenewAttemptResult{}, nil
}

func (repository *recordingAttemptRepository) ExpireAttempts(_ context.Context, command ports.ExpireAttemptsCommand) (ports.ExpireAttemptsResult, error) {
	repository.expireCalled = true
	repository.expireCommand = command
	return repository.expireResult, nil
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

func (repository *recordingAttemptRepository) LookupFinishedAttempt(_ context.Context, key string, hash []byte) (ports.FinishAttemptResult, bool, error) {
	repository.lookupCalled = true
	repository.lookupKey = key
	repository.lookupHash = append([]byte(nil), hash...)
	return repository.lookupResult, repository.lookupFound, repository.lookupError
}

func failureReasonPointer(value domain.FailureReasonCode) *domain.FailureReasonCode { return &value }

type fixedAttemptIDGenerator string

func (generator fixedAttemptIDGenerator) New() (string, error) { return string(generator), nil }

type sequenceAttemptIDGenerator struct {
	ids  []string
	next int
}

func (generator *sequenceAttemptIDGenerator) New() (string, error) {
	id := generator.ids[generator.next]
	generator.next++
	return id, nil
}

func TestAttemptServiceFinishLooksUpReplayBeforeAllocatingIDs(t *testing.T) {
	repository := &recordingAttemptRepository{
		lookupFound:  true,
		lookupResult: ports.FinishAttemptResult{LatestEventID: 19},
	}
	service, err := NewAttemptService(repository, clock.NewFakeClock(time.Now()), &sequenceAttemptIDGenerator{})
	if err != nil {
		t.Fatal(err)
	}
	key := "  finish-retry "
	input := domain.FinishAttemptInput{
		AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token",
		Outcome: domain.AttemptOutcomeFailed, ResultSummary: "failed",
		FailureReasonCode: failureReasonPointer(domain.FailureReasonOther), IdempotencyKey: &key,
	}
	result, err := service.FinishAttempt(context.Background(), input)
	if err != nil || result.LatestEventID != 19 || !repository.lookupCalled || repository.finishCalled {
		t.Fatalf("result = %#v, err = %v, repository = %#v", result, err, repository)
	}
	normalized, err := input.Validate()
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := domain.CanonicalFinishAttemptRequest(normalized)
	if err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256(canonical)
	if repository.lookupKey != "finish-retry" || !reflect.DeepEqual(repository.lookupHash, expected[:]) {
		t.Fatalf("lookup = %q/%x, want %q/%x", repository.lookupKey, repository.lookupHash, "finish-retry", expected)
	}
}

func TestAttemptServiceFinishForwardsIdempotencyAndPropagatesLookupError(t *testing.T) {
	repository := &recordingAttemptRepository{lookupError: errors.New("lookup failed")}
	service, err := NewAttemptService(repository, clock.NewFakeClock(time.Now()), fixedAttemptIDGenerator("01ARZ3NDEKTSV4RRFFQ69G5FAX"))
	if err != nil {
		t.Fatal(err)
	}
	key := "finish-key"
	_, err = service.FinishAttempt(context.Background(), domain.FinishAttemptInput{
		AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: "token",
		Outcome: domain.AttemptOutcomeFailed, ResultSummary: "failed",
		FailureReasonCode: failureReasonPointer(domain.FailureReasonOther), IdempotencyKey: &key,
	})
	if err == nil || err.Error() != "lookup failed" || repository.finishCalled {
		t.Fatalf("lookup error = %v, finish called = %t", err, repository.finishCalled)
	}
}
