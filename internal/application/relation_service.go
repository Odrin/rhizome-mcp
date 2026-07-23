package application

import (
	"context"
	"crypto/sha256"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

// RelationService manages directed and symmetric issue relations.
type RelationService struct {
	repository ports.RelationRepository
	clock      clock.Clock
	ids        IDGenerator
}

// ManageIssueRelationResult is the result of one relation mutation.
type ManageIssueRelationResult = ports.ManageIssueRelationResult

// NewRelationService composes the relation use case from required dependencies.
func NewRelationService(repository ports.RelationRepository, source clock.Clock, generator IDGenerator) (*RelationService, error) {
	if repository == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "relation repository is required", false)
	}
	if source == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "relation clock is required", false)
	}
	if generator == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "relation ID generator is required", false)
	}
	return &RelationService{repository: repository, clock: source, ids: generator}, nil
}

// ManageIssueRelation validates identifiers and generates a relation ID only
// for adds before delegating one atomic mutation to storage.
func (service *RelationService) ManageIssueRelation(ctx context.Context, input domain.ManageIssueRelationInput) (ManageIssueRelationResult, error) {
	normalized, err := input.Validate()
	if err != nil {
		return ManageIssueRelationResult{}, err
	}
	source, err := domain.ParseIssueIdentifier(normalized.SourceIssueID)
	if err != nil {
		return ManageIssueRelationResult{}, err
	}
	target, err := domain.ParseIssueIdentifier(normalized.TargetIssueID)
	if err != nil {
		return ManageIssueRelationResult{}, err
	}

	var idempotencyKey string
	var requestHash []byte
	if normalized.IdempotencyKey != nil {
		canonical, err := domain.CanonicalManageIssueRelationRequest(normalized)
		if err != nil {
			return ManageIssueRelationResult{}, domain.WrapError(err, domain.CodeStorageFailure, "cannot encode relation request", false)
		}
		hash := sha256.Sum256(canonical)
		requestHash = append([]byte(nil), hash[:]...)
		idempotencyKey = *normalized.IdempotencyKey
		result, found, err := service.repository.LookupManageIssueRelation(ctx, idempotencyKey, requestHash)
		if err != nil {
			return ManageIssueRelationResult{}, err
		}
		if found {
			return result, nil
		}
	}

	command := ports.ManageIssueRelationCommand{
		Action:           normalized.Action,
		SourceIdentifier: source,
		TargetIdentifier: target,
		RelationType:     normalized.RelationType,
		OccurredAt:       service.clock.Now().UTC(),
		IdempotencyKey:   idempotencyKey,
		RequestHash:      requestHash,
	}
	if normalized.Action == domain.RelationActionAdd {
		command.RelationID, err = service.ids.New()
		if err != nil {
			return ManageIssueRelationResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate relation identifier", false)
		}
		if _, err := ids.ParseStrict(command.RelationID); err != nil {
			return ManageIssueRelationResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate relation identifier", false)
		}
	}
	return service.repository.ManageIssueRelation(ctx, command)
}
