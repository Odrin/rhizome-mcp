package ports

import (
	"context"

	"rhizome-mcp/internal/domain"
)

// GetWorkContextCommand contains a validated work-context query.
type GetWorkContextCommand struct {
	Input domain.GetWorkContextInput
}

// WorkContextRepository reads the compact issue work-context projection.
type WorkContextRepository interface {
	GetWorkContext(context.Context, GetWorkContextCommand) (domain.WorkContext, error)
}
