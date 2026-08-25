package application

import (
	"context"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

type MaintenanceService struct {
	attemptRepository     ports.AttemptRepository
	searchIndexRepository ports.SearchIndexRepository
	clock                 clock.Clock
}

type ForceReleaseAttemptResult struct {
	Attempt       domain.WorkAttempt
	LatestEventID int64
}

func NewMaintenanceService(attemptRepository ports.AttemptRepository, searchIndexRepository ports.SearchIndexRepository, source clock.Clock) (*MaintenanceService, error) {
	if attemptRepository == nil || searchIndexRepository == nil || source == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "maintenance dependencies are required", false)
	}
	return &MaintenanceService{attemptRepository: attemptRepository, searchIndexRepository: searchIndexRepository, clock: source}, nil
}

func (service *MaintenanceService) ForceReleaseAttempt(ctx context.Context, attemptID string) (ForceReleaseAttemptResult, error) {
	if _, err := ids.ParseStrict(attemptID); err != nil {
		return ForceReleaseAttemptResult{}, domain.NewError(domain.CodeInvalidArgument, "attempt release command is invalid", false)
	}
	now := service.clock.Now().UTC()
	result, err := service.attemptRepository.ForceReleaseAttempt(ctx, ports.ForceReleaseAttemptCommand{AttemptID: attemptID, OccurredAt: now})
	if err != nil {
		return ForceReleaseAttemptResult{}, err
	}
	return ForceReleaseAttemptResult{
		Attempt:       result.Attempt,
		LatestEventID: result.LatestEventID,
	}, nil
}

func (service *MaintenanceService) RebuildSearchIndex(ctx context.Context) error {
	return service.searchIndexRepository.Rebuild(ctx)
}
