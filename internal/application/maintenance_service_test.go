package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

func TestMaintenanceServiceForceReleaseAttemptUsesUTCClock(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.FixedZone("test", 2*60*60))
	attemptRepository := &recordingMaintenanceAttemptRepository{forceReleaseResult: ports.ForceReleaseAttemptResult{Attempt: domain.WorkAttempt{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAX"}, LatestEventID: 42}}
	searchIndexRepository := &recordingMaintenanceSearchIndexRepository{}
	service, err := NewMaintenanceService(attemptRepository, searchIndexRepository, clock.NewFakeClock(now))
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.ForceReleaseAttempt(context.Background(), "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err != nil {
		t.Fatal(err)
	}
	if !attemptRepository.forceReleaseCalled || !attemptRepository.forceReleaseCommand.OccurredAt.Equal(now.UTC()) || attemptRepository.forceReleaseCommand.OccurredAt.Location() != time.UTC {
		t.Fatalf("force release command = %#v", attemptRepository.forceReleaseCommand)
	}
	if result.LatestEventID != 42 {
		t.Fatalf("result = %#v", result)
	}
}

func TestMaintenanceServiceForceReleaseAttemptRejectsInvalidIDBeforeRepository(t *testing.T) {
	attemptRepository := &recordingMaintenanceAttemptRepository{}
	searchIndexRepository := &recordingMaintenanceSearchIndexRepository{}
	service, err := NewMaintenanceService(attemptRepository, searchIndexRepository, clock.NewFakeClock(time.Now()))
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.ForceReleaseAttempt(context.Background(), "bad")
	if !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) || attemptRepository.forceReleaseCalled {
		t.Fatalf("error = %v, repository called = %t", err, attemptRepository.forceReleaseCalled)
	}
}

func TestMaintenanceServiceRebuildSearchIndexDelegates(t *testing.T) {
	attemptRepository := &recordingMaintenanceAttemptRepository{}
	searchIndexRepository := &recordingMaintenanceSearchIndexRepository{}
	service, err := NewMaintenanceService(attemptRepository, searchIndexRepository, clock.NewFakeClock(time.Now()))
	if err != nil {
		t.Fatal(err)
	}

	if err := service.RebuildSearchIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !searchIndexRepository.rebuildCalled {
		t.Fatal("expected rebuild to be delegated")
	}
}

type recordingMaintenanceAttemptRepository struct {
	forceReleaseCommand ports.ForceReleaseAttemptCommand
	forceReleaseResult  ports.ForceReleaseAttemptResult
	forceReleaseCalled  bool
	err                 error
}

func (repository *recordingMaintenanceAttemptRepository) ClaimIssue(context.Context, ports.ClaimIssueCommand) (ports.ClaimIssueResult, error) {
	return ports.ClaimIssueResult{}, nil
}

func (repository *recordingMaintenanceAttemptRepository) RenewAttempt(context.Context, ports.RenewAttemptCommand) (ports.RenewAttemptResult, error) {
	return ports.RenewAttemptResult{}, nil
}

func (repository *recordingMaintenanceAttemptRepository) SaveAttemptNote(context.Context, ports.SaveAttemptNoteCommand) (ports.SaveAttemptNoteResult, error) {
	return ports.SaveAttemptNoteResult{}, nil
}

func (repository *recordingMaintenanceAttemptRepository) LookupSaveAttemptNote(context.Context, string, []byte) (ports.SaveAttemptNoteResult, bool, error) {
	return ports.SaveAttemptNoteResult{}, false, nil
}

func (repository *recordingMaintenanceAttemptRepository) LookupFinishedAttempt(context.Context, string, []byte) (ports.FinishAttemptResult, bool, error) {
	return ports.FinishAttemptResult{}, false, nil
}

func (repository *recordingMaintenanceAttemptRepository) FinishAttempt(context.Context, ports.FinishAttemptCommand) (ports.FinishAttemptResult, error) {
	return ports.FinishAttemptResult{}, nil
}

func (repository *recordingMaintenanceAttemptRepository) ForceReleaseAttempt(_ context.Context, command ports.ForceReleaseAttemptCommand) (ports.ForceReleaseAttemptResult, error) {
	repository.forceReleaseCalled = true
	repository.forceReleaseCommand = command
	return repository.forceReleaseResult, repository.err
}

func (repository *recordingMaintenanceAttemptRepository) ExpireAttempts(context.Context, ports.ExpireAttemptsCommand) (ports.ExpireAttemptsResult, error) {
	return ports.ExpireAttemptsResult{}, nil
}

func (repository *recordingMaintenanceAttemptRepository) ListActiveAttempts(context.Context, ports.ListActiveAttemptsCommand) ([]domain.ActiveAttemptSummary, error) {
	return nil, nil
}

func (repository *recordingMaintenanceAttemptRepository) SubmitGateEvidence(context.Context, ports.SubmitGateEvidenceCommand) (ports.SubmitGateEvidenceResult, error) {
	return ports.SubmitGateEvidenceResult{}, nil
}

func (repository *recordingMaintenanceAttemptRepository) LookupSubmitGateEvidence(context.Context, string, []byte) (ports.SubmitGateEvidenceResult, bool, error) {
	return ports.SubmitGateEvidenceResult{}, false, nil
}

func (repository *recordingMaintenanceAttemptRepository) ListAttemptEvidence(context.Context, ports.ListAttemptEvidenceCommand) ([]domain.AttemptEvidence, error) {
	return nil, nil
}

func (repository *recordingMaintenanceAttemptRepository) ReserveResources(context.Context, ports.ReserveResourcesCommand) (ports.ReserveResourcesResult, error) {
	return ports.ReserveResourcesResult{}, nil
}

func (repository *recordingMaintenanceAttemptRepository) LookupReserveResources(context.Context, string, []byte) (ports.ReserveResourcesResult, bool, error) {
	return ports.ReserveResourcesResult{}, false, nil
}

func (repository *recordingMaintenanceAttemptRepository) ReleaseResources(context.Context, ports.ReleaseResourcesCommand) (ports.ReleaseResourcesResult, error) {
	return ports.ReleaseResourcesResult{}, nil
}

func (repository *recordingMaintenanceAttemptRepository) LookupReleaseResources(context.Context, string, []byte) (ports.ReleaseResourcesResult, bool, error) {
	return ports.ReleaseResourcesResult{}, false, nil
}

type recordingMaintenanceSearchIndexRepository struct {
	rebuildCalled bool
	err           error
}

func (repository *recordingMaintenanceSearchIndexRepository) Rebuild(context.Context) error {
	repository.rebuildCalled = true
	return repository.err
}
