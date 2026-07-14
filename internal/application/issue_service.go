// Package application contains use cases composed from domain rules and ports.
package application

import (
	"context"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

// IDGenerator creates canonical internal identifiers.
type IDGenerator interface {
	New() (string, error)
}

// IssueService creates, updates, and queries issues.
type IssueService struct {
	repository ports.IssueRepository
	clock      clock.Clock
	ids        IDGenerator
}

// CreateIssueResult contains the allocated identity and persisted issue
// projection for one successful creation.
type CreateIssueResult struct {
	ID         string
	DisplayID  string
	SequenceNo int64
	Issue      domain.Issue
}

// UpdateIssueResult contains the persisted projection and sorted changed field
// names after an optimistic patch.
type UpdateIssueResult struct {
	Issue         domain.Issue
	ChangedFields []string
}

// NewIssueService composes the issue use case from its required dependencies.
func NewIssueService(repository ports.IssueRepository, source clock.Clock, generator IDGenerator) (*IssueService, error) {
	if repository == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "issue repository is required", false)
	}
	if source == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "issue clock is required", false)
	}
	if generator == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "issue ID generator is required", false)
	}
	return &IssueService{repository: repository, clock: source, ids: generator}, nil
}

// CreateIssue validates input, generates the issue ID before the write
// transaction, and persists the issue through its repository.
func (service *IssueService) CreateIssue(ctx context.Context, input domain.CreateIssueInput) (CreateIssueResult, error) {
	normalized, err := input.Validate()
	if err != nil {
		return CreateIssueResult{}, err
	}
	id, err := service.ids.New()
	if err != nil {
		return CreateIssueResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate issue identifier", false)
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return CreateIssueResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate issue identifier", false)
	}
	issue, err := service.repository.CreateIssue(ctx, ports.CreateIssueCommand{
		ID:        id,
		Input:     normalized,
		CreatedAt: service.clock.Now().UTC(),
	})
	if err != nil {
		return CreateIssueResult{}, err
	}
	return CreateIssueResult{
		ID:         issue.ID,
		DisplayID:  issue.DisplayID,
		SequenceNo: issue.SequenceNo,
		Issue:      issue,
	}, nil
}

// UpdateIssue validates and normalizes a patch before its one transactional
// conditional persistence operation.
func (service *IssueService) UpdateIssue(ctx context.Context, input domain.UpdateIssueInput) (UpdateIssueResult, error) {
	normalized, err := input.Validate()
	if err != nil {
		return UpdateIssueResult{}, err
	}
	identifier, err := domain.ParseIssueIdentifier(normalized.IssueID)
	if err != nil {
		return UpdateIssueResult{}, err
	}
	result, err := service.repository.UpdateIssue(ctx, ports.UpdateIssueCommand{
		Identifier:      identifier,
		ExpectedVersion: normalized.ExpectedVersion,
		Changes:         normalized.Changes,
		UpdatedAt:       service.clock.Now().UTC(),
	})
	if err != nil {
		return UpdateIssueResult{}, err
	}
	return UpdateIssueResult{
		Issue:         result.Issue,
		ChangedFields: append([]string(nil), result.ChangedFields...),
	}, nil
}

// GetIssue validates an internal or display issue identifier and returns the
// current base issue projection. Archived issues remain visible.
func (service *IssueService) GetIssue(ctx context.Context, identifier string) (domain.Issue, error) {
	normalized, err := domain.ParseIssueIdentifier(identifier)
	if err != nil {
		return domain.Issue{}, err
	}
	return service.repository.GetIssue(ctx, normalized)
}
