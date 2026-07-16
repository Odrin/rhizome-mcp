package application

import (
	"context"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

type DecisionService struct {
	repository ports.DecisionRepository
	clock      clock.Clock
	ids        IDGenerator
}

func NewDecisionService(repository ports.DecisionRepository, source clock.Clock, generator IDGenerator) (*DecisionService, error) {
	if repository == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "decision repository is required", false)
	}
	if source == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "decision clock is required", false)
	}
	if generator == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "decision ID generator is required", false)
	}
	return &DecisionService{repository: repository, clock: source, ids: generator}, nil
}

func (service *DecisionService) RecordDecision(ctx context.Context, input domain.RecordDecisionInput) (domain.RecordDecisionResult, error) {
	normalized, err := input.Validate()
	if err != nil {
		return domain.RecordDecisionResult{}, err
	}
	id, err := service.ids.New()
	if err != nil {
		return domain.RecordDecisionResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate decision identifier", false)
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return domain.RecordDecisionResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate decision identifier", false)
	}
	return service.repository.RecordDecision(ctx, ports.RecordDecisionCommand{
		ID: id, Input: normalized, OccurredAt: service.clock.Now().UTC(),
	})
}

func (service *DecisionService) ListDecisions(ctx context.Context, input domain.ListDecisionsInput) (domain.DecisionList, error) {
	normalized, err := input.Validate()
	if err != nil {
		return domain.DecisionList{}, err
	}
	result, err := service.repository.ListDecisions(ctx, ports.ListDecisionsCommand{Input: normalized})
	if err != nil {
		return domain.DecisionList{}, err
	}
	return domain.CloneDecisionList(result), nil
}
