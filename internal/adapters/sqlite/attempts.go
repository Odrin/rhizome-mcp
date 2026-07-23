package sqlite

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

// AttemptRepository is the SQLite implementation of ports.AttemptRepository.
type AttemptRepository struct{ db *DB }

const (
	claimIssueOperation      = "claim_issue"
	finishAttemptOperation   = "finish_attempt"
	saveAttemptNoteOperation = "save_attempt_note"
)

func NewAttemptRepository(database *DB) (*AttemptRepository, error) {
	if database == nil {
		return nil, domain.NewError(domain.CodeStorageConfiguration, "attempt database is required", false)
	}
	return &AttemptRepository{db: database}, nil
}

// LookupClaimIssue serves a replay before the lease token is generated and the
// attempt ID is allocated. Claim still repeats the lookup in the writer transaction to close the race.
func (repository *AttemptRepository) LookupClaimIssue(ctx context.Context, key string, hash []byte) (ports.ClaimIssueResult, bool, error) {
	var result ports.ClaimIssueResult
	var found bool
	err := repository.db.Read(ctx, func(ctx context.Context, query Queryer) error {
		var savedHash []byte
		var savedResponse string
		err := query.QueryRowContext(ctx, `SELECT request_hash, response_json FROM idempotency_records
			WHERE operation = ? AND idempotency_key = ?`, claimIssueOperation, key).Scan(&savedHash, &savedResponse)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if !bytes.Equal(savedHash, hash) {
			return domain.NewError(domain.CodeIdempotencyConflict, "idempotency key was used with a different request", false,
				domain.Detail{Field: "idempotency_key", Code: domain.CodeIdempotencyConflict})
		}
		if err := json.Unmarshal([]byte(savedResponse), &result); err != nil {
			return domain.WrapError(err, domain.CodeStorageCorrupt, "stored idempotency response is invalid", false)
		}
		found = true
		return nil
	})
	return result, found, err
}

func (repository *AttemptRepository) ClaimIssue(ctx context.Context, command ports.ClaimIssueCommand) (ports.ClaimIssueResult, error) {
	if !validAttemptSessionID(command.SessionID) {
		return ports.ClaimIssueResult{}, domain.NewError(domain.CodeInvalidArgument, "attempt claim command is invalid", false)
	}
	if _, err := ids.ParseStrict(command.AttemptID); err != nil || len(command.TokenHash) != 32 || command.LeaseDuration <= 0 {
		return ports.ClaimIssueResult{}, domain.NewError(domain.CodeInvalidArgument, "attempt claim command is invalid", false)
	}
	now := command.OccurredAt.UTC()
	timestamp := now.Format(time.RFC3339Nano)
	expires := now.Add(command.LeaseDuration).UTC()
	expiresTimestamp := expires.Format(time.RFC3339Nano)
	var result ports.ClaimIssueResult
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		if command.IdempotencyKey != "" {
			var savedHash []byte
			var savedResponse string
			err := tx.QueryRowContext(ctx, `SELECT request_hash, response_json FROM idempotency_records
				WHERE operation = ? AND idempotency_key = ?`, claimIssueOperation, command.IdempotencyKey).Scan(&savedHash, &savedResponse)
			switch {
			case err == nil:
				if !bytes.Equal(savedHash, command.RequestHash) {
					return domain.NewError(domain.CodeIdempotencyConflict, "idempotency key was used with a different request", false,
						domain.Detail{Field: "idempotency_key", Code: domain.CodeIdempotencyConflict})
				}
				if err := json.Unmarshal([]byte(savedResponse), &result); err != nil {
					return domain.WrapError(err, domain.CodeStorageCorrupt, "stored idempotency response is invalid", false)
				}
				return nil
			case err == sql.ErrNoRows:
			default:
				return err
			}
		}
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
		) VALUES (?, ?, ?, NULL, ?, 'active', ?, ?, ?, ?, ?, ?, NULL)`,
			command.AttemptID, issue.ID, nullableStringValuePtr(command.SessionID), kind, issue.Version, latestEventID, command.TokenHash,
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
		) VALUES (?, 'attempt_started', ?, ?, ?, ?)`, issue.ID, nullableStringValuePtr(command.SessionID), command.AttemptID, string(payload), timestamp); err != nil {
			return err
		}
		result.Issue = issue
		result.Attempt = domain.WorkAttempt{
			ID: command.AttemptID, IssueID: issue.ID, SessionID: copyOptionalString(command.SessionID), Kind: kind, Status: domain.AttemptStatusActive,
			IssueVersionAtStart: issue.Version, ContextEventIDAtStart: latestEventID,
			LeaseExpiresAt: expires, StartedAt: now, LastHeartbeatAt: now,
		}
		result.LeaseToken = command.LeaseToken
		if command.IdempotencyKey != "" {
			response, err := json.Marshal(result)
			if err != nil {
				return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode claim response", false)
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO idempotency_records(
				idempotency_key, operation, request_hash, response_json, created_at
			) VALUES (?, ?, ?, ?, ?)`, command.IdempotencyKey, claimIssueOperation, command.RequestHash, string(response), timestamp)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ports.ClaimIssueResult{}, err
	}
	return result, nil
}

func (repository *AttemptRepository) RenewAttempt(ctx context.Context, command ports.RenewAttemptCommand) (ports.RenewAttemptResult, error) {
	if !validAttemptSessionID(command.SessionID) {
		return ports.RenewAttemptResult{}, domain.NewError(domain.CodeInvalidArgument, "attempt renewal command is invalid", false)
	}
	if _, err := ids.ParseStrict(command.AttemptID); err != nil || len(command.TokenHash) != 32 || command.LeaseDuration <= 0 {
		return ports.RenewAttemptResult{}, domain.NewError(domain.CodeInvalidArgument, "attempt renewal command is invalid", false)
	}
	now := command.OccurredAt.UTC()
	timestamp := now.Format(time.RFC3339Nano)
	expires := now.Add(command.LeaseDuration).UTC()
	var result ports.RenewAttemptResult
	var leaseExpired bool
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
			if _, err := expireAttempt(ctx, tx, command.AttemptID, now); err != nil {
				return err
			}
			leaseExpired = true
			return nil
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
	if leaseExpired {
		return ports.RenewAttemptResult{}, domain.NewError(domain.CodeLeaseExpired, "attempt lease has expired", false)
	}
	return result, nil
}

func (repository *AttemptRepository) ExpireAttempts(ctx context.Context, command ports.ExpireAttemptsCommand) (ports.ExpireAttemptsResult, error) {
	if command.OccurredAt.IsZero() {
		return ports.ExpireAttemptsResult{}, domain.NewError(domain.CodeInvalidArgument, "attempt expiry cleanup command timestamp is required", false)
	}
	now := command.OccurredAt.UTC()
	var result ports.ExpireAttemptsResult
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM work_attempts
			WHERE status = 'active' AND lease_expires_at <= ? ORDER BY id ASC`, now.Format(time.RFC3339Nano))
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
			expired, err := expireAttempt(ctx, tx, id, now)
			if err != nil {
				return err
			}
			if expired {
				result.ExpiredAttemptCount++
			}
		}
		return nil
	})
	if err != nil {
		return ports.ExpireAttemptsResult{}, err
	}
	return result, nil
}

