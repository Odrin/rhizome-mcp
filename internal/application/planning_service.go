package application

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"time"

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

type ApplyIssuePlanCreatedIssue struct {
	Ref   string
	Issue domain.Issue
}

type ApplyIssuePlanDecision struct {
	ID        string
	IssueID   *string
	Title     string
	Summary   string
	Content   string
	Status    string
	CreatedAt time.Time
}

type ApplyIssuePlanResult struct {
	CreatedIssues    []ApplyIssuePlanCreatedIssue
	CreatedRelations []domain.IssueRelation
	CreatedDecisions []ApplyIssuePlanDecision
	LatestEventID    int64
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

func (service *PlanningService) ApplyIssuePlan(ctx context.Context, plan domain.IssuePlan, key string) (ApplyIssuePlanResult, error) {
	if err := domain.ValidateText("idempotency_key", key, domain.MaxIdempotencyKeyRunes); err != nil {
		return ApplyIssuePlanResult{}, err
	}
	if strings.TrimSpace(key) == "" {
		return ApplyIssuePlanResult{}, domain.NewError(domain.CodeInvalidArgument, "idempotency_key must not be blank", false,
			domain.Detail{Field: "idempotency_key", Code: "REQUIRED"})
	}
	validation := domain.NormalizeIssuePlan(plan)
	if !validation.Valid {
		return ApplyIssuePlanResult{}, domain.NewError(domain.CodeValidationError, "issue plan is invalid", false, validation.Errors...)
	}
	encoded, err := json.Marshal(validation.NormalizedPlan)
	if err != nil {
		return ApplyIssuePlanResult{}, domain.WrapError(err, domain.CodeStorageFailure, "cannot normalize issue plan", false)
	}
	hash := sha256.Sum256(encoded)
	if result, found, err := service.repository.LookupAppliedIssuePlan(ctx, key, hash[:]); err != nil {
		return ApplyIssuePlanResult{}, err
	} else if found {
		return ApplyIssuePlanResult{
			CreatedIssues:    convertCreatedPlanIssues(result.CreatedIssues),
			CreatedRelations: result.CreatedRelations,
			CreatedDecisions: convertPlanDecisions(result.CreatedDecisions),
			LatestEventID:    result.LatestEventID,
		}, nil
	}
	command := ports.ApplyIssuePlanCommand{Plan: validation.NormalizedPlan, IdempotencyKey: key, RequestHash: hash[:], OccurredAt: service.clock.Now().UTC()}
	for range command.Plan.Issues {
		id, err := service.newID()
		if err != nil {
			return ApplyIssuePlanResult{}, err
		}
		command.IssueIDs = append(command.IssueIDs, id)
	}
	for range command.Plan.Relations {
		id, err := service.newID()
		if err != nil {
			return ApplyIssuePlanResult{}, err
		}
		command.RelationIDs = append(command.RelationIDs, id)
	}
	for range command.Plan.Decisions {
		id, err := service.newID()
		if err != nil {
			return ApplyIssuePlanResult{}, err
		}
		command.DecisionIDs = append(command.DecisionIDs, id)
	}
	for _, issue := range command.Plan.Issues {
		var labels []string
		if issue.CreateMissingLabels {
			labels = make([]string, len(issue.Labels))
			for i := range labels {
				id, err := service.newID()
				if err != nil {
					return ApplyIssuePlanResult{}, err
				}
				labels[i] = id
			}
		}
		command.LabelIDs = append(command.LabelIDs, labels)
	}
	result, err := service.repository.ApplyIssuePlan(ctx, command)
	if err != nil {
		return ApplyIssuePlanResult{}, err
	}
	return ApplyIssuePlanResult{
		CreatedIssues:    convertCreatedPlanIssues(result.CreatedIssues),
		CreatedRelations: result.CreatedRelations,
		CreatedDecisions: convertPlanDecisions(result.CreatedDecisions),
		LatestEventID:    result.LatestEventID,
	}, nil
}

func convertCreatedPlanIssues(issues []ports.CreatedPlanIssue) []ApplyIssuePlanCreatedIssue {
	result := make([]ApplyIssuePlanCreatedIssue, len(issues))
	for i, issue := range issues {
		result[i] = ApplyIssuePlanCreatedIssue{Ref: issue.Ref, Issue: issue.Issue}
	}
	return result
}

func convertPlanDecisions(decisions []ports.Decision) []ApplyIssuePlanDecision {
	result := make([]ApplyIssuePlanDecision, len(decisions))
	for i, decision := range decisions {
		result[i] = ApplyIssuePlanDecision{
			ID:        decision.ID,
			IssueID:   decision.IssueID,
			Title:     decision.Title,
			Summary:   decision.Summary,
			Content:   decision.Content,
			Status:    decision.Status,
			CreatedAt: decision.CreatedAt,
		}
	}
	return result
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
