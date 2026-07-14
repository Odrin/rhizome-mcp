package ports

import (
	"context"
	"time"

	"rhizome-mcp/internal/domain"
)

type ApplyIssuePlanCommand struct {
	Plan           domain.IssuePlan
	IdempotencyKey string
	RequestHash    []byte
	IssueIDs       []string
	RelationIDs    []string
	DecisionIDs    []string
	LabelIDs       [][]string
	OccurredAt     time.Time
}

type CreatedPlanIssue struct {
	Ref   string       `json:"ref,omitempty"`
	Issue domain.Issue `json:"issue"`
}

type Decision struct {
	ID        string    `json:"id"`
	IssueID   *string   `json:"issue_id,omitempty"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary"`
	Content   string    `json:"content"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type ApplyIssuePlanResult struct {
	CreatedIssues    []CreatedPlanIssue     `json:"created_issues"`
	CreatedRelations []domain.IssueRelation `json:"created_relations"`
	CreatedDecisions []Decision             `json:"created_decisions"`
	LatestEventID    int64                  `json:"latest_event_id"`
}

// PlanningRepository validates against a consistent database snapshot and
// applies a fully prepared plan in one writer transaction.
type PlanningRepository interface {
	ValidateIssuePlan(context.Context, domain.IssuePlan) ([]domain.Detail, error)
	LookupAppliedIssuePlan(context.Context, string, []byte) (ApplyIssuePlanResult, bool, error)
	ApplyIssuePlan(context.Context, ApplyIssuePlanCommand) (ApplyIssuePlanResult, error)
}
