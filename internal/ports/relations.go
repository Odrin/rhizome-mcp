package ports

import (
	"context"
	"time"

	"rhizome-mcp/internal/domain"
)

// ManageIssueRelationCommand contains a validated relation mutation and
// application-generated values required by storage.
type ManageIssueRelationCommand struct {
	Action           domain.RelationAction
	SourceIdentifier domain.IssueIdentifier
	TargetIdentifier domain.IssueIdentifier
	RelationType     domain.RelationType
	RelationID       string // Required for add; empty for remove.
	OccurredAt       time.Time
}

// ManageIssueRelationResult is the canonical relation and its two current
// issue projections. Changed is false for an idempotent add or absent remove.
type ManageIssueRelationResult struct {
	Relation       domain.IssueRelation
	AffectedIssues []domain.IssueProjection
	Changed        bool
}

// RelationRepository persists one relation mutation atomically with its event.
type RelationRepository interface {
	ManageIssueRelation(context.Context, ManageIssueRelationCommand) (ManageIssueRelationResult, error)
}
