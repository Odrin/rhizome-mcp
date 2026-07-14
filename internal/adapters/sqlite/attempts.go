package sqlite

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

// AttemptRepository is the SQLite implementation of ports.AttemptRepository.
type AttemptRepository struct{ db *DB }

func NewAttemptRepository(database *DB) (*AttemptRepository, error) {
	if database == nil {
		return nil, domain.NewError(domain.CodeStorageConfiguration, "attempt database is required", false)
	}
	return &AttemptRepository{db: database}, nil
}

func (repository *AttemptRepository) ClaimIssue(ctx context.Context, command ports.ClaimIssueCommand) (ports.ClaimIssueResult, error) {
	if _, err := ids.ParseStrict(command.AttemptID); err != nil || len(command.TokenHash) != 32 || command.LeaseDuration <= 0 {
		return ports.ClaimIssueResult{}, domain.NewError(domain.CodeInvalidArgument, "attempt claim command is invalid", false)
	}
	now := command.OccurredAt.UTC()
	timestamp := now.Format(time.RFC3339Nano)
	expires := now.Add(command.LeaseDuration).UTC()
	expiresTimestamp := expires.Format(time.RFC3339Nano)
	var result ports.ClaimIssueResult
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		issue, err := loadIssueForMutation(ctx, tx, command.Identifier)
		if err != nil {
			return err
		}
		if err := expireAttemptsForIssue(ctx, tx, issue.ID, now); err != nil {
			return err
		}
		if issue.ArchivedAt != nil {
			return domain.NewError(domain.CodeIssueArchived, "issue is archived", false)
		}
		if issue.Type != domain.TypeTask && issue.Type != domain.TypeBug {
			return domain.NewError(domain.CodeInvalidArgument, "issue type is not executable", false,
				domain.Detail{Field: "issue_id", Code: "NOT_EXECUTABLE"})
		}
		var blocked bool
		if err := tx.QueryRowContext(ctx, `SELECT `+issueUnresolvedBlockerCountSQL+` > 0 FROM issues WHERE id = ?`, issue.ID).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			return domain.NewError(domain.CodeInvalidArgument, "issue has unresolved blockers", false,
				domain.Detail{Field: "issue_id", Code: "BLOCKED"})
		}
		var kind domain.AttemptKind
		switch issue.Status {
		case domain.StatusReady:
			kind = domain.AttemptKindWork
		case domain.StatusReview:
			kind = domain.AttemptKindReview
		default:
			return domain.NewError(domain.CodeInvalidArgument, "issue is not claimable", false,
				domain.Detail{Field: "issue_id", Code: "NOT_CLAIMABLE"})
		}
		var active bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_attempts WHERE issue_id = ? AND status = 'active')`, issue.ID).Scan(&active); err != nil {
			return err
		}
		if active {
			return domain.NewError(domain.CodeActiveAttemptExists, "issue has an active work attempt", false)
		}
		var latestEventID int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM issue_events`).Scan(&latestEventID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(
			id, issue_id, session_id, agent_label, kind, status, issue_version_at_start,
			context_event_id_at_start, lease_token_hash, lease_expires_at, started_at,
			last_heartbeat_at, finished_at
		) VALUES (?, ?, NULL, NULL, ?, 'active', ?, ?, ?, ?, ?, ?, NULL)`,
			command.AttemptID, issue.ID, kind, issue.Version, latestEventID, command.TokenHash,
			expiresTimestamp, timestamp, timestamp); err != nil {
			if isActiveAttemptConstraint(err) {
				return domain.NewError(domain.CodeActiveAttemptExists, "issue has an active work attempt", false)
			}
			return err
		}
		payload, err := json.Marshal(struct {
			AttemptID string             `json:"attempt_id"`
			Kind      domain.AttemptKind `json:"kind"`
		}{AttemptID: command.AttemptID, Kind: kind})
		if err != nil {
			return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode attempt start event", false)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_events(
			issue_id, event_type, session_id, attempt_id, payload, created_at
		) VALUES (?, 'attempt_started', NULL, ?, ?, ?)`, issue.ID, command.AttemptID, string(payload), timestamp); err != nil {
			return err
		}
		result.Issue = issue
		result.Attempt = domain.WorkAttempt{
			ID: command.AttemptID, IssueID: issue.ID, Kind: kind, Status: domain.AttemptStatusActive,
			IssueVersionAtStart: issue.Version, ContextEventIDAtStart: latestEventID,
			LeaseExpiresAt: expires, StartedAt: now, LastHeartbeatAt: now,
		}
		return nil
	})
	if err != nil {
		return ports.ClaimIssueResult{}, err
	}
	return result, nil
}

