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

type DecisionRepository struct {
	db *DB
}

func NewDecisionRepository(database *DB) (*DecisionRepository, error) {
	if database == nil {
		return nil, domain.NewError(domain.CodeStorageConfiguration, "decision database is required", false)
	}
	return &DecisionRepository{db: database}, nil
}

func (repository *DecisionRepository) RecordDecision(ctx context.Context, command ports.RecordDecisionCommand) (domain.RecordDecisionResult, error) {
	if _, err := ids.ParseStrict(command.ID); err != nil {
		return domain.RecordDecisionResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate decision identifier", false)
	}
	input, err := command.Input.Validate()
	if err != nil {
		return domain.RecordDecisionResult{}, err
	}
	if command.OccurredAt.IsZero() {
		return domain.RecordDecisionResult{}, domain.NewError(domain.CodeInvalidArgument, "decision command is invalid", false)
	}

	now := command.OccurredAt.UTC()
	timestamp := now.Format(time.RFC3339Nano)
	var result domain.RecordDecisionResult
	err = repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		var issueID *string
		if input.IssueID != nil {
			identifier, err := domain.ParseIssueIdentifier(*input.IssueID)
			if err != nil {
				return err
			}
			issue, err := loadIssueForMutation(ctx, tx, identifier)
			if err != nil {
				return err
			}
			if issue.ArchivedAt != nil {
				return domain.NewError(domain.CodeIssueArchived, "issue is archived", false)
			}
			issueID = &issue.ID
		}

		var predecessor *domain.Decision
		if input.SupersedesID != nil {
			loaded, err := loadDecision(ctx, tx, *input.SupersedesID)
			if err == sql.ErrNoRows {
				return decisionSupersessionError("NOT_FOUND", "predecessor decision was not found")
			}
			if err != nil {
				return err
			}
			if !sameDecisionScope(issueID, loaded.IssueID) {
				return decisionSupersessionError("SCOPE_MISMATCH", "predecessor decision has a different scope")
			}
			if loaded.Status != domain.DecisionStatusActive {
				return decisionSupersessionError("NOT_ACTIVE", "predecessor decision is not active")
			}
			predecessor = &loaded
		}

		if _, err := tx.ExecContext(ctx, `INSERT INTO decisions(
			id, issue_id, title, summary, content, status, supersedes_id, created_by_session_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			command.ID, nullableStringValuePtr(issueID), input.Title, input.Summary, input.Content, input.Status,
			nullableStringValuePtr(input.SupersedesID), nullableStringValuePtr(input.SessionID), timestamp); err != nil {
			return err
		}

		if predecessor != nil {
			updated, err := tx.ExecContext(ctx, `UPDATE decisions SET status = 'superseded'
				WHERE id = ? AND status = 'active'`, predecessor.ID)
			if err != nil {
				return err
			}
			count, err := updated.RowsAffected()
			if err != nil {
				return err
			}
			if count != 1 {
				return decisionSupersessionError("NOT_ACTIVE", "predecessor decision is not active")
			}
		}

		payloadFields := struct {
			DecisionID   string                `json:"decision_id"`
			Status       domain.DecisionStatus `json:"status"`
			SupersedesID *string               `json:"supersedes_id,omitempty"`
		}{DecisionID: command.ID, Status: input.Status, SupersedesID: input.SupersedesID}
		payload, err := json.Marshal(payloadFields)
		if err != nil {
			return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode decision event", false)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_events(
			issue_id, event_type, session_id, attempt_id, payload, created_at
		) VALUES (?, 'decision_recorded', ?, NULL, ?, ?)`,
			nullableStringValuePtr(issueID), nullableStringValuePtr(input.SessionID), string(payload), timestamp); err != nil {
			return err
		}

		decision, err := loadDecision(ctx, tx, command.ID)
		if err != nil {
			return err
		}
		result.Decision = decision
		if predecessor != nil {
			id := predecessor.ID
			result.SupersededDecisionID = &id
		}
		return nil
	})
	if err != nil {
		return domain.RecordDecisionResult{}, err
	}
	result.Decision = domain.CloneDecision(result.Decision)
	if result.SupersededDecisionID != nil {
		id := *result.SupersededDecisionID
		result.SupersededDecisionID = &id
	}
	return result, nil
}

func sameDecisionScope(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func decisionSupersessionError(code, message string) error {
	return domain.NewError(domain.CodeInvalidArgument, message, false,
		domain.Detail{Field: "supersedes_id", Code: code})
}

func loadDecision(ctx context.Context, query Queryer, id string) (domain.Decision, error) {
	return scanDecision(query.QueryRowContext(ctx, `SELECT id, issue_id, title, summary, content,
		status, supersedes_id, created_by_session_id, created_at FROM decisions WHERE id = ?`, id))
}

func scanDecision(row *sql.Row) (domain.Decision, error) {
	var (
		id, title, summary, content, status, createdAt string
		issueID, supersedesID, sessionID               sql.NullString
	)
	if err := row.Scan(&id, &issueID, &title, &summary, &content, &status, &supersedesID, &sessionID, &createdAt); err != nil {
		if err == sql.ErrNoRows {
			return domain.Decision{}, err
		}
		return domain.Decision{}, corruptDecision(err)
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return domain.Decision{}, corruptDecisionField(err, "id", "INVALID_ULID")
	}
	issue, err := parseNullableDecisionID("issue_id", issueID)
	if err != nil {
		return domain.Decision{}, err
	}
	if err := validateStoredDecisionText("title", title, domain.MaxTitleRunes, true); err != nil {
		return domain.Decision{}, err
	}
	if err := validateStoredDecisionText("summary", summary, domain.MaxDecisionSummaryRunes, true); err != nil {
		return domain.Decision{}, err
	}
	if err := validateStoredDecisionText("content", content, domain.MaxDecisionContentRunes, false); err != nil {
		return domain.Decision{}, err
	}
	decisionStatus := domain.DecisionStatus(status)
	if !decisionStatus.Valid() {
		return domain.Decision{}, corruptDecisionField(nil, "status", "INVALID_VALUE")
	}
	supersedes, err := parseNullableDecisionID("supersedes_id", supersedesID)
	if err != nil {
		return domain.Decision{}, err
	}
	session, err := parseNullableDecisionID("created_by_session_id", sessionID)
	if err != nil {
		return domain.Decision{}, err
	}
	created, err := parseIssueTimestamp("created_at", createdAt)
	if err != nil {
		return domain.Decision{}, corruptDecision(err)
	}
	return domain.Decision{
		ID: id, IssueID: issue, Title: title, Summary: summary, Content: content,
		Status: decisionStatus, SupersedesID: supersedes, CreatedBySessionID: session,
		CreatedAt: created,
	}, nil
}

func parseNullableDecisionID(field string, value sql.NullString) (*string, error) {
	if !value.Valid {
		return nil, nil
	}
	if _, err := ids.ParseStrict(value.String); err != nil {
		return nil, corruptDecisionField(err, field, "INVALID_ULID")
	}
	result := value.String
	return &result, nil
}

func validateStoredDecisionText(field, value string, maximum int, required bool) error {
	if err := domain.ValidateText(field, value, maximum); err != nil {
		return corruptDecision(err)
	}
	if required && strings.TrimSpace(value) == "" {
		return corruptDecisionField(nil, field, "REQUIRED")
	}
	return nil
}

func corruptDecision(cause error) error {
	return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored decision is invalid", false)
}

func corruptDecisionField(cause error, field, code string) error {
	return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored decision is invalid", false,
		domain.Detail{Field: field, Code: code})
}

var _ ports.DecisionRepository = (*DecisionRepository)(nil)
