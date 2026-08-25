package application

import (
	"context"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

type WorkflowPolicyService struct {
	repository ports.WorkflowPolicyRepository
	clock      clock.Clock
	ids        IDGenerator
}

func NewWorkflowPolicyService(repository ports.WorkflowPolicyRepository, source clock.Clock, generator IDGenerator) (*WorkflowPolicyService, error) {
	if repository == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "workflow policy repository is required", false)
	}
	if source == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "workflow policy clock is required", false)
	}
	if generator == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "workflow policy ID generator is required", false)
	}
	return &WorkflowPolicyService{repository: repository, clock: source, ids: generator}, nil
}

func (service *WorkflowPolicyService) CreatePolicy(ctx context.Context, input domain.WorkflowPolicyInput, sessionID *string, idempotencyKey string) (domain.WorkflowPolicy, error) {
	id, err := service.ids.New()
	if err != nil {
		return domain.WorkflowPolicy{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate policy identifier", false)
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return domain.WorkflowPolicy{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate policy identifier", false)
	}

	return service.repository.CreatePolicy(ctx, ports.CreateWorkflowPolicyCommand{
		ID:             id,
		Input:          input,
		SessionID:      sessionID,
		CreatedAt:      service.clock.Now().UTC(),
		IdempotencyKey: idempotencyKey,
		RequestHash:    nil,
	})
}

func (service *WorkflowPolicyService) GetPolicy(ctx context.Context, policyID string) (domain.WorkflowPolicy, error) {
	policy, err := service.repository.GetPolicy(ctx, ports.GetWorkflowPolicyCommand{PolicyID: policyID})
	if err != nil {
		return domain.WorkflowPolicy{}, err
	}
	return domain.CloneWorkflowPolicy(policy), nil
}

func (service *WorkflowPolicyService) ListPolicies(ctx context.Context, input domain.ListWorkflowPoliciesInput) (domain.WorkflowPolicyList, error) {
	normalized, err := input.Validate()
	if err != nil {
		return domain.WorkflowPolicyList{}, err
	}
	result, err := service.repository.ListPolicies(ctx, ports.ListWorkflowPoliciesCommand{Input: normalized})
	if err != nil {
		return domain.WorkflowPolicyList{}, err
	}
	return domain.CloneWorkflowPolicyList(result), nil
}

func (service *WorkflowPolicyService) UpdatePolicy(ctx context.Context, policyID string, expectedVersion int64, input domain.WorkflowPolicyInput, sessionID *string, idempotencyKey string) (domain.WorkflowPolicy, error) {
	return service.repository.UpdatePolicy(ctx, ports.UpdateWorkflowPolicyCommand{
		PolicyID:        policyID,
		ExpectedVersion: expectedVersion,
		Input:           input,
		SessionID:       sessionID,
		UpdatedAt:       service.clock.Now().UTC(),
		IdempotencyKey:  idempotencyKey,
		RequestHash:     nil,
	})
}

func (service *WorkflowPolicyService) ArchivePolicy(ctx context.Context, policyID string, expectedVersion int64, sessionID *string, idempotencyKey string) (domain.WorkflowPolicy, error) {
	return service.repository.ArchivePolicy(ctx, ports.ArchiveWorkflowPolicyCommand{
		PolicyID:        policyID,
		ExpectedVersion: expectedVersion,
		SessionID:       sessionID,
		ArchivedAt:      service.clock.Now().UTC(),
		IdempotencyKey:  idempotencyKey,
		RequestHash:     nil,
	})
}

// GateDiagnosticEvaluation carries the result of a gate diagnostic evaluation.
type GateDiagnosticEvaluation struct {
	Evaluation     domain.GateEvaluation
	Requirements   []domain.PolicyRequirement
	SourcePolicies []domain.SourcePolicyRef
	SnapshotFound  bool
}

// EvaluateGatesInput is the application-level request for a gate diagnostic.
// It exists so presentation adapters can ask for one without importing
// internal/ports: a storage-contract change must not reach DTO code
// (ISSUE-203).
type EvaluateGatesInput struct {
	IssueID          string
	EnforcementPoint domain.EnforcementPoint
	AttemptID        *string
	ReviewTargetID   *string
}

func (service *WorkflowPolicyService) EvaluateGates(ctx context.Context, input EvaluateGatesInput) (GateDiagnosticEvaluation, error) {
	command := ports.GateDiagnosticCommand{
		IssueID:   input.IssueID,
		Point:     input.EnforcementPoint,
		AttemptID: input.AttemptID,
		TargetID:  input.ReviewTargetID,
	}
	diagnostic, err := service.repository.LoadGateDiagnostic(ctx, command)
	if err != nil {
		return GateDiagnosticEvaluation{}, err
	}

	evaluation, err := domain.EvaluateGate(input.EnforcementPoint, diagnostic.Requirements, diagnostic.Evidence)
	if err != nil {
		return GateDiagnosticEvaluation{}, err
	}

	return GateDiagnosticEvaluation{
		Evaluation:     domain.CloneGateEvaluation(evaluation),
		Requirements:   diagnostic.Requirements,
		SourcePolicies: diagnostic.SourcePolicies,
		SnapshotFound:  diagnostic.SnapshotFound,
	}, nil
}