// ListActiveAttempts returns a bounded, project-wide projection of currently
// active (leased, unexpired) attempts joined with their issue and, when
// present, the claiming session's label. The result is capped at command.Limit
// regardless of how many issues or attempts exist.
func (repository *AttemptRepository) ListActiveAttempts(ctx context.Context, command ports.ListActiveAttemptsCommand) ([]domain.ActiveAttemptSummary, error) {
	limit := command.Limit
	if limit <= 0 || limit > domain.MaxBoardCollectionLimit {
		limit = domain.MaxBoardCollectionLimit
	}
	now := command.Now.UTC()
	var result []domain.ActiveAttemptSummary
	err := repository.db.Read(ctx, func(ctx context.Context, query Queryer) error {
		rows, err := query.QueryContext(ctx, `SELECT wa.id, wa.issue_id, i.sequence_no, i.title, wa.kind,
				wa.session_id, s.agent_label, wa.started_at, wa.lease_expires_at
			FROM work_attempts AS wa
			JOIN issues AS i ON i.id = wa.issue_id
			LEFT JOIN agent_sessions AS s ON s.id = wa.session_id
			WHERE wa.status = 'active' AND wa.lease_expires_at > ?
			ORDER BY wa.lease_expires_at ASC, wa.id ASC
			LIMIT ?`, now.Format(time.RFC3339Nano), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				id, issueID, title, kindText, startedAt, leaseExpiresAt string
				sequenceNo                                              int64
				sessionID, agentLabel                                   sql.NullString
			)
			if err := rows.Scan(&id, &issueID, &sequenceNo, &title, &kindText, &sessionID, &agentLabel, &startedAt, &leaseExpiresAt); err != nil {
				return domain.WrapError(err, domain.CodeStorageCorrupt, "stored active attempt projection is invalid", false)
			}
			started, err := parseIssueTimestamp("started_at", startedAt)
			if err != nil {
				return err
			}
			leaseExpires, err := parseIssueTimestamp("lease_expires_at", leaseExpiresAt)
			if err != nil {
				return err
			}
			result = append(result, domain.ActiveAttemptSummary{
				AttemptID:      id,
				IssueID:        issueID,
				IssueDisplayID: fmt.Sprintf("ISSUE-%d", sequenceNo),
				IssueTitle:     title,
				Kind:           domain.AttemptKind(kindText),
				SessionID:      nullableStringScan(sessionID),
				SessionLabel:   nullableStringScan(agentLabel),
				StartedAt:      started,
				LeaseExpiresAt: leaseExpires,
			})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		result = []domain.ActiveAttemptSummary{}
	}
	return result, nil
}

// LookupSaveAttemptNote serves a replay before the note ID and artifact IDs
// are allocated. SaveAttemptNote still repeats this check in its writer
// transaction to close the lookup/write race.
func (repository *AttemptRepository) LookupSaveAttemptNote(ctx context.Context, key string, hash []byte) (ports.SaveAttemptNoteResult, bool, error) {
	var result ports.SaveAttemptNoteResult
	var found bool
	err := repository.db.Read(ctx, func(ctx context.Context, query Queryer) error {
		var savedHash []byte
		var savedResponse string
		err := query.QueryRowContext(ctx, `SELECT request_hash, response_json FROM idempotency_records
			WHERE operation = ? AND idempotency_key = ?`, saveAttemptNoteOperation, key).Scan(&savedHash, &savedResponse)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if !bytes.Equal(savedHash, hash) {
			return domain.NewError(domain.CodeIdempotencyConflict, "idempotency key was used with a different request", false,
				domain.Detail{Field: "idempotency_key", Code: domain.CodeIdempotencyConflict})
		}
		if err := json.Unmarshal([]byte(savedResponse), &result); err != nil {
			return domain.WrapError(err, domain.CodeStorageCorrupt, "stored idempotency response is invalid", false)
		}
		found = true
		return nil
	})
	return result, found, err
}

func (repository *AttemptRepository) SaveAttemptNote(ctx context.Context, command ports.SaveAttemptNoteCommand) (ports.SaveAttemptNoteResult, error) {
	if !validAttemptSessionID(command.SessionID) {
		return ports.SaveAttemptNoteResult{}, domain.NewError(domain.CodeInvalidArgument, "attempt note command is invalid", false)
	}
	if _, err := ids.ParseStrict(command.NoteID); err != nil {
		return ports.SaveAttemptNoteResult{}, domain.NewError(domain.CodeInvalidArgument, "attempt note command is invalid", false)
	}

	if _, err := ids.ParseStrict(command.AttemptID); err != nil || len(command.TokenHash) != 32 || !command.Kind.Valid() {
		return ports.SaveAttemptNoteResult{}, domain.NewError(domain.CodeInvalidArgument, "attempt note command is invalid", false)
	}
	now := command.OccurredAt.UTC()
	artifacts, err := validateAttemptArtifacts(command.Artifacts, now)
	if err != nil {
		return ports.SaveAttemptNoteResult{}, err
	}
	timestamp := now.Format(time.RFC3339Nano)
	var result ports.SaveAttemptNoteResult
	var leaseExpired bool
	err = repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		if command.IdempotencyKey != "" {
			var savedHash []byte
			var savedResponse string
			err := tx.QueryRowContext(ctx, `SELECT request_hash, response_json FROM idempotency_records
				WHERE operation = ? AND idempotency_key = ?`, saveAttemptNoteOperation, command.IdempotencyKey).Scan(&savedHash, &savedResponse)
			switch {
			case err == nil:
				if !bytes.Equal(savedHash, command.RequestHash) {
					return domain.NewError(domain.CodeIdempotencyConflict, "idempotency key was used with a different request", false,
						domain.Detail{Field: "idempotency_key", Code: domain.CodeIdempotencyConflict})
				}
				if err := json.Unmarshal([]byte(savedResponse), &result); err != nil {
					return domain.WrapError(err, domain.CodeStorageCorrupt, "stored idempotency response is invalid", false)
				}
				return nil
			case err == sql.ErrNoRows:
			default:
				return err
			}
		}
		var issueID, status, leaseExpiresAt string
		var tokenHash []byte
		err := tx.QueryRowContext(ctx, `SELECT issue_id, status, lease_token_hash, lease_expires_at
			FROM work_attempts WHERE id = ?`, command.AttemptID).Scan(&issueID, &status, &tokenHash, &leaseExpiresAt)
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
			if _, err := expireAttempt(ctx, tx, command.AttemptID, now); err != nil {
				return err
			}
			leaseExpired = true
			return nil
		}
		if subtle.ConstantTimeCompare(tokenHash, command.TokenHash) != 1 {
			return domain.NewError(domain.CodeInvalidLeaseToken, "lease token is invalid", false)
		}
		var nextStepsJSON *string
		if command.NextSteps != nil {
			encoded, err := json.Marshal(command.NextSteps)
			if err != nil {
				return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode attempt note next steps", false)
			}
			value := string(encoded)
			nextStepsJSON = &value
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO attempt_notes(
			id, attempt_id, kind, content, next_steps_json, important, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`, command.NoteID, command.AttemptID, command.Kind,
			command.Content, nextStepsJSON, command.Important, timestamp); err != nil {
			return err
		}
		result.Artifacts = make([]domain.Artifact, len(artifacts))
		for index, artifact := range artifacts {
			var title any
			if artifact.Title != nil {
				title = *artifact.Title
			}
			var metadata any
			if artifact.Metadata != nil {
				metadata = string(artifact.Metadata)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts(
				id, issue_id, attempt_id, type, uri, title, metadata, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, artifact.ID, issueID, command.AttemptID,
				artifact.Type, artifact.URI, title, metadata, timestamp); err != nil {
				return err
			}
			attemptID := command.AttemptID
			result.Artifacts[index] = domain.Artifact{
				ID: artifact.ID, IssueID: issueID, AttemptID: &attemptID, Type: artifact.Type,
				URI: artifact.URI, Title: domain.CloneArtifact(artifact).Title,
				Metadata: append([]byte(nil), artifact.Metadata...), CreatedAt: now,
			}
		}
		eventType := "attempt_note_saved"
		if command.Kind == domain.AttemptNoteKindCheckpoint {
			eventType = "checkpoint_saved"
		}
		payload, err := json.Marshal(struct {
			NoteID string                 `json:"note_id"`
			Kind   domain.AttemptNoteKind `json:"kind"`
		}{NoteID: command.NoteID, Kind: command.Kind})
		if err != nil {
			return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode attempt note event", false)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_events(
			issue_id, event_type, session_id, attempt_id, payload, created_at
		) VALUES (?, ?, ?, ?, ?, ?)`, issueID, eventType, nullableStringValuePtr(command.SessionID), command.AttemptID, string(payload), timestamp); err != nil {
			return err
		}
		result.Note = domain.AttemptNote{
			ID: command.NoteID, AttemptID: command.AttemptID, Kind: command.Kind, Content: command.Content,
			NextSteps: append([]string(nil), command.NextSteps...), Important: command.Important, CreatedAt: now,
		}
		if command.IdempotencyKey != "" {
			response, err := json.Marshal(result)
			if err != nil {
				return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode attempt note response", false)
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO idempotency_records(
				idempotency_key, operation, request_hash, response_json, created_at
			) VALUES (?, ?, ?, ?, ?)`, command.IdempotencyKey, saveAttemptNoteOperation, command.RequestHash, string(response), timestamp)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ports.SaveAttemptNoteResult{}, err
	}
	if leaseExpired {
		return ports.SaveAttemptNoteResult{}, domain.NewError(domain.CodeLeaseExpired, "attempt lease has expired", false)
	}
	return result, nil
}

func validateAttemptArtifacts(values []domain.Artifact, occurredAt time.Time) ([]domain.Artifact, error) {
	if len(values) > domain.MaxArtifactsPerAttemptMutation {
		return nil, domain.NewError(domain.CodeLimitExceeded, "artifacts exceeds the maximum count of 20", false,
			domain.Detail{Field: "artifacts", Code: "MAX_ITEMS", Message: "maximum 20"})
	}
	inputs := make([]domain.ArtifactInput, len(values))
	for index, artifact := range values {
		if _, err := ids.ParseStrict(artifact.ID); err != nil || artifact.IssueID != "" || artifact.AttemptID != nil ||
			!artifact.CreatedAt.Equal(occurredAt) || artifact.CreatedAt.Location() != time.UTC {
			return nil, domain.NewError(domain.CodeInvalidArgument, "attempt artifact command is invalid", false,
				domain.Detail{Field: "artifacts[" + strconv.Itoa(index) + "]", Code: "INVALID_VALUE"})
		}
		inputs[index] = domain.ArtifactInput{
			Type: artifact.Type, URI: artifact.URI, Title: artifact.Title, Metadata: artifact.Metadata,
		}
	}
	normalized, err := domain.ValidateArtifactInputs("artifacts", inputs)
	if err != nil {
		return nil, err
	}
	result := make([]domain.Artifact, len(values))
	for index, artifact := range values {
		result[index] = domain.Artifact{
			ID: artifact.ID, Type: normalized[index].Type, URI: normalized[index].URI,
			Title: normalized[index].Title, Metadata: normalized[index].Metadata, CreatedAt: occurredAt,
		}
	}
	return result, nil
}

// LookupFinishedAttempt serves a replay before application-side artifact IDs
// and timestamps are allocated.
func (repository *AttemptRepository) LookupFinishedAttempt(ctx context.Context, key string, hash []byte) (ports.FinishAttemptResult, bool, error) {
	if err := validateFinishIdempotency(key, hash); err != nil {
		return ports.FinishAttemptResult{}, false, err
	}
	var result ports.FinishAttemptResult
	var found bool
	err := repository.db.Read(ctx, func(ctx context.Context, query Queryer) error {
		var lookupErr error
		result, found, lookupErr = lookupFinishedAttempt(ctx, query, key, hash)
		return lookupErr
	})
	return result, found, err
}

func validateFinishIdempotency(key string, hash []byte) error {
	if err := domain.ValidateText("idempotency_key", key, domain.MaxIdempotencyKeyRunes); err != nil {
		return err
	}
	if strings.TrimSpace(key) == "" || len(hash) != 32 {
		return domain.NewError(domain.CodeInvalidArgument, "finish idempotency command is invalid", false,
			domain.Detail{Field: "idempotency_key", Code: "REQUIRED"})
	}
	return nil
}

func lookupFinishedAttempt(ctx context.Context, query Queryer, key string, hash []byte) (ports.FinishAttemptResult, bool, error) {
	var savedHash []byte
	var savedResponse string
	err := query.QueryRowContext(ctx, `SELECT request_hash, response_json FROM idempotency_records
		WHERE operation = ? AND idempotency_key = ?`, finishAttemptOperation, key).Scan(&savedHash, &savedResponse)
	if err == sql.ErrNoRows {
		return ports.FinishAttemptResult{}, false, nil
	}
	if err != nil {
		return ports.FinishAttemptResult{}, false, err
	}
	if !bytes.Equal(savedHash, hash) {
		return ports.FinishAttemptResult{}, false, domain.NewError(domain.CodeIdempotencyConflict,
			"idempotency key was used with a different request", false,
			domain.Detail{Field: "idempotency_key", Code: domain.CodeIdempotencyConflict})
	}
	var result ports.FinishAttemptResult
	if err := json.Unmarshal([]byte(savedResponse), &result); err != nil {
		return ports.FinishAttemptResult{}, false, domain.WrapError(err, domain.CodeStorageCorrupt,
			"stored idempotency response is invalid", false)
	}
	for index := range result.Artifacts {
		if bytes.Equal(result.Artifacts[index].Metadata, []byte("null")) {
			result.Artifacts[index].Metadata = nil
		}
	}
	if err := validateStoredFinishResult(result); err != nil {
		return ports.FinishAttemptResult{}, false, err
	}
	return cloneFinishResult(result), true, nil
}

func validateStoredFinishResult(result ports.FinishAttemptResult) error {
	if _, err := ids.ParseStrict(result.Attempt.ID); err != nil || result.Attempt.IssueID == "" {
		return corruptFinishResult()
	}
	if _, err := ids.ParseStrict(result.Attempt.IssueID); err != nil {
		return corruptFinishResult()
	}
	if _, err := ids.ParseStrict(result.Issue.ID); err != nil || result.Attempt.IssueID != result.Issue.ID ||
		!result.Attempt.Kind.Valid() || !result.Attempt.Status.Valid() || result.LatestEventID < 0 ||
		result.Attempt.FinishedAt == nil || result.Attempt.LeaseExpiresAt.IsZero() ||
		result.Attempt.StartedAt.IsZero() || result.Attempt.LastHeartbeatAt.IsZero() ||
		result.Issue.CreatedAt.IsZero() || result.Issue.UpdatedAt.IsZero() {
		return corruptFinishResult()
	}
	if !result.Issue.Type.Valid() || !result.Issue.Status.Valid() || !result.Issue.Priority.Valid() {
		return corruptFinishResult()
	}
	for _, timestamp := range []time.Time{result.Attempt.LeaseExpiresAt, result.Attempt.StartedAt,
		result.Attempt.LastHeartbeatAt, *result.Attempt.FinishedAt, result.Issue.CreatedAt, result.Issue.UpdatedAt} {
		if timestamp.IsZero() || timestamp.Location() != time.UTC {
			return corruptFinishResult()
		}
	}
	for _, timestamp := range []*time.Time{result.Issue.ClosedAt, result.Issue.ArchivedAt} {
		if timestamp != nil && (timestamp.IsZero() || timestamp.Location() != time.UTC) {
			return corruptFinishResult()
		}
	}
	for _, artifact := range result.Artifacts {
		if _, err := ids.ParseStrict(artifact.ID); err != nil || artifact.IssueID != result.Issue.ID ||
			artifact.AttemptID == nil || *artifact.AttemptID != result.Attempt.ID || artifact.CreatedAt.IsZero() ||
			artifact.CreatedAt.Location() != time.UTC {
			return corruptFinishResult()
		}
		normalized, err := domain.ValidateArtifactInputs("artifacts", []domain.ArtifactInput{{
			Type: artifact.Type, URI: artifact.URI, Title: artifact.Title, Metadata: artifact.Metadata,
		}})
		if err != nil || len(normalized) != 1 ||
			!bytes.Equal(normalized[0].Metadata, artifact.Metadata) {
			return corruptFinishResult()
		}
	}
	return nil
}

func corruptFinishResult() error {
	return domain.NewError(domain.CodeStorageCorrupt, "stored idempotency response is invalid", false)
}

func cloneFinishResult(result ports.FinishAttemptResult) ports.FinishAttemptResult {
	cloned := result
	cloned.Warnings = cloneAttemptStrings(result.Warnings)
	cloned.Artifacts = domain.CloneArtifacts(result.Artifacts)
	cloned.Attempt.SessionID = cloneAttemptString(result.Attempt.SessionID)
	cloned.Attempt.AgentLabel = cloneAttemptString(result.Attempt.AgentLabel)
	cloned.Attempt.FinishedAt = cloneAttemptTime(result.Attempt.FinishedAt)
	cloned.Attempt.ResultSummary = cloneAttemptString(result.Attempt.ResultSummary)
	cloned.Attempt.NextSteps = cloneAttemptStrings(result.Attempt.NextSteps)
	cloned.Attempt.Verification = cloneAttemptStrings(result.Attempt.Verification)
	cloned.Attempt.FailureReasonCode = cloneAttemptFailure(result.Attempt.FailureReasonCode)
	cloned.Attempt.InterruptionReasonCode = cloneAttemptInterruption(result.Attempt.InterruptionReasonCode)
	cloned.Attempt.ReasonDetails = cloneAttemptString(result.Attempt.ReasonDetails)
	cloned.Issue.Description = cloneAttemptString(result.Issue.Description)
	cloned.Issue.AcceptanceCriteria = cloneAttemptString(result.Issue.AcceptanceCriteria)
	cloned.Issue.ParentID = cloneAttemptString(result.Issue.ParentID)
	cloned.Issue.BlockedReason = cloneAttemptString(result.Issue.BlockedReason)
	cloned.Issue.CreatedBySessionID = cloneAttemptString(result.Issue.CreatedBySessionID)
	cloned.Issue.ClosedAt = cloneAttemptTime(result.Issue.ClosedAt)
	cloned.Issue.ArchivedAt = cloneAttemptTime(result.Issue.ArchivedAt)
	cloned.Issue.ArchivedBySessionID = cloneAttemptString(result.Issue.ArchivedBySessionID)
	if result.Issue.Labels != nil {
		cloned.Issue.Labels = make([]domain.Label, len(result.Issue.Labels))
		copy(cloned.Issue.Labels, result.Issue.Labels)
	}
	for index := range cloned.Issue.Labels {
		cloned.Issue.Labels[index].Description = cloneAttemptString(result.Issue.Labels[index].Description)
	}
	return cloned
}

func cloneAttemptStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

func cloneAttemptString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneAttemptTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneAttemptFailure(value *domain.FailureReasonCode) *domain.FailureReasonCode {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneAttemptInterruption(value *domain.InterruptionReasonCode) *domain.InterruptionReasonCode {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (repository *AttemptRepository) FinishAttempt(ctx context.Context, command ports.FinishAttemptCommand) (ports.FinishAttemptResult, error) {
	if !validAttemptSessionID(command.SessionID) {
		return ports.FinishAttemptResult{}, domain.NewError(domain.CodeInvalidArgument, "attempt completion command is invalid", false)
	}
	if _, err := ids.ParseStrict(command.AttemptID); err != nil || len(command.TokenHash) != 32 {
		return ports.FinishAttemptResult{}, domain.NewError(domain.CodeInvalidArgument, "attempt completion command is invalid", false)
	}
	if command.IdempotencyKey != "" {
		if err := validateFinishIdempotency(command.IdempotencyKey, command.RequestHash); err != nil {
			return ports.FinishAttemptResult{}, err
		}
	} else if len(command.RequestHash) != 0 {
		return ports.FinishAttemptResult{}, domain.NewError(domain.CodeInvalidArgument, "attempt completion command is invalid", false)
	}
	input, err := command.Input.Validate()
	if err != nil {
		return ports.FinishAttemptResult{}, err
	}
	now := command.OccurredAt.UTC()
	artifacts, err := validateAttemptArtifacts(command.Artifacts, now)
	if err != nil {
		return ports.FinishAttemptResult{}, err
	}
	timestamp := now.Format(time.RFC3339Nano)
	var result ports.FinishAttemptResult
	var leaseExpired bool
	var staleReviewTargetErr error
	err = repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		if command.IdempotencyKey != "" {
			saved, found, err := lookupFinishedAttempt(ctx, tx, command.IdempotencyKey, command.RequestHash)
			if err != nil {
				return err
			}
			if found {
				result = saved
				return nil
			}
		}
		var issueID, kindText, status, expiry string
		var tokenHash []byte
		var version, contextEventID int64
		var sessionID, agentLabel sql.NullString
		var started, heartbeat, finished sql.NullString
		var resultSummary, nextJSON, verificationJSON, failureCode, interruptionCode, reasonDetails sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT issue_id, session_id, agent_label, kind, status,
				issue_version_at_start, context_event_id_at_start, lease_token_hash, lease_expires_at,
				started_at, last_heartbeat_at, finished_at, result_summary, next_steps_json, verification_json,
				failure_reason_code, interruption_reason_code, reason_details
				FROM work_attempts WHERE id = ?`, command.AttemptID).Scan(&issueID, &sessionID, &agentLabel, &kindText, &status,
			&version, &contextEventID, &tokenHash, &expiry, &started, &heartbeat, &finished, &resultSummary,
			&nextJSON, &verificationJSON, &failureCode, &interruptionCode, &reasonDetails)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.NewError(domain.CodeAttemptNotFound, "attempt not found", false)
		}
		if err != nil {
			return err
		}
		if status != string(domain.AttemptStatusActive) {
			return domain.NewError(domain.CodeAttemptNotActive, "attempt is not active", false)
		}
		expiryTime, err := parseIssueTimestamp("lease_expires_at", expiry)
		if err != nil {
			return err
		}
		if !expiryTime.After(now) {
			if _, err := expireAttempt(ctx, tx, command.AttemptID, now); err != nil {
				return err
			}
			leaseExpired = true
			return nil
		}
		if subtle.ConstantTimeCompare(tokenHash, command.TokenHash) != 1 {
			return domain.NewError(domain.CodeInvalidLeaseToken, "lease token is invalid", false)
		}
		kind := domain.AttemptKind(kindText)
		if err := domain.ValidateFinishAttemptForKind(input, kind); err != nil {
			return err
		}
		issue, err := loadIssueForMutation(ctx, tx, domain.IssueIdentifier{Kind: domain.IssueIdentifierInternalID, Value: issueID})
		if err != nil {
			return err
		}
		if issue.ArchivedAt != nil {
			return domain.NewError(domain.CodeIssueArchived, "issue is archived", false)
		}
		if issue.Status == domain.StatusCancelled {
			return domain.NewError(domain.CodeIssueChangedDuringAttempt, "issue was cancelled during attempt", true, domain.Detail{Field: "status", Code: "CANCELLED"})
		}
		var blockers int64
		if err := tx.QueryRowContext(ctx, `SELECT `+issueUnresolvedBlockerCountSQL+` FROM issues WHERE id = ?`, issue.ID).Scan(&blockers); err != nil {
			return err
		}
		if blockers > 0 {
			return domain.NewError(domain.CodeUnresolvedBlockersAdded, "unresolved blockers were added during attempt", true, domain.Detail{Field: "issue_id", Code: "BLOCKED"})
		}
		var latestEventID int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM issue_events`).Scan(&latestEventID); err != nil {
			return err
		}
		var reviewRequest *domain.ReviewRequest
		if kind == domain.AttemptKindReview && input.Outcome == domain.AttemptOutcomeCompleted {
			reviewRequest, err = loadActiveReviewRequestForAttempt(ctx, tx, command.AttemptID)
			if err != nil {
				return err
			}
			if reviewRequest != nil && reviewRequest.Status == domain.ReviewRequestStatusClaimed && reviewRequest.ActiveAttemptID != nil && *reviewRequest.ActiveAttemptID == command.AttemptID {
				if reviewRequest.TargetIssueVersion != issue.Version || reviewRequest.TargetEventID != latestEventID {
					if err := supersedeReviewRequestForAttempt(ctx, tx, *reviewRequest, command.AttemptID, now); err != nil {
						return err
					}
					staleReviewTargetErr = domain.NewError(domain.CodeReviewTargetStale, "review target is stale", false)
					return nil
				}
			}
		}
		warnings, required, err := completionIssueChanges(ctx, tx, issue.ID, contextEventID)
		if err != nil {
			return err
		}
		if len(required) > 0 {
			ack := input.AcknowledgedChanges
			if ack == nil || ack.IssueVersion != issue.Version || ack.LatestEventID != latestEventID {
				details := make([]domain.Detail, len(required))
				for i, field := range required {
					details[i] = domain.Detail{Field: field, Code: "ACKNOWLEDGEMENT_REQUIRED"}
				}
				return domain.NewError(domain.CodeIssueChangedDuringAttempt, "issue changed during attempt", true, details...)
			}
		}
		target := issue.Status
		if input.Outcome == domain.AttemptOutcomeCompleted {
			if kind == domain.AttemptKindWork {
				target = *input.TargetIssueStatus
			} else {
				switch *input.ReviewOutcome {
				case domain.ReviewOutcomeApproved:
					target = domain.StatusDone
				case domain.ReviewOutcomeChangesRequested:
					target = domain.StatusReady
				case domain.ReviewOutcomeBlocked:
					target = domain.StatusBlocked
				}
			}
			blockedReason, err := domain.ApplyStatusTransition(issue.Status, target, stringValue(input.BlockedReason))
			if err != nil {
				return err
			}
			closedAt := issue.ClosedAt
			if !issue.Status.Terminal() && target.Terminal() {
				closedAt = &now
			} else if issue.Status.Terminal() && !target.Terminal() {
				closedAt = nil
			}
			res, err := tx.ExecContext(ctx, `UPDATE issues SET status = ?, blocked_reason = ?, version = version + 1, updated_at = ?, closed_at = ?
					WHERE id = ? AND version = ? AND archived_at IS NULL`, target, nullableStringValue(blockedReason), timestamp, nullableTime(closedAt), issue.ID, issue.Version)
			if err != nil {
				return fmt.Errorf("update issue status: %w", err)
			}
			affected, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if affected != 1 {
				return classifyConditionalUpdateFailure(ctx, tx, issue.ID)
			}
			issue.Status, issue.Version, issue.UpdatedAt, issue.BlockedReason, issue.ClosedAt = target, issue.Version+1, now, nullableAttemptString(blockedReason), closedAt
			if reviewRequest != nil && reviewRequest.Status == domain.ReviewRequestStatusClaimed && reviewRequest.ActiveAttemptID != nil && *reviewRequest.ActiveAttemptID == command.AttemptID {
				if err := resolveReviewRequestForAttempt(ctx, tx, *reviewRequest, command.AttemptID, now, *input.ReviewOutcome, input.BlockedReason); err != nil {
					return err
				}
			}
		}
		var nextValue, verificationValue any
		if input.NextSteps != nil {
			encoded, err := json.Marshal(input.NextSteps)
			if err != nil {
				return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode attempt next steps", false)
			}
			nextValue = string(encoded)
		}
		if input.Verification != nil {
			encoded, err := json.Marshal(input.Verification)
			if err != nil {
				return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode attempt verification", false)
			}
			verificationValue = string(encoded)
		}
		var failure, interruption any
		if input.FailureReasonCode != nil {
			failure = string(*input.FailureReasonCode)
		}
		if input.InterruptionReasonCode != nil {
			interruption = string(*input.InterruptionReasonCode)
		}
		res, err := tx.ExecContext(ctx, `UPDATE work_attempts SET status = ?, finished_at = ?, result_summary = ?, next_steps_json = ?,
				verification_json = ?, failure_reason_code = ?, interruption_reason_code = ?, reason_details = ?
				WHERE id = ? AND status = 'active'`, input.Outcome, timestamp, input.ResultSummary, nextValue, verificationValue,
			failure, interruption, nullableStringValuePtr(input.ReasonDetails), command.AttemptID)
		if err != nil {
			return fmt.Errorf("update work attempt: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return domain.NewError(domain.CodeAttemptNotActive, "attempt is not active", false)
		}
		result.Artifacts = make([]domain.Artifact, len(artifacts))
		for index, artifact := range artifacts {
			var title any
			if artifact.Title != nil {
				title = *artifact.Title
			}
			var metadata any
			if artifact.Metadata != nil {
				metadata = string(artifact.Metadata)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts(
				id, issue_id, attempt_id, type, uri, title, metadata, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, artifact.ID, issue.ID, command.AttemptID,
				artifact.Type, artifact.URI, title, metadata, timestamp); err != nil {
				return fmt.Errorf("insert artifact: %w", err)
			}
			attemptID := command.AttemptID
			result.Artifacts[index] = domain.Artifact{
				ID: artifact.ID, IssueID: issue.ID, AttemptID: &attemptID, Type: artifact.Type,
				URI: artifact.URI, Title: domain.CloneArtifact(artifact).Title,
				Metadata: append([]byte(nil), artifact.Metadata...), CreatedAt: now,
			}
		}
		eventTarget := domain.Status("")
		if input.Outcome == domain.AttemptOutcomeCompleted {
			eventTarget = target
		}
		payload := struct {
			AttemptID              string                         `json:"attempt_id"`
			Outcome                domain.AttemptOutcome          `json:"outcome"`
			TargetStatus           domain.Status                  `json:"target_status,omitempty"`
			FailureReasonCode      *domain.FailureReasonCode      `json:"failure_reason_code,omitempty"`
			InterruptionReasonCode *domain.InterruptionReasonCode `json:"interruption_reason_code,omitempty"`
		}{AttemptID: command.AttemptID, Outcome: input.Outcome, TargetStatus: eventTarget, FailureReasonCode: input.FailureReasonCode, InterruptionReasonCode: input.InterruptionReasonCode}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode attempt completion event", false)
		}
		eventType := "attempt_completed"
		if input.Outcome == domain.AttemptOutcomeFailed {
			eventType = "attempt_failed"
		}
		if input.Outcome == domain.AttemptOutcomeInterrupted {
			eventType = "attempt_interrupted"
		}
		if err := tx.QueryRowContext(ctx, `INSERT INTO issue_events(issue_id, event_type, session_id, attempt_id, payload, created_at)
				VALUES (?, ?, ?, ?, ?, ?) RETURNING id`, issue.ID, eventType, nullableStringValuePtr(command.SessionID), command.AttemptID, string(encoded), timestamp).Scan(&latestEventID); err != nil {
			return fmt.Errorf("insert issue event: %w", err)
		}
		parsedStarted, err := parseNullableAttemptTimestamp(started)
		if err != nil {
			return err
		}
		parsedHeartbeat, err := parseNullableAttemptTimestamp(heartbeat)
		if err != nil {
			return err
		}
		parsedFinished, err := parseNullableAttemptTimestamp(finished)
		if err != nil {
			return err
		}
		attempt := domain.WorkAttempt{ID: command.AttemptID, IssueID: issue.ID, SessionID: nullableStringScan(sessionID), AgentLabel: nullableStringScan(agentLabel),
			Kind: kind, Status: domain.AttemptStatus(input.Outcome), IssueVersionAtStart: version, ContextEventIDAtStart: contextEventID,
			LeaseExpiresAt: expiryTime, StartedAt: parsedStarted, LastHeartbeatAt: parsedHeartbeat, FinishedAt: &parsedFinished,
			ResultSummary: nullableStringScan(resultSummary), NextSteps: []string{}, Verification: []string{}, FailureReasonCode: nullableFailure(failureCode),
			InterruptionReasonCode: nullableInterruption(interruptionCode), ReasonDetails: nullableStringScan(reasonDetails)}
		attempt.FinishedAt = &now
		if input.NextSteps != nil {
			attempt.NextSteps = append([]string{}, input.NextSteps...)
		}
		if input.Verification != nil {
			attempt.Verification = append([]string{}, input.Verification...)
		}
		if input.ResultSummary != "" {
			v := input.ResultSummary
			attempt.ResultSummary = &v
		}
		if input.ReasonDetails != nil {
			v := *input.ReasonDetails
			attempt.ReasonDetails = &v
		}
		if input.FailureReasonCode != nil {
			v := *input.FailureReasonCode
			attempt.FailureReasonCode = &v
		}
		if input.InterruptionReasonCode != nil {
			v := *input.InterruptionReasonCode
			attempt.InterruptionReasonCode = &v
		}
		result = ports.FinishAttemptResult{Attempt: attempt, Issue: issue, Warnings: warnings, LatestEventID: latestEventID, Artifacts: result.Artifacts}
		if command.IdempotencyKey != "" {
			response, err := json.Marshal(result)
			if err != nil {
				return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode finish response", false)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO idempotency_records(
				idempotency_key, operation, request_hash, response_json, created_at
			) VALUES (?, ?, ?, ?, ?)`, command.IdempotencyKey, finishAttemptOperation, command.RequestHash,
				string(response), timestamp); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if leaseExpired {
			return ports.FinishAttemptResult{}, domain.NewError(domain.CodeLeaseExpired, "attempt lease has expired", false)
		}
		return ports.FinishAttemptResult{}, err
	}
	if staleReviewTargetErr != nil {
		return ports.FinishAttemptResult{}, staleReviewTargetErr
	}
	if leaseExpired {
		return ports.FinishAttemptResult{}, domain.NewError(domain.CodeLeaseExpired, "attempt lease has expired", false)
	}
	return result, nil
}

