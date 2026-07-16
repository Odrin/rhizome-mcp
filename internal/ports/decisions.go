package ports

import (
	"context"
	"time"

	"rhizome-mcp/internal/domain"
)

type RecordDecisionCommand struct {
	ID         string
	Input      domain.RecordDecisionInput
	OccurredAt time.Time
}

type ListDecisionsCommand struct {
	Input domain.ListDecisionsInput
}

type DecisionRepository interface {
	RecordDecision(context.Context, RecordDecisionCommand) (domain.RecordDecisionResult, error)
	ListDecisions(context.Context, ListDecisionsCommand) (domain.DecisionList, error)
}
