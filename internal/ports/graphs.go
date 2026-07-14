package ports

import (
	"context"

	"rhizome-mcp/internal/domain"
)

// LoadGraphCommand identifies the optional explicit graph root. A nil root
// requests a project-wide planning snapshot.
type LoadGraphCommand struct {
	RootIdentifier *domain.IssueIdentifier
}

// GraphRepository reads one consistent, fully batched graph candidate snapshot.
type GraphRepository interface {
	LoadGraph(context.Context, LoadGraphCommand) (domain.GraphSnapshot, error)
}