func (repository *AttemptRepository) ForceReleaseAttempt(ctx context.Context, command ports.ForceReleaseAttemptCommand) (ports.ForceReleaseAttemptResult, error) {
	if _, err := ids.ParseStrict(command.AttemptID); err != nil || command.OccurredAt.IsZero() {
		return ports.ForceReleaseAttemptResult{}, domain.NewError(domain.CodeInvalidArgument, "attempt release command is invalid", false)
	}
	now := command.OccurredAt.UTC()
	timestamp := now.Format(time.RFC3339Nano)
	var result ports.ForceReleaseAttemptResult
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		var issueID, status string
		err := tx.QueryRowContext(ctx, `SELECT issue_id, status FROM work_attempts WHERE id = ?`, command.AttemptID).Scan(&issueID, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.NewError(domain.CodeAttemptNotFound, "attempt not found", false)
		}
		if err != nil {
			return err
		}
		if status != string(domain.AttemptStatusActive) {
			return domain.NewError(domain.CodeAttemptNotActive, "attempt is not active", false)
		}
		res, err := tx.ExecContext(ctx, `UPDATE work_attempts
			SET status = 'interrupted', finished_at = ?, interruption_reason_code = 'user_request'
			WHERE id = ? AND status = 'active'`, timestamp, command.AttemptID)
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
		payload, err := json.Marshal(struct {
			AttemptID              string                        `json:"attempt_id"`
			Outcome                domain.AttemptOutcome         `json:"outcome"`
			InterruptionReasonCode domain.InterruptionReasonCode `json:"interruption_reason_code"`
		}{AttemptID: command.AttemptID, Outcome: domain.AttemptOutcomeInterrupted, InterruptionReasonCode: domain.InterruptionReasonUserRequest})
		if err != nil {
			return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode attempt interruption event", false)
		}
		var latestEventID int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO issue_events(issue_id, event_type, session_id, attempt_id, payload, created_at)
			VALUES (?, 'attempt_interrupted', NULL, ?, ?, ?) RETURNING id`, issueID, command.AttemptID, string(payload), timestamp).Scan(&latestEventID); err != nil {
			return err
		}
		attempt, err := scanActivityAttempt(tx.QueryRowContext(ctx, `SELECT id, issue_id, session_id, agent_label, kind, status,
				issue_version_at_start, context_event_id_at_start, lease_expires_at,
				started_at, last_heartbeat_at, finished_at, result_summary, next_steps_json, verification_json,
				failure_reason_code, interruption_reason_code, reason_details
				FROM work_attempts WHERE id = ?`, command.AttemptID))
		if err != nil {
			return err
		}
		result = ports.ForceReleaseAttemptResult{Attempt: attempt, LatestEventID: latestEventID}
		return nil
	})
	if err != nil {
		return ports.ForceReleaseAttemptResult{}, err
	}
	return result, nil
}

func loadActiveReviewRequestForAttempt(ctx context.Context, tx Queryer, attemptID string) (*domain.ReviewRequest, error) {
	var request domain.ReviewRequest
	var artifactIDsJSON []byte
	var status string
	var supersedesID sql.NullString
	var activeAttemptID sql.NullString
	var createdAtText string
	var resolvedAtText sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, status, supersedes_id, active_attempt_id, version, created_at, resolved_at
		FROM review_requests WHERE active_attempt_id = ? AND status = 'claimed'`, attemptID).Scan(
		&request.ID, &request.TargetID, &request.IssueID, &request.TargetIssueVersion, &request.TargetEventID, &artifactIDsJSON, &status, &supersedesID, &activeAttemptID, &request.Version, &createdAtText, &resolvedAtText,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	request.ArtifactIDs, err = unmarshalArtifactIDs(artifactIDsJSON)
	if err != nil {
		return nil, err
	}
	request.Status = domain.ReviewRequestStatus(status)
	if supersedesID.Valid {
		value := supersedesID.String
		request.SupersedesID = &value
	}
	if activeAttemptID.Valid {
		value := activeAttemptID.String
		request.ActiveAttemptID = &value
	}
	request.CreatedAt = parseTimestamp(createdAtText)
	if resolvedAtText.Valid {
		value := parseTimestamp(resolvedAtText.String)
		request.ResolvedAt = &value
	}
	return &request, nil
}

