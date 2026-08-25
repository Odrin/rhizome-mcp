package application

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

func TestWorkflowPolicyServiceConstructorValidatesRequiredDependencies(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFakeClock(now)
	generator, _ := ids.NewGenerator(fakeClock, rand.Reader)

	tests := []struct {
		name       string
		repository ports.WorkflowPolicyRepository
		clock      clock.Clock
		generator  IDGenerator
	}{
		{
			name:       "nil repository",
			repository: nil,
			clock:      fakeClock,
			generator:  generator,
		},
		{
			name:       "nil clock",
			repository: &workflowPolicyRepositoryStub{},
			clock:      nil,
			generator:  generator,
		},
		{
			name:       "nil generator",
			repository: &workflowPolicyRepositoryStub{},
			clock:      fakeClock,
			generator:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewWorkflowPolicyService(tt.repository, tt.clock, tt.generator)
			if err == nil {
				t.Fatalf("NewWorkflowPolicyService() succeeded, want an INVALID_ARGUMENT error")
			}
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeInvalidArgument {
				t.Fatalf("error = %v, want INVALID_ARGUMENT", err)
			}
		})
	}
}

func TestWorkflowPolicyServiceCreatePolicyAllocatesIDAndStampsTime(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFakeClock(now)
	generator, _ := ids.NewGenerator(fakeClock, rand.Reader)
	repository := &workflowPolicyRepositoryStub{
		policy: domain.WorkflowPolicy{
			ID:        "generated-id",
			Status:    domain.PolicyStatusActive,
			Version:   1,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	service, _ := NewWorkflowPolicyService(repository, fakeClock, generator)

	policy, err := service.CreatePolicy(context.Background(), domain.WorkflowPolicyInput{
		Selector:     domain.PolicySelectorInput{IssueTypes: []domain.Type{domain.TypeTask}},
		Requirements: []domain.PolicyRequirementInput{},
	}, nil, "")

	if err != nil {
		t.Fatalf("CreatePolicy() error = %v", err)
	}
	if policy.ID != "generated-id" || policy.CreatedAt != now {
		t.Fatalf("CreatePolicy() = %#v, want ID=generated-id and CreatedAt=%v", policy, now)
	}
	if repository.createCommand.ID == "" {
		t.Fatalf("repository was not called")
	}
	if repository.createCommand.CreatedAt != now {
		t.Fatalf("repository CreatedAt = %v, want %v", repository.createCommand.CreatedAt, now)
	}
}

func TestWorkflowPolicyServiceListPoliciesValidatesInput(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFakeClock(now)
	generator, _ := ids.NewGenerator(fakeClock, rand.Reader)
	repository := &workflowPolicyRepositoryStub{}
	service, _ := NewWorkflowPolicyService(repository, fakeClock, generator)

	// Test that a limit of 101 (over the maximum) is rejected before reaching the repository.
	_, err := service.ListPolicies(context.Background(), domain.ListWorkflowPoliciesInput{Limit: 101})
	if err == nil {
		t.Fatalf("ListPolicies(limit 101) succeeded, want an INVALID_ARGUMENT error")
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeInvalidArgument {
		t.Fatalf("error = %v, want INVALID_ARGUMENT", err)
	}
	// Verify the stub was NOT called (validation happens before the repository).
	if repository.listCommand.Input.Limit != 0 {
		t.Fatalf("repository was called despite invalid input")
	}
}

func TestWorkflowPolicyServiceReadsAreCloned(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFakeClock(now)
	generator, _ := ids.NewGenerator(fakeClock, rand.Reader)

	original := domain.WorkflowPolicy{
		ID:       "policy-1",
		Status:   domain.PolicyStatusActive,
		Version:  1,
		Selector: domain.PolicySelector{IssueTypes: []domain.Type{domain.TypeTask}},
		Requirements: []domain.PolicyRequirement{
			{PolicyID: "policy-1", Key: "req-1", Kind: domain.RequirementKindIssueFieldNonblank, Field: "acceptance_criteria"},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	repository := &workflowPolicyRepositoryStub{policy: original}
	service, _ := NewWorkflowPolicyService(repository, fakeClock, generator)

	// Read once.
	first, err := service.GetPolicy(context.Background(), "policy-1")
	if err != nil {
		t.Fatalf("GetPolicy() error = %v", err)
	}

	// Mutate the returned value.
	if len(first.Selector.IssueTypes) > 0 {
		first.Selector.IssueTypes[0] = domain.TypeBug
	}

	// Read again.
	second, err := service.GetPolicy(context.Background(), "policy-1")
	if err != nil {
		t.Fatalf("GetPolicy() second read error = %v", err)
	}

	// The second read should still have the original value.
	if len(second.Selector.IssueTypes) == 0 || second.Selector.IssueTypes[0] != domain.TypeTask {
		t.Fatalf("second read returned mutated value; defensive cloning failed")
	}
}

func TestWorkflowPolicyServiceEvaluateGatesReturnsEvaluation(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFakeClock(now)
	generator, _ := ids.NewGenerator(fakeClock, rand.Reader)

	requirement := domain.PolicyRequirement{
		PolicyID: "policy-1",
		Key:      "acceptance-criteria",
		Kind:     domain.RequirementKindIssueFieldNonblank,
		Field:    "acceptance_criteria",
	}

	repository := &workflowPolicyRepositoryStub{
		gateDiagnostic: ports.GateDiagnosticResult{
			Requirements:   []domain.PolicyRequirement{requirement},
			SourcePolicies: []domain.SourcePolicyRef{{PolicyID: "policy-1", Version: 1}},
			Evidence: domain.GateEvidence{
				AcceptanceCriteriaBlank: true,
				AttemptEvidenceKeys:     make(map[string]bool),
				ReviewApprovalPurposes:  make(map[string]bool),
			},
			SnapshotFound: false,
		},
	}
	service, _ := NewWorkflowPolicyService(repository, fakeClock, generator)

	result, err := service.EvaluateGates(context.Background(), EvaluateGatesInput{
		IssueID:          "issue-1",
		EnforcementPoint: domain.EnforcementPointClaimWork,
	})

	if err != nil {
		t.Fatalf("EvaluateGates() error = %v", err)
	}
	if len(result.Evaluation.Unmet) == 0 {
		t.Fatalf("EvaluateGates() unmet requirements = empty, want unsatisfied requirement")
	}
	if result.SnapshotFound != false {
		t.Fatalf("SnapshotFound = %v, want false", result.SnapshotFound)
	}
	if len(result.Requirements) != 1 || result.Requirements[0].Key != "acceptance-criteria" {
		t.Fatalf("requirements = %#v, want one with key 'acceptance-criteria'", result.Requirements)
	}
}

func TestWorkflowPolicyServiceEvaluateGatesPerformsNoWrites(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFakeClock(now)
	generator, _ := ids.NewGenerator(fakeClock, rand.Reader)

	repository := &workflowPolicyRepositoryStub{
		gateDiagnostic: ports.GateDiagnosticResult{
			Requirements:   []domain.PolicyRequirement{},
			SourcePolicies: []domain.SourcePolicyRef{},
			Evidence: domain.GateEvidence{
				AcceptanceCriteriaBlank: false,
				AttemptEvidenceKeys:     make(map[string]bool),
				ReviewApprovalPurposes:  make(map[string]bool),
			},
			SnapshotFound: false,
		},
	}
	service, _ := NewWorkflowPolicyService(repository, fakeClock, generator)

	_, err := service.EvaluateGates(context.Background(), EvaluateGatesInput{
		IssueID:          "issue-1",
		EnforcementPoint: domain.EnforcementPointClaimWork,
	})

	if err != nil {
		t.Fatalf("EvaluateGates() error = %v", err)
	}

	// Verify no write methods were called.
	if repository.createCommand.ID != "" || repository.updateCommand.PolicyID != "" || repository.archiveCommand.PolicyID != "" {
		t.Fatalf("EvaluateGates() invoked a write method unexpectedly")
	}
}

type workflowPolicyRepositoryStub struct {
	policy            domain.WorkflowPolicy
	policyList        domain.WorkflowPolicyList
	gateDiagnostic    ports.GateDiagnosticResult
	createCommand     ports.CreateWorkflowPolicyCommand
	updateCommand     ports.UpdateWorkflowPolicyCommand
	archiveCommand    ports.ArchiveWorkflowPolicyCommand
	listCommand       ports.ListWorkflowPoliciesCommand
	diagnosticCommand ports.GateDiagnosticCommand
	diagnosticCalls   int
	err               error
}

func (stub *workflowPolicyRepositoryStub) CreatePolicy(_ context.Context, command ports.CreateWorkflowPolicyCommand) (domain.WorkflowPolicy, error) {
	stub.createCommand = command
	if stub.err != nil {
		return domain.WorkflowPolicy{}, stub.err
	}
	return stub.policy, nil
}

func (stub *workflowPolicyRepositoryStub) GetPolicy(_ context.Context, _ ports.GetWorkflowPolicyCommand) (domain.WorkflowPolicy, error) {
	if stub.err != nil {
		return domain.WorkflowPolicy{}, stub.err
	}
	return stub.policy, nil
}

func (stub *workflowPolicyRepositoryStub) ListPolicies(_ context.Context, command ports.ListWorkflowPoliciesCommand) (domain.WorkflowPolicyList, error) {
	stub.listCommand = command
	if stub.err != nil {
		return domain.WorkflowPolicyList{}, stub.err
	}
	return stub.policyList, nil
}

func (stub *workflowPolicyRepositoryStub) UpdatePolicy(_ context.Context, command ports.UpdateWorkflowPolicyCommand) (domain.WorkflowPolicy, error) {
	stub.updateCommand = command
	if stub.err != nil {
		return domain.WorkflowPolicy{}, stub.err
	}
	return stub.policy, nil
}

func (stub *workflowPolicyRepositoryStub) ArchivePolicy(_ context.Context, command ports.ArchiveWorkflowPolicyCommand) (domain.WorkflowPolicy, error) {
	stub.archiveCommand = command
	if stub.err != nil {
		return domain.WorkflowPolicy{}, stub.err
	}
	return stub.policy, nil
}

func (stub *workflowPolicyRepositoryStub) GetAttemptGateSnapshot(_ context.Context, _ ports.GetAttemptGateSnapshotCommand) (domain.GateSnapshot, error) {
	if stub.err != nil {
		return domain.GateSnapshot{}, stub.err
	}
	return domain.GateSnapshot{}, nil
}

func (stub *workflowPolicyRepositoryStub) GetReviewTargetGateSnapshot(_ context.Context, _ ports.GetReviewTargetGateSnapshotCommand) (domain.GateSnapshot, error) {
	if stub.err != nil {
		return domain.GateSnapshot{}, stub.err
	}
	return domain.GateSnapshot{}, nil
}

func (stub *workflowPolicyRepositoryStub) LoadGateDiagnostic(_ context.Context, command ports.GateDiagnosticCommand) (ports.GateDiagnosticResult, error) {
	stub.diagnosticCommand = command
	stub.diagnosticCalls++
	if stub.err != nil {
		return ports.GateDiagnosticResult{}, stub.err
	}
	return stub.gateDiagnostic, nil
}

// TestWorkflowPolicyServiceEvaluateGatesParsesIssueIdentifier guards the
// wiring the ISSUE-174 review found broken: evaluate_gates advertises both
// identifier forms, so the service must hand the repository a parsed
// identifier rather than the raw request string. Passing the string through
// is what made an ISSUE-N return ISSUE_NOT_FOUND.
func TestWorkflowPolicyServiceEvaluateGatesParsesIssueIdentifier(t *testing.T) {
	fakeClock := clock.NewFakeClock(time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC))
	generator, _ := ids.NewGenerator(fakeClock, rand.Reader)
	repository := &workflowPolicyRepositoryStub{}
	service, _ := NewWorkflowPolicyService(repository, fakeClock, generator)

	if _, err := service.EvaluateGates(context.Background(), EvaluateGatesInput{
		IssueID:          "ISSUE-174",
		EnforcementPoint: domain.EnforcementPointClaimWork,
	}); err != nil {
		t.Fatalf("EvaluateGates() error = %v", err)
	}
	identifier := repository.diagnosticCommand.Identifier
	if identifier.Kind != domain.IssueIdentifierDisplayID || identifier.SequenceNo != 174 {
		t.Fatalf("identifier passed to the repository = %+v, want a display identifier with sequence 174", identifier)
	}
}

// TestWorkflowPolicyServiceEvaluateGatesRejectsMalformedIdentifier pins that
// a request the schema pattern would not accept is refused before any read
// reaches storage.
func TestWorkflowPolicyServiceEvaluateGatesRejectsMalformedIdentifier(t *testing.T) {
	fakeClock := clock.NewFakeClock(time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC))
	generator, _ := ids.NewGenerator(fakeClock, rand.Reader)
	repository := &workflowPolicyRepositoryStub{}
	service, _ := NewWorkflowPolicyService(repository, fakeClock, generator)

	_, err := service.EvaluateGates(context.Background(), EvaluateGatesInput{
		IssueID:          "not-an-identifier",
		EnforcementPoint: domain.EnforcementPointClaimWork,
	})
	if err == nil {
		t.Fatal("EvaluateGates() succeeded, want a rejection for a malformed identifier")
	}
	if repository.diagnosticCalls != 0 {
		t.Fatalf("repository diagnostic calls = %d, want 0 for a malformed identifier", repository.diagnosticCalls)
	}
}
