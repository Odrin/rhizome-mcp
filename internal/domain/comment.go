package domain

import (
	"encoding/json"
	"strings"
	"time"
)

// Comment is an append-only issue comment.
type Comment struct {
	ID                 string     `json:"id"`
	IssueID            string     `json:"issue_id"`
	Content            string     `json:"content"`
	CreatedBySessionID *string    `json:"created_by_session_id"`
	AuthorLabel        *string    `json:"author_label"`
	CreatedAt          time.Time  `json:"created_at"`
	EditedAt           *time.Time `json:"edited_at"`
}

// AddCommentInput contains the caller-owned values for one new comment.
type AddCommentInput struct {
	IssueID        string
	Content        string
	SessionID      *string
	IdempotencyKey *string
}

// Validate checks and normalizes one comment request. Content is validated
// without trimming so Markdown whitespace is retained exactly.
func (input AddCommentInput) Validate() (AddCommentInput, error) {
	identifier, err := ParseIssueIdentifier(input.IssueID)
	if err != nil {
		return AddCommentInput{}, err
	}
	if err := ValidateText("content", input.Content, MaxCommentRunes); err != nil {
		return AddCommentInput{}, err
	}
	if strings.TrimSpace(input.Content) == "" {
		return AddCommentInput{}, validationError("content", "REQUIRED", "is required")
	}
	sessionID, err := copyOptionalSessionID(input.SessionID)
	if err != nil {
		return AddCommentInput{}, err
	}
	var idempotencyKey *string
	if input.IdempotencyKey != nil {
		if err := ValidateText("idempotency_key", *input.IdempotencyKey, MaxIdempotencyKeyRunes); err != nil {
			return AddCommentInput{}, err
		}
		key := strings.TrimSpace(*input.IdempotencyKey)
		if key == "" {
			return AddCommentInput{}, validationError("idempotency_key", "REQUIRED", "must not be blank")
		}
		idempotencyKey = &key
	}
	return AddCommentInput{IssueID: identifier.Value, Content: input.Content, SessionID: sessionID, IdempotencyKey: idempotencyKey}, nil
}

// CanonicalAddCommentRequest returns deterministic JSON for a normalized
// add-comment request. The idempotency key and transient session identity are
// intentionally excluded.
func CanonicalAddCommentRequest(input AddCommentInput) ([]byte, error) {
	request := struct {
		IssueID string `json:"issue_id"`
		Content string `json:"content"`
	}{IssueID: input.IssueID, Content: input.Content}
	return json.Marshal(request)
}

// CloneComment returns a comment whose pointer fields do not share storage
// with the source value.
func CloneComment(comment Comment) Comment {
	comment.CreatedBySessionID = copyOptionalString(comment.CreatedBySessionID)
	comment.AuthorLabel = copyOptionalString(comment.AuthorLabel)
	if comment.EditedAt != nil {
		editedAt := *comment.EditedAt
		comment.EditedAt = &editedAt
	}
	return comment
}

func copyOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