func supersedeReviewRequestForAttempt(ctx context.Context, tx Executor, request domain.ReviewRequest, attemptID string, occurredAt time.Time) error {
	resolvedAt := occurredAt.UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `UPDATE review_requests SET status = ?, active_attempt_id = NULL, resolved_at = ?, version = version + 1
		WHERE id = ? AND status = 'claimed' AND active_attempt_id = ?`, domain.ReviewRequestStatusSuperseded, resolvedAt, request.ID, attemptID)
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO review_events(request_id, target_id, attempt_id, event_type, payload, created_at)
		VALUES (?, ?, ?, 'review_superseded', ?, ?)`, request.ID, request.TargetID, attemptID, string(payloadForReviewEvent(request.ID, request.TargetID, &attemptID, nil, nil)), resolvedAt); err != nil {
		return err
	}
	return nil
}

func resolveReviewRequestForAttempt(ctx context.Context, tx Executor, request domain.ReviewRequest, attemptID string, occurredAt time.Time, outcome domain.ReviewOutcome, reason *string) error {
	nextStatus := reviewRequestStatusForOutcome(outcome)
	resolvedAt := occurredAt.UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `UPDATE review_requests SET status = ?, active_attempt_id = NULL, resolved_at = ?, version = version + 1
		WHERE id = ? AND status = 'claimed' AND active_attempt_id = ?`, nextStatus, resolvedAt, request.ID, attemptID)
	if err != nil {
		return fmt.Errorf("update review request: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return domain.NewError(domain.CodeInvalidArgument, "review request is not active", false)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO review_outcomes(id, request_id, attempt_id, outcome, reason, version, created_at)
		VALUES (?, ?, ?, ?, ?, 1, ?)`, attemptID, request.ID, attemptID, outcome, stringOrNil(reason), resolvedAt); err != nil {
		return fmt.Errorf("insert review outcome: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO review_events(request_id, target_id, attempt_id, event_type, payload, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, request.ID, request.TargetID, attemptID, reviewEventTypeForOutcome(outcome), string(payloadForReviewEvent(request.ID, request.TargetID, &attemptID, &outcome, reason)), resolvedAt); err != nil {
		return fmt.Errorf("insert review event: %w", err)
	}
	return nil
}

