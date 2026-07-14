// Package ports defines application-owned persistence boundaries.
package ports

import (
	"context"
	"time"

	"rhizome-mcp/internal/domain"
)

// CreateIssueCommand contains the application-generated values needed to
// atomically allocate and persist a new issue.
type CreateIssueCommand struct {
	ID        string
	Input     domain.CreateIssueInput
	CreatedAt time.Time
}

// UpdateIssueCommand contains a validated optimistic patch and the time to
// persist if its conditional write succeeds.
type UpdateIssueCommand struct {
	Identifier      domain.IssueIdentifier
	ExpectedVersion int64
	Changes         domain.IssuePatch
	UpdatedAt       time.Time
}

// UpdateIssueResult is the persisted projection and sorted names of fields
// changed by a successful patch.
type UpdateIssueResult struct {
	Issue         domain.Issue
	ChangedFields []string
}

// IssueRepository reads issue projections and persists issue mutations.
type IssueRepository interface {
	CreateIssue(context.Context, CreateIssueCommand) (domain.Issue, error)
	UpdateIssue(context.Context, UpdateIssueCommand) (UpdateIssueResult, error)
	GetIssue(context.Context, domain.IssueIdentifier) (domain.Issue, error)
}
