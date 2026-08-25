package ports

import (
	"context"
	"time"

	"rhizome-mcp/internal/domain"
)

// CreateWorkflowPolicyCommand captures a workflow policy creation intent.
// The application layer allocates ID; the repository persists a policy_created
// audit event in the same transaction as the row insert.
type CreateWorkflowPolicyCommand struct {
	ID             string
	Input          domain.WorkflowPolicyInput
	SessionID      *string
	CreatedAt      time.Time
	IdempotencyKey string
	RequestHash    []byte
}

// GetWorkflowPolicyCommand identifies one policy to read.
type GetWorkflowPolicyCommand struct {
	PolicyID string
}

// ListWorkflowPoliciesCommand carries filtering and pagination for policies.
type ListWorkflowPoliciesCommand struct {
	Input domain.ListWorkflowPoliciesInput
}

// UpdateWorkflowPolicyCommand captures an optimistic policy update. Updating
// replaces the selector and requirement set wholesale (the locked schema has
// no partial-field patch) and appends a policy_updated audit event.
type UpdateWorkflowPolicyCommand struct {
	PolicyID        string
	ExpectedVersion int64
	Input           domain.WorkflowPolicyInput
	SessionID       *string
	UpdatedAt       time.Time
	IdempotencyKey  string
	RequestHash     []byte
}

// ArchiveWorkflowPolicyCommand captures an optimistic, irreversible archive.
// Archiving an already-archived policy at its current version is a no-op
// success (idempotent), matching the locked schema's "soft and irreversible"
// archive semantics.
type ArchiveWorkflowPolicyCommand struct {
	PolicyID        string
	ExpectedVersion int64
	SessionID       *string
	ArchivedAt      time.Time
	IdempotencyKey  string
	RequestHash     []byte
}

// GetAttemptGateSnapshotCommand identifies one work attempt's frozen gate
// snapshot to read.
type GetAttemptGateSnapshotCommand struct {
	AttemptID string
}

// GetReviewTargetGateSnapshotCommand identifies one review target's frozen
// gate snapshot to read.
type GetReviewTargetGateSnapshotCommand struct {
	TargetID string
}

// GateDiagnosticCommand requests the read-only inputs of a gate decision.
// Identifier accepts either form the public contract advertises (ULID or
// ISSUE-N); the repository resolves it exactly like every other issue read.
type GateDiagnosticCommand struct {
	Identifier domain.IssueIdentifier
	Point      domain.EnforcementPoint
	AttemptID  *string // when set, use this attempt's frozen snapshot
	TargetID   *string // when set, use this review target's frozen snapshot
}

// IssueGateSummaryCommand requests one issue's compact gate summary -- the
// same projection get_work_context reports (ISSUE-175 AC1), reused by the
// board and issue-detail surfaces (AC2). Identifier accepts either public
// form (ULID or ISSUE-N); Now anchors the active-attempt lease check, like
// GetWorkContextCommand.Now.
type IssueGateSummaryCommand struct {
	Identifier domain.IssueIdentifier
	Now        time.Time
}

// GateDiagnosticResult carries the assembled inputs of a gate decision.
type GateDiagnosticResult struct {
	Requirements   []domain.PolicyRequirement
	SourcePolicies []domain.SourcePolicyRef
	Evidence       domain.GateEvidence
	SnapshotFound  bool // false when requirements came from live policies
}

// WorkflowPolicyRepository persists workflow policies, their audit trail, and
// the immutable gate snapshots frozen at claim and review-request-creation
// time (docs/02 §17.6). Snapshot rows are written inside the same
// transaction as the claim or review-target creation they guard (ISSUE-172/
// ISSUE-173), not through this interface -- this repository only exposes
// read access to snapshots already written, plus policy CRUD.
type WorkflowPolicyRepository interface {
	CreatePolicy(context.Context, CreateWorkflowPolicyCommand) (domain.WorkflowPolicy, error)
	GetPolicy(context.Context, GetWorkflowPolicyCommand) (domain.WorkflowPolicy, error)
	ListPolicies(context.Context, ListWorkflowPoliciesCommand) (domain.WorkflowPolicyList, error)
	UpdatePolicy(context.Context, UpdateWorkflowPolicyCommand) (domain.WorkflowPolicy, error)
	ArchivePolicy(context.Context, ArchiveWorkflowPolicyCommand) (domain.WorkflowPolicy, error)
	GetAttemptGateSnapshot(context.Context, GetAttemptGateSnapshotCommand) (domain.GateSnapshot, error)
	GetReviewTargetGateSnapshot(context.Context, GetReviewTargetGateSnapshotCommand) (domain.GateSnapshot, error)
	LoadGateDiagnostic(context.Context, GateDiagnosticCommand) (GateDiagnosticResult, error)
	LoadIssueGateSummary(context.Context, IssueGateSummaryCommand) (domain.WorkContextGateSummary, error)
}