func completionIssueChanges(ctx context.Context, tx Queryer, issueID string, startID int64) ([]string, []string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT event_type, payload FROM issue_events WHERE issue_id = ? AND id > ? ORDER BY id ASC`, issueID, startID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	warningSet, requiredSet := map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var eventType, raw string
		if err := rows.Scan(&eventType, &raw); err != nil {
			return nil, nil, err
		}
		if eventType == "issue_archived" {
			return nil, nil, domain.NewError(domain.CodeIssueArchived, "issue is archived", false)
		}
		if eventType != "issue_updated" && eventType != "status_changed" && eventType != "labels_changed" {
			continue
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			return nil, nil, domain.WrapError(err, domain.CodeStorageCorrupt, "stored issue event payload is invalid", false)
		}
		rawFields, ok := payload["changed_fields"]
		if !ok || string(rawFields) == "null" {
			return nil, nil, domain.NewError(domain.CodeStorageCorrupt, "stored issue event payload is invalid", false)
		}
		var changedFields []string
		if err := json.Unmarshal(rawFields, &changedFields); err != nil {
			return nil, nil, domain.WrapError(err, domain.CodeStorageCorrupt, "stored issue event payload is invalid", false)
		}
		for _, field := range changedFields {
			switch field {
			case "description", "acceptance_criteria", "status", "blocked_reason":
				requiredSet[field] = true
			case "title", "priority", "labels", "parent_id", "type":
				warningSet[field] = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	warnings := make([]string, 0, len(warningSet))
	for field := range warningSet {
		warnings = append(warnings, "ISSUE_CHANGED:"+field)
	}
	sort.Strings(warnings)
	required := make([]string, 0, len(requiredSet))
	for field := range requiredSet {
		required = append(required, field)
	}
	sort.Strings(required)
	return warnings, required, nil
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
func nullableStringValue(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func nullableAttemptString(v string) *string {
	if v == "" {
		return nil
	}
	x := v
	return &x
}
func nullableStringValuePtr(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

func validAttemptSessionID(v *string) bool {
	if v == nil {
		return true
	}
	_, err := ids.ParseStrict(*v)
	return err == nil && len(*v) == 26
}

func parseNullableAttemptTimestamp(v sql.NullString) (time.Time, error) {
	if !v.Valid {
		return time.Time{}, nil
	}
	return parseIssueTimestamp("attempt_timestamp", v.String)
}
func nullableStringScan(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	x := v.String
	return &x
}
func nullableFailure(v sql.NullString) *domain.FailureReasonCode {
	if !v.Valid {
		return nil
	}
	x := domain.FailureReasonCode(v.String)
	return &x
}
func nullableInterruption(v sql.NullString) *domain.InterruptionReasonCode {
	if !v.Valid {
		return nil
	}
	x := domain.InterruptionReasonCode(v.String)
	return &x
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
		if _, err := expireAttempt(ctx, tx, id, now); err != nil {
			return err
		}
	}
	return nil
}

func expireAttempt(ctx context.Context, tx Executor, attemptID string, now time.Time) (bool, error) {
	timestamp := now.UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `UPDATE work_attempts SET status = 'expired', finished_at = ?
		WHERE id = ? AND status = 'active' AND lease_expires_at <= ?`, timestamp, attemptID, timestamp)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 {
		return false, nil
	}
	var issueID string
	if err := tx.QueryRowContext(ctx, `SELECT issue_id FROM work_attempts WHERE id = ?`, attemptID).Scan(&issueID); err != nil {
		return false, err
	}
	request, err := loadActiveReviewRequestForAttempt(ctx, tx, attemptID)
	if err != nil {
		return false, err
	}
	if request != nil {
		resolvedAt := now.UTC().Format(time.RFC3339Nano)
		res, err := tx.ExecContext(ctx, `UPDATE review_requests SET status = ?, active_attempt_id = NULL, resolved_at = NULL, version = version + 1
			WHERE id = ? AND status = 'claimed' AND active_attempt_id = ?`, domain.ReviewRequestStatusOpen, request.ID, attemptID)
		if err != nil {
			return false, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return false, err
		}
		if affected == 1 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO review_events(request_id, target_id, attempt_id, event_type, payload, created_at)
				VALUES (?, ?, ?, 'review_requested', ?, ?)`, request.ID, request.TargetID, attemptID, string(payloadForReviewEvent(request.ID, request.TargetID, &attemptID, nil, nil)), resolvedAt); err != nil {
				return false, err
			}
		}
	}
	payload, err := json.Marshal(struct {
		AttemptID string `json:"attempt_id"`
	}{AttemptID: attemptID})
	if err != nil {
		return false, domain.WrapError(err, domain.CodeStorageFailure, "cannot encode attempt expiry event", false)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_type, session_id, attempt_id, payload, created_at)
		VALUES (?, 'attempt_expired', NULL, ?, ?, ?)`, issueID, attemptID, string(payload), timestamp)
	return true, err
}

func isActiveAttemptConstraint(err error) bool {
	code, ok := sqliteCode(err)
	return ok && code&0xff == 19
}

var _ ports.AttemptRepository = (*AttemptRepository)(nil)
