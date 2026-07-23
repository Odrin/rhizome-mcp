package ports

import (
	"context"
	"time"

	"rhizome-mcp/internal/domain"
)

// AddCommentCommand contains a validated comment and application-generated
// identity and timestamp values.
type AddCommentCommand struct {
	ID             string
	Input          domain.AddCommentInput
	OccurredAt     time.Time
	IdempotencyKey string
	RequestHash    []byte
}

// CommentRepository persists one append-only comment and its issue event.
type CommentRepository interface {
	AddComment(context.Context, AddCommentCommand) (domain.Comment, error)
	LookupAddComment(context.Context, string, []byte) (domain.Comment, bool, error)
}
