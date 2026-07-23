package application

import (
	"context"
	"crypto/sha256"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

// CommentService manages append-only issue comments.
type CommentService struct {
	repository ports.CommentRepository
	clock      clock.Clock
	ids        IDGenerator
}

// NewCommentService composes the comment use case from required dependencies.
func NewCommentService(repository ports.CommentRepository, source clock.Clock, generator IDGenerator) (*CommentService, error) {
	if repository == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "comment repository is required", false)
	}
	if source == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "comment clock is required", false)
	}
	if generator == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "comment ID generator is required", false)
	}
	return &CommentService{repository: repository, clock: source, ids: generator}, nil
}

// AddComment validates the request, allocates one canonical ID, and persists
// the comment through one repository operation.
func (service *CommentService) AddComment(ctx context.Context, input domain.AddCommentInput) (domain.Comment, error) {
	normalized, err := input.Validate()
	if err != nil {
		return domain.Comment{}, err
	}
	var idempotencyKey string
	var requestHash []byte
	if normalized.IdempotencyKey != nil {
		canonical, err := domain.CanonicalAddCommentRequest(normalized)
		if err != nil {
			return domain.Comment{}, domain.WrapError(err, domain.CodeStorageFailure, "cannot encode add comment request", false)
		}
		hash := sha256.Sum256(canonical)
		requestHash = append([]byte(nil), hash[:]...)
		idempotencyKey = *normalized.IdempotencyKey
		comment, found, err := service.repository.LookupAddComment(ctx, idempotencyKey, requestHash)
		if err != nil {
			return domain.Comment{}, err
		}
		if found {
			return comment, nil
		}
	}
	id, err := service.ids.New()
	if err != nil {
		return domain.Comment{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate comment identifier", false)
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return domain.Comment{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate comment identifier", false)
	}
	return service.repository.AddComment(ctx, ports.AddCommentCommand{
		ID: id, Input: normalized, OccurredAt: service.clock.Now().UTC(),
		IdempotencyKey: idempotencyKey, RequestHash: requestHash,
	})
}
