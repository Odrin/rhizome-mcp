package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

// unitOfWork adapts one write transaction's Executor to ports.UnitOfWork.
type unitOfWork struct{ tx Executor }

func (uow unitOfWork) LookupIdempotency(ctx context.Context, operation, key string) ([]byte, string, bool, error) {
	var hash []byte
	var response string
	err := uow.tx.QueryRowContext(ctx, `SELECT request_hash, response_json FROM idempotency_records
		WHERE operation = ? AND idempotency_key = ?`, operation, key).Scan(&hash, &response)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	return hash, response, true, nil
}

func (uow unitOfWork) StoreIdempotency(ctx context.Context, operation, key string, requestHash []byte, responseJSON []byte, occurredAt time.Time) error {
	_, err := uow.tx.ExecContext(ctx, `INSERT INTO idempotency_records(
		idempotency_key, operation, request_hash, response_json, created_at
	) VALUES (?, ?, ?, ?, ?)`, key, operation, requestHash, string(responseJSON), formatStorageTime(occurredAt))
	return err
}

func (uow unitOfWork) AppendIssueEvent(ctx context.Context, issueID *string, eventType string, sessionID, attemptID *string, payload []byte, occurredAt time.Time) (int64, error) {
	var id int64
	err := uow.tx.QueryRowContext(ctx, `INSERT INTO issue_events(
		issue_id, event_type, session_id, attempt_id, payload, created_at
	) VALUES (?, ?, ?, ?, ?, ?) RETURNING id`,
		nullableStringValuePtr(issueID), eventType, nullableStringValuePtr(sessionID), nullableStringValuePtr(attemptID), string(payload), formatStorageTime(occurredAt),
	).Scan(&id)
	return id, err
}

func (uow unitOfWork) ConditionalUpdateIssue(ctx context.Context, issueID string, expectedVersion int64, setClause string, args ...any) error {
	fullArgs := append(append([]any{}, args...), issueID, expectedVersion)
	res, err := uow.tx.ExecContext(ctx, `UPDATE issues SET `+setClause+` WHERE id = ? AND version = ? AND archived_at IS NULL`, fullArgs...)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	return classifyConditionalUpdateFailure(ctx, uow.tx, issueID)
}

func (uow unitOfWork) LoadIssueForMutation(ctx context.Context, identifier domain.IssueIdentifier) (domain.Issue, error) {
	return loadIssueForMutation(ctx, uow.tx, identifier)
}

// executor returns the underlying transaction executor. This is unexported and
// should only be used by same-package repository methods that need to call
// package-private helpers taking an Executor parameter.
func (uow unitOfWork) executor() Executor {
	return uow.tx
}

// Transactor is the SQLite implementation of ports.Transactor, backed by
// DB.Write -- see ports.Transactor's doc comment for the retry contract
// every fn passed to RunWrite must honor.
type Transactor struct{ db *DB }

// NewTransactor returns a transactor backed by database.
func NewTransactor(database *DB) (*Transactor, error) {
	if database == nil {
		return nil, domain.NewError(domain.CodeStorageConfiguration, "transactor database is required", false)
	}
	return &Transactor{db: database}, nil
}

func (t *Transactor) RunWrite(ctx context.Context, fn func(context.Context, ports.UnitOfWork) error) error {
	return t.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		return fn(ctx, unitOfWork{tx: tx})
	})
}
