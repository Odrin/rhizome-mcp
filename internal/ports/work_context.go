package ports

import (
	"context"
	"time"

	"rhizome-mcp/internal/domain"
)

// GetWorkContextCommand contains a validated work-context query and the UTC
// time used for effective status.
type GetWorkContextCommand struct {
	Input domain.GetWorkContextInput
	Now   time.Time
}

// WorkContextRepository reads the compact issue work-context projection.
type WorkContextRepository interface {
	GetWorkContext(context.Context, GetWorkContextCommand) (domain.WorkContext, error)
}
