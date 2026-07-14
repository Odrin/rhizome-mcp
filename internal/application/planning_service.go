package application

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

type PlanningService struct {
	repository ports.PlanningRepository
	clock      clock.Clock
	ids        IDGenerator
}

func NewPlanningService(repository ports.PlanningRepository, source clock.Clock, generator IDGenerator) (*PlanningService, error) {
	if repository == nil || source == nil || generator == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "planning dependencies are required", false)
	}
	return &PlanningService{repository: repository, clock: source, ids: generator}, nil
}

func (service *PlanningService) ValidateIssuePlan(ctx context.Context, plan domain.IssuePlan) (domain.PlanValidation, error) {
	validation := domain.NormalizeIssuePlan(plan)
	if !validation.Valid {
		return validation, nil
	}
	details, err := service.repository.ValidateIssuePlan(ctx, validation.NormalizedPlan)
	if err != nil {
		return domain.PlanValidation{}, err
	}
	return domain.MergePlanErrors(validation, details), nil
}

func (service *PlanningService) ApplyIssuePlan(ctx context.Context, plan domain.IssuePlan, key string) (ports.ApplyIssuePlanResult, error) {
	if err := domain.ValidateText("idempotency_key", key, domain.MaxIdempotencyKeyRunes); err != nil {
		return ports.ApplyIssuePlanResult{}, err
	}
	if strings.TrimSpace(key) == "" {
		return ports.ApplyIssuePlanResult{}, domain.NewError(domain.CodeInvalidArgument, "idempotency_key must not be blank", false,
			domain.Detail{Field: "idempotency_key", Code: "REQUIRED"})
	}
	validation := domain.NormalizeIssuePlan(plan)
	if !validation.Valid {
		return ports.ApplyIssuePlanResult{}, domain.NewError(domain.CodeValidationError, "issue plan is invalid", false, validation.Errors...)
	}
	encoded, err := json.Marshal(validation.NormalizedPlan)
	if err != nil {
		return ports.ApplyIssuePlanResult{}, domain.WrapError(err, domain.CodeStorageFailure, "cannot normalize issue plan", false)
	}
	hash := sha256.Sum256(encoded)
	if result, found, err := service.repository.LookupAppliedIssuePlan(ctx, key, hash[:]); err != nil {
		return ports.ApplyIssuePlanResult{}, err
	} else if found {
		return result, nil
	}
	command := ports.ApplyIssuePlanCommand{Plan: validation.NormalizedPlan, IdempotencyKey: key, RequestHash: hash[:], OccurredAt: service.clock.Now().UTC()}
	for range command.Plan.Issues {
		id, err := service.newID()
		if err != nil {
			return ports.ApplyIssuePlanResult{}, err
		}
		command.IssueIDs = append(command.IssueIDs, id)
	}
	for range command.Plan.Relations {
		id, err := service.newID()
		if err != nil {
			return ports.ApplyIssuePlanResult{}, err
		}
		command.RelationIDs = append(command.RelationIDs, id)
	}
	for range command.Plan.Decisions {
		id, err := service.newID()
		if err != nil {
			return ports.ApplyIssuePlanResult{}, err
		}
		command.DecisionIDs = append(command.DecisionIDs, id)
	}
	for _, issue := range command.Plan.Issues {
		labels := make([]string, len(issue.Labels))
		for i := range labels {
			id, err := service.newID()
			if err != nil {
				return ports.ApplyIssuePlanResult{}, err
			}
			labels[i] = id
		}
		command.LabelIDs = append(command.LabelIDs, labels)
	}
	return service.repository.ApplyIssuePlan(ctx, command)
}

func (service *PlanningService) newID() (string, error) {
	id, err := service.ids.New()
	if err != nil {
		return "", domain.WrapError(err, domain.CodeIDGeneration, "cannot generate plan identifier", false)
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return "", domain.WrapError(err, domain.CodeIDGeneration, "cannot generate plan identifier", false)
	}
	return id, nil
}
