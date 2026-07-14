package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

// CommentRepository is the SQLite implementation of ports.CommentRepository.
type CommentRepository struct {
	db *DB
}

// NewCommentRepository returns a comment repository backed by database.
func NewCommentRepository(database *DB) (*CommentRepository, error) {
	if database == nil {
		return nil, domain.NewError(domain.CodeStorageConfiguration, "comment database is required", false)
	}
	return &CommentRepository{db: database}, nil
}

// AddComment atomically inserts one comment and its compact issue event.
func (repository *CommentRepository) AddComment(ctx context.Context, command ports.AddCommentCommand) (domain.Comment, error) {
	if _, err := ids.ParseStrict(command.ID); err != nil {
		return domain.Comment{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate comment identifier", false)
	}
	input, err := command.Input.Validate()
	if err != nil {
		return domain.Comment{}, err
	}
	identifier, err := domain.ParseIssueIdentifier(input.IssueID)
	if err != nil {
		return domain.Comment{}, err
	}
	if command.OccurredAt.IsZero() {
		return domain.Comment{}, domain.NewError(domain.CodeInvalidArgument, "comment command is invalid", false)
	}

	now := command.OccurredAt.UTC()
	timestamp := now.Format(time.RFC3339Nano)
	var comment domain.Comment
	err = repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		issue, err := loadIssueForMutation(ctx, tx, identifier)
		if err != nil {
			return err
		}
		if issue.ArchivedAt != nil {
			return domain.NewError(domain.CodeIssueArchived, "issue is archived", false)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO comments(
			id, issue_id, content, created_by_session_id, author_label, created_at, edited_at
		) VALUES (?, ?, ?, ?, NULL, ?, NULL)`,
			command.ID, issue.ID, input.Content, nullableStringValuePtr(input.SessionID), timestamp,
		); err != nil {
			return err
		}

		payload, err := json.Marshal(struct {
			CommentID string `json:"comment_id"`
		}{CommentID: command.ID})
		if err != nil {
			return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode comment event", false)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_events(
			issue_id, event_type, session_id, attempt_id, payload, created_at
		) VALUES (?, 'comment_added', ?, NULL, ?, ?)`,
			issue.ID, nullableStringValuePtr(input.SessionID), string(payload), timestamp,
		); err != nil {
			return err
		}

		comment, err = loadComment(ctx, tx, command.ID)
		return err
	})
	if err != nil {
		return domain.Comment{}, err
	}
	return domain.CloneComment(comment), nil
}

func loadComment(ctx context.Context, query Queryer, id string) (domain.Comment, error) {
	return scanComment(query.QueryRowContext(ctx, `SELECT id, issue_id, content,
		created_by_session_id, author_label, created_at, edited_at
		FROM comments WHERE id = ?`, id))
}

func scanComment(row *sql.Row) (domain.Comment, error) {
	var (
		id, issueID, content, createdAt  string
		sessionID, authorLabel, editedAt sql.NullString
	)
	if err := row.Scan(&id, &issueID, &content, &sessionID, &authorLabel, &createdAt, &editedAt); err != nil {
		if err != sql.ErrNoRows {
			return domain.Comment{}, corruptComment(err)
		}
		return domain.Comment{}, err
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return domain.Comment{}, corruptCommentField(err, "id", "INVALID_ULID")
	}
	if _, err := ids.ParseStrict(issueID); err != nil {
		return domain.Comment{}, corruptCommentField(err, "issue_id", "INVALID_ULID")
	}
	if err := validateStoredCommentText("content", content); err != nil {
		return domain.Comment{}, err
	}
	created, err := parseIssueTimestamp("created_at", createdAt)
	if err != nil {
		return domain.Comment{}, err
	}
	edited, err := parseNullableIssueTimestamp("edited_at", editedAt)
	if err != nil {
		return domain.Comment{}, err
	}
	createdBySessionID, err := parseNullableCommentID("created_by_session_id", sessionID)
	if err != nil {
		return domain.Comment{}, err
	}
	if authorLabel.Valid {
		if err := domain.ValidateText("author_label", authorLabel.String, -1); err != nil {
			return domain.Comment{}, corruptComment(err)
		}
	}
	return domain.Comment{
		ID: id, IssueID: issueID, Content: content,
		CreatedBySessionID: createdBySessionID, AuthorLabel: nullableStringPointer(authorLabel),
		CreatedAt: created, EditedAt: edited,
	}, nil
}

func parseNullableCommentID(field string, value sql.NullString) (*string, error) {
	if !value.Valid {
		return nil, nil
	}
	if _, err := ids.ParseStrict(value.String); err != nil {
		return nil, corruptCommentField(err, field, "INVALID_ULID")
	}
	result := value.String
	return &result, nil
}

func validateStoredCommentText(field, value string) error {
	if err := domain.ValidateText(field, value, domain.MaxCommentRunes); err != nil {
		return corruptComment(err)
	}
	if field == "content" && strings.TrimSpace(value) == "" {
		return corruptCommentField(nil, field, "REQUIRED")
	}
	return nil
}

func corruptComment(cause error) error {
	return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored comment is invalid", false)
}

func corruptCommentField(cause error, field, code string) error {
	return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored comment is invalid", false,
		domain.Detail{Field: field, Code: code})
}

var _ ports.CommentRepository = (*CommentRepository)(nil)
