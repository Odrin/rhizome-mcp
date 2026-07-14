package ports

import (
	"context"

	"rhizome-mcp/internal/domain"
)

// GetIssueActivityCommand contains a validated activity query.
type GetIssueActivityCommand struct {
	Input domain.GetIssueActivityInput
}

// ActivityRepository reads the issue activity projection.
type ActivityRepository interface {
	GetIssueActivity(context.Context, GetIssueActivityCommand) (domain.IssueActivity, error)
}
