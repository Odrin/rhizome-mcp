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

func NewMaintenanceService(attemptRepository ports.AttemptRepository, searchIndexRepository ports.SearchIndexRepository, source clock.Clock) (*MaintenanceService, error) {
	if attemptRepository == nil || searchIndexRepository == nil || source == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "maintenance dependencies are required", false)
	}
	return &MaintenanceService{attemptRepository: attemptRepository, searchIndexRepository: searchIndexRepository, clock: source}, nil
}

func (service *MaintenanceService) ForceReleaseAttempt(ctx context.Context, attemptID string) (ports.ForceReleaseAttemptResult, error) {
	if _, err := ids.ParseStrict(attemptID); err != nil {
		return ports.ForceReleaseAttemptResult{}, domain.NewError(domain.CodeInvalidArgument, "attempt release command is invalid", false)
	}
	now := service.clock.Now().UTC()
	return service.attemptRepository.ForceReleaseAttempt(ctx, ports.ForceReleaseAttemptCommand{AttemptID: attemptID, OccurredAt: now})
}

func (service *MaintenanceService) RebuildSearchIndex(ctx context.Context) error {
	return service.searchIndexRepository.Rebuild(ctx)
}
