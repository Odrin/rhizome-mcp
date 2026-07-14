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

type DecisionRepository interface {
	RecordDecision(context.Context, RecordDecisionCommand) (domain.RecordDecisionResult, error)
}
