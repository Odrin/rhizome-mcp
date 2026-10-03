package ports

import (
	"context"
	"time"

	"rhizome-mcp/internal/domain"
)

// LoadGraphCommand identifies the optional explicit graph root. A nil root
// requests a project-wide planning snapshot.
type LoadGraphCommand struct {
	RootIdentifier *domain.IssueIdentifier
	Now            time.Time
	Stats          *GraphLoadStats
	Traversal      *domain.GraphTraversal
}

type GraphLoadStats struct {
	Queries          int
	RowsRead         int
	CandidateNodes   int
	HydratedNodes    int
	SnapshotDuration time.Duration
}

// GraphRepository reads one consistent, fully batched graph candidate snapshot.
type GraphRepository interface {
	LoadGraph(context.Context, LoadGraphCommand) (domain.GraphSnapshot, error)
}
