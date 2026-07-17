package ports

import (
	"context"

	"rhizome-mcp/internal/domain"
)

// ProjectRepository reads metadata for the one current project.
type ProjectRepository interface {
	GetProject(context.Context) (domain.Project, error)
	ExportLogicalProject(context.Context) (domain.LogicalProjectDocument, error)
}