func (repository *AttemptRepository) RenewAttempt(ctx context.Context, command ports.RenewAttemptCommand) (ports.RenewAttemptResult, error) {
	if _, err := ids.ParseStrict(command.AttemptID); err != nil || len(command.TokenHash) != 32 || command.LeaseDuration <= 0 {
		return ports.RenewAttemptResult{}, domain.NewError(domain.CodeInvalidArgument, "attempt renewal command is invalid", false)
	}
	now := command.OccurredAt.UTC()
	timestamp := now.Format(time.RFC3339Nano)
	expires := now.Add(command.LeaseDuration).UTC()
	var result ports.RenewAttemptResult
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		var status, leaseExpiresAt string
		var tokenHash []byte
		err := tx.QueryRowContext(ctx, `SELECT status, lease_token_hash, lease_expires_at FROM work_attempts WHERE id = ?`, command.AttemptID).
			Scan(&status, &tokenHash, &leaseExpiresAt)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.NewError(domain.CodeAttemptNotFound, "attempt not found", false)
		}
		if err != nil {
			return err
		}
		if status != string(domain.AttemptStatusActive) {
			return domain.NewError(domain.CodeAttemptNotActive, "attempt is not active", false)
		}
		leaseExpiry, err := parseIssueTimestamp("lease_expires_at", leaseExpiresAt)
		if err != nil {
			return err
		}
		if !leaseExpiry.After(now) {
			if err := expireAttempt(ctx, tx, command.AttemptID, now); err != nil {
				return err
			}
			return domain.NewError(domain.CodeLeaseExpired, "attempt lease has expired", false)
		}
		if subtle.ConstantTimeCompare(tokenHash, command.TokenHash) != 1 {
			return domain.NewError(domain.CodeInvalidLeaseToken, "lease token is invalid", false)
		}
		res, err := tx.ExecContext(ctx, `UPDATE work_attempts
			SET lease_expires_at = ?, last_heartbeat_at = ?
			WHERE id = ? AND status = 'active'`, expires.Format(time.RFC3339Nano), timestamp, command.AttemptID)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return domain.NewError(domain.CodeAttemptNotActive, "attempt is not active", false)
		}
		result = ports.RenewAttemptResult{LeaseExpiresAt: expires, ServerTime: now}
		return nil
	})
	if err != nil {
		return ports.RenewAttemptResult{}, err
	}
	return result, nil
}

// expireAttemptsForIssue releases only expired active attempts. Its conditional
// update makes repeated lazy cleanup safe and ensures exactly one expiry event.
func expireAttemptsForIssue(ctx context.Context, tx Executor, issueID string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM work_attempts
		WHERE issue_id = ? AND status = 'active' AND lease_expires_at <= ?`, issueID, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	var attemptIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		attemptIDs = append(attemptIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range attemptIDs {
		if err := expireAttempt(ctx, tx, id, now); err != nil {
			return err
		}
	}
	return nil
}

func expireAttempt(ctx context.Context, tx Executor, attemptID string, now time.Time) error {
	timestamp := now.UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `UPDATE work_attempts SET status = 'expired', finished_at = ?
		WHERE id = ? AND status = 'active' AND lease_expires_at <= ?`, timestamp, attemptID, timestamp)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return nil
	}
	var issueID string
	if err := tx.QueryRowContext(ctx, `SELECT issue_id FROM work_attempts WHERE id = ?`, attemptID).Scan(&issueID); err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		AttemptID string `json:"attempt_id"`
	}{AttemptID: attemptID})
	if err != nil {
		return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode attempt expiry event", false)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_type, session_id, attempt_id, payload, created_at)
		VALUES (?, 'attempt_expired', NULL, ?, ?, ?)`, issueID, attemptID, string(payload), timestamp)
	return err
}

func isActiveAttemptConstraint(err error) bool {
	code, ok := sqliteCode(err)
	return ok && code&0xff == 19
}

var _ ports.AttemptRepository = (*AttemptRepository)(nil)
