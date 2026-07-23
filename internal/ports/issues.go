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
	ID             string
	Input          domain.CreateIssueInput
	LabelIDs       []string // Corresponds to Input.Labels when missing creation is enabled.
	CreatedAt      time.Time
	IdempotencyKey string
	RequestHash    []byte
}

// UpdateIssueCommand contains a validated optimistic patch and the time to
// persist if its conditional write succeeds.
type UpdateIssueCommand struct {
	Identifier          domain.IssueIdentifier
	ExpectedVersion     int64
	Changes             domain.IssuePatch
	LabelIDs            []string // Corresponds to Changes.Labels when missing creation is enabled.
	CreateMissingLabels bool
	UpdatedAt           time.Time
	IdempotencyKey      string
	RequestHash         []byte
}

// UpdateIssueResult is the persisted projection and sorted names of fields
// changed by a successful patch.
type UpdateIssueResult struct {
	Issue         domain.Issue
	ChangedFields []string
}

// ArchiveIssueCommand contains a validated archive request and its mutation
// timestamp.
type ArchiveIssueCommand struct {
	Identifier      domain.IssueIdentifier
	ExpectedVersion int64
	ArchivedAt      time.Time
	IdempotencyKey  string
	RequestHash     []byte
}

// ArchiveIssueResult is the full persisted projection after archiving.
type ArchiveIssueResult struct {
	Issue domain.Issue
}

// ListLabelsCommand contains a validated page request.
type ListLabelsCommand struct {
	Input domain.ListLabelsInput
}

// ListIssuesCommand contains a validated page request.
type ListIssuesCommand struct {
	Input domain.ListIssuesInput
	Now   time.Time
}

// IssueRepository reads issue projections and persists issue mutations.
type IssueRepository interface {
	CreateIssue(context.Context, CreateIssueCommand) (domain.Issue, error)
	LookupCreateIssue(context.Context, string, []byte) (domain.Issue, bool, error)
	UpdateIssue(context.Context, UpdateIssueCommand) (UpdateIssueResult, error)
	LookupUpdateIssue(context.Context, string, []byte) (UpdateIssueResult, bool, error)
	ArchiveIssue(context.Context, ArchiveIssueCommand) (ArchiveIssueResult, error)
	LookupArchiveIssue(context.Context, string, []byte) (ArchiveIssueResult, bool, error)
	GetIssue(context.Context, domain.IssueIdentifier) (domain.Issue, error)
	ListLabels(context.Context, ListLabelsCommand) (domain.LabelList, error)
	ListIssues(context.Context, ListIssuesCommand) (domain.IssueList, error)
}
