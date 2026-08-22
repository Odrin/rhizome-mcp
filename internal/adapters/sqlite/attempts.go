package sqlite

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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

// authenticateActiveAttempt is the lease-authenticated attempt gate shared
// by RenewAttempt, SaveAttemptNote, FinishAttempt and ForceReleaseAttempt.
// It loads the attempt's current issue_id, kind, status and lease, verifies
// the attempt is still active, lazily expires it (via expireAttempt, in
// this same transaction) if its lease has already passed now, and verifies
// the caller's token against the stored hash in constant time. If
// requireKind is non-nil, the attempt's kind must match it exactly.
//
// On success, returns the attempt's issueID and kind with expired=false. If
// the lease had already expired, expireAttempt has already run in this
// transaction (status is now 'expired', any claimed review request already
// released) and this returns expired=true — the caller must treat the
// overall operation as a no-op and report LEASE_EXPIRED, exactly like the
// three call sites this centralizes already did.
func authenticateActiveAttempt(ctx context.Context, tx Executor, attemptID string, tokenHash []byte, now time.Time, requireKind *domain.AttemptKind) (issueID string, kind domain.AttemptKind, expired bool, err error) {
	var status, leaseExpiresAt, kindText string
	var storedHash []byte
	err = tx.QueryRowContext(ctx, `SELECT issue_id, kind, status, lease_token_hash, lease_expires_at FROM work_attempts WHERE id = ?`, attemptID).
		Scan(&issueID, &kindText, &status, &storedHash, &leaseExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, domain.NewError(domain.CodeAttemptNotFound, "attempt not found", false)
	}
	if err != nil {
		return "", "", false, err
	}
	if status != string(domain.AttemptStatusActive) {
		return "", "", false, domain.NewError(domain.CodeAttemptNotActive, "attempt is not active", false)
	}
	expiry, err := parseIssueTimestamp("lease_expires_at", leaseExpiresAt)
	if err != nil {
		return "", "", false, err
	}
	if !expiry.After(now) {
		wasExpired, err := expireAttempt(ctx, tx, attemptID, now)
		if err != nil {
			return "", "", false, err
		}
		if !wasExpired {
			return "", "", false, domain.NewError(domain.CodeStorageCorrupt,
				"attempt lease expiry state disagreed between the caller's read and expireAttempt's own guard", false)
		}
		return "", "", true, nil
	}
	if subtle.ConstantTimeCompare(storedHash, tokenHash) != 1 {
		return "", "", false, domain.NewError(domain.CodeInvalidLeaseToken, "lease token is invalid", false)
	}
	kind = domain.AttemptKind(kindText)
	if requireKind != nil && kind != *requireKind {
		return "", "", false, domain.NewError(domain.CodeInvalidArgument, "attempt kind does not match the required kind", false,
			domain.Detail{Field: "attempt_id", Code: "WRONG_KIND"})
	}
	return issueID, kind, false, nil
}

// terminateAttemptReason carries the optional failure/interruption reason
// codes terminateAttempt persists alongside a terminal status.
type terminateAttemptReason struct {
	FailureReasonCode      *domain.FailureReasonCode
	InterruptionReasonCode *domain.InterruptionReasonCode
}

// terminateAttempt is the shared terminal-status primitive used by
// ForceReleaseAttempt and FinishAttempt (for all three outcomes: the
// completed outcome's review resolution is already handled separately by
// resolveReviewRequestForAttempt/supersedeReviewRequestForAttempt before
// this runs, so this is a correct no-op on review_requests for completed).
// expireAttempt keeps its own UPDATE, since it additionally guards on
// lease_expires_at rather than relying on a prior authenticateActiveAttempt
// call, but shares releaseClaimedReviewRequest below.
//
// Sets work_attempts to finalStatus with the given reason codes and, for a
// status that abandons a review claim (failed, interrupted, expired --
// never completed or cancelled), returns any claimed review request back
// to open (docs/09's promise that losing the review attempt returns the
// request to open, previously true only for lease expiry). Returns whether
// the attempt was found active and terminated.
func terminateAttempt(ctx context.Context, tx Executor, attemptID string, finalStatus domain.AttemptStatus, reason terminateAttemptReason, now time.Time) (bool, error) {
	timestamp := formatStorageTime(now)
	var failure, interruption any
	if reason.FailureReasonCode != nil {
		failure = string(*reason.FailureReasonCode)
	}
	if reason.InterruptionReasonCode != nil {
		interruption = string(*reason.InterruptionReasonCode)
	}
	res, err := tx.ExecContext(ctx, `UPDATE work_attempts SET status = ?, finished_at = ?, failure_reason_code = ?, interruption_reason_code = ?
		WHERE id = ? AND status = 'active'`, string(finalStatus), timestamp, failure, interruption, attemptID)
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
	if !terminalStatusAbandonsReviewClaim(finalStatus) {
		return true, nil
	}
	if err := releaseClaimedReviewRequest(ctx, tx, attemptID, now); err != nil {
		return false, err
	}
	return true, nil
}

func terminalStatusAbandonsReviewClaim(status domain.AttemptStatus) bool {
	switch status {
	case domain.AttemptStatusFailed, domain.AttemptStatusInterrupted, domain.AttemptStatusExpired:
		return true
	default:
		return false
	}
}

// releaseClaimedReviewRequest returns attemptID's claimed review request (if
// any) to open and appends a review_requested event -- the recovery
// expireAttempt already performed on lease expiry, now shared by every path
// that ends a review attempt without it completing (failed, interrupted,
// force-released).
func releaseClaimedReviewRequest(ctx context.Context, tx Executor, attemptID string, now time.Time) error {
	request, err := loadActiveReviewRequestForAttempt(ctx, tx, attemptID)
	if err != nil {
		return err
	}
	if request == nil {
		return nil
	}
	timestamp := formatStorageTime(now)
	res, err := tx.ExecContext(ctx, `UPDATE review_requests SET status = ?, active_attempt_id = NULL, resolved_at = NULL, version = version + 1
		WHERE id = ? AND status = 'claimed' AND active_attempt_id = ?`, domain.ReviewRequestStatusOpen, request.ID, attemptID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return nil
	}
	return appendReviewEvent(ctx, tx, request.IssueID, "review_requested", &attemptID,
		payloadForReviewEvent(request.ID, request.TargetID, &attemptID, nil, nil), timestamp)
}

func (repository *AttemptRepository) ClaimIssue(ctx context.Context, command ports.ClaimIssueCommand) (ports.ClaimIssueResult, error) {
	if !validAttemptSessionID(command.SessionID) {
		return ports.ClaimIssueResult{}, domain.NewError(domain.CodeInvalidArgument, "attempt claim command is invalid", false)
	}
	if _, err := ids.ParseStrict(command.AttemptID); err != nil || len(command.TokenHash) != 32 || command.LeaseDuration <= 0 {
		return ports.ClaimIssueResult{}, domain.NewError(domain.CodeInvalidArgument, "attempt claim command is invalid", false)
	}
	now := command.OccurredAt.UTC()
	timestamp := formatStorageTime(now)
	expires := now.Add(command.LeaseDuration).UTC()
	expiresTimestamp := formatStorageTime(expires)
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
				// The persisted response never carries a lease token (see
				// ports.ClaimIssueResult.LeaseToken), so replay cannot serve
				// the original secret. If the attempt is still active,
				// rotate the lease and issue this call's freshly generated
				// token instead; the previous token, if the original caller
				// ever received it, stops working the moment this succeeds.
				var attemptStatus string
				scanErr := tx.QueryRowContext(ctx, `SELECT status FROM work_attempts WHERE id = ?`, result.Attempt.ID).Scan(&attemptStatus)
				if scanErr == sql.ErrNoRows {
					return domain.WrapError(scanErr, domain.CodeStorageCorrupt, "idempotency record references a missing attempt", false)
				}
				if scanErr != nil {
					return scanErr
				}
				if attemptStatus != string(domain.AttemptStatusActive) {
					return domain.NewError(domain.CodeAttemptNotActive, "claimed attempt is no longer active; the original claim response was not retained", false,
						domain.Detail{Field: "attempt_id", Code: "NOT_ACTIVE"})
				}
				res, updateErr := tx.ExecContext(ctx, `UPDATE work_attempts SET lease_token_hash = ?, lease_expires_at = ?
						WHERE id = ? AND status = 'active'`, command.TokenHash, expiresTimestamp, result.Attempt.ID)
				if updateErr != nil {
					return updateErr
				}
				if affected, rowsErr := res.RowsAffected(); rowsErr != nil {
					return rowsErr
				} else if affected != 1 {
					return domain.NewError(domain.CodeAttemptNotActive, "claimed attempt is no longer active; the original claim response was not retained", false,
						domain.Detail{Field: "attempt_id", Code: "NOT_ACTIVE"})
				}
				result.Attempt.LeaseExpiresAt = expires
				result.LeaseToken = command.LeaseToken
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
		var blockerCount int64
		if err := tx.QueryRowContext(ctx, `SELECT `+issueUnresolvedBlockerCountSQL+` FROM issues WHERE id = ?`, issue.ID).Scan(&blockerCount); err != nil {
			return err
		}
		var active bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_attempts WHERE issue_id = ? AND status = 'active')`, issue.ID).Scan(&active); err != nil {
			return err
		}
		kind, err := domain.EvaluateClaim(issue, blockerCount, active)
		if err != nil {
			return err
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
		if kind == domain.AttemptKindReview {
			if err := bindOpenReviewRequestForAttempt(ctx, tx, issue.ID, command.AttemptID, now); err != nil {
				return err
			}
		}
		result.Issue = issue
		result.Attempt = domain.WorkAttempt{
			ID: command.AttemptID, IssueID: issue.ID, SessionID: copyOptionalString(command.SessionID), Kind: kind, Status: domain.AttemptStatusActive,
			IssueVersionAtStart: issue.Version, ContextEventIDAtStart: latestEventID,
			LeaseExpiresAt: expires, StartedAt: now, LastHeartbeatAt: now,
		}
		result.LeaseToken = command.LeaseToken
		// Read the real post-claim projection inside this same transaction
		// rather than asserting values that are merely true under today's
		// claim preconditions -- see ISSUE-208.
		projection, err := queryIssueProjectionByID(ctx, tx, issue.ID, now)
		if err != nil {
			return err
		}
		result.Projection = projection
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
	timestamp := formatStorageTime(now)
	expires := now.Add(command.LeaseDuration).UTC()
	var result ports.RenewAttemptResult
	var leaseExpired bool
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		_, _, expired, err := authenticateActiveAttempt(ctx, tx, command.AttemptID, command.TokenHash, now, nil)
		if err != nil {
			return err
		}
		if expired {
			leaseExpired = true
			return nil
		}
		res, err := tx.ExecContext(ctx, `UPDATE work_attempts
			SET lease_expires_at = ?, last_heartbeat_at = ?
			WHERE id = ? AND status = 'active'`, formatStorageTime(expires), timestamp, command.AttemptID)
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
			WHERE status = 'active' AND lease_expires_at <= ? ORDER BY id ASC`, formatStorageTime(now))
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
			LIMIT ?`, formatStorageTime(now), limit)
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
	timestamp := formatStorageTime(now)
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
		issueID, _, expired, err := authenticateActiveAttempt(ctx, tx, command.AttemptID, command.TokenHash, now, nil)
		if err != nil {
			return err
		}
		if expired {
			leaseExpired = true
			return nil
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
	timestamp := formatStorageTime(now)
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
		_, _, expired, err := authenticateActiveAttempt(ctx, tx, command.AttemptID, command.TokenHash, now, nil)
		if err != nil {
			return err
		}
		if expired {
			leaseExpired = true
			return nil
		}
		var issueID, kindText, expiry string
		var version, contextEventID int64
		var sessionID, agentLabel sql.NullString
		var started, heartbeat, finished sql.NullString
		var resultSummary, nextJSON, verificationJSON, failureCode, interruptionCode, reasonDetails sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT issue_id, session_id, agent_label, kind,
				issue_version_at_start, context_event_id_at_start, lease_expires_at,
				started_at, last_heartbeat_at, finished_at, result_summary, next_steps_json, verification_json,
				failure_reason_code, interruption_reason_code, reason_details
				FROM work_attempts WHERE id = ?`, command.AttemptID).Scan(&issueID, &sessionID, &agentLabel, &kindText,
			&version, &contextEventID, &expiry, &started, &heartbeat, &finished, &resultSummary,
			&nextJSON, &verificationJSON, &failureCode, &interruptionCode, &reasonDetails)
		if err != nil {
			return err
		}
		expiryTime, err := parseIssueTimestamp("lease_expires_at", expiry)
		if err != nil {
			return err
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
		// Excludes source='review' rows and attempt_started rows
		// deliberately: an attempt's own lifecycle (its own claim, in
		// particular -- now that ClaimIssue auto-binds a review request to
		// its attempt_started row, ISSUE-189) always advances the shared
		// issue_events sequence past whatever event position was captured
		// when the review target was created, so counting those events here
		// would make every review appear stale from the moment it is
		// claimed. Staleness and the change-acknowledgment check both mean
		// "did the reviewed work change," not "did some attempt's own
		// workflow progress" -- see docs/02 for the unification contract and
		// this exclusion's rationale.
		var latestEventID int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM issue_events WHERE source != 'review' AND event_type != 'attempt_started'`).Scan(&latestEventID); err != nil {
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
			// Validate that an approval attempt is bound to a request, or no
			// unresolved request exists (review is truly optional).
			if reviewRequest == nil && input.ReviewOutcome != nil && *input.ReviewOutcome == domain.ReviewOutcomeApproved {
				var unresolved int64
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM review_requests WHERE issue_id = ? AND status IN ('open','claimed'))`, issue.ID).Scan(&unresolved); err != nil {
					return err
				}
				if unresolved == 1 {
					return domain.NewError(domain.CodeReviewRequestRequired, "an open review request exists for this issue but is not bound to this attempt", false,
						domain.Detail{Field: "review_request_id", Code: domain.CodeReviewRequestRequired})
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
		target, err := domain.FinishTargetStatus(kind, input, issue.Status)
		if err != nil {
			return err
		}
		if input.Outcome == domain.AttemptOutcomeCompleted {
			blockedReason, err := domain.ApplyFinishTransition(issue.Status, target, stringValue(input.BlockedReason))
			if err != nil {
				return err
			}
			closedAt := domain.NextClosedAt(issue.Status, target, now, issue.ClosedAt)
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
		reason := terminateAttemptReason{FailureReasonCode: input.FailureReasonCode, InterruptionReasonCode: input.InterruptionReasonCode}
		terminated, err := terminateAttempt(ctx, tx, command.AttemptID, domain.AttemptStatus(input.Outcome), reason, now)
		if err != nil {
			return fmt.Errorf("update work attempt: %w", err)
		}
		if !terminated {
			return domain.NewError(domain.CodeAttemptNotActive, "attempt is not active", false)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE work_attempts SET result_summary = ?, next_steps_json = ?, verification_json = ?, reason_details = ?
				WHERE id = ?`, input.ResultSummary, nextValue, verificationValue, nullableStringValuePtr(input.ReasonDetails), command.AttemptID); err != nil {
			return fmt.Errorf("update work attempt: %w", err)
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
	timestamp := formatStorageTime(now)
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
		reason := domain.InterruptionReasonUserRequest
		terminated, err := terminateAttempt(ctx, tx, command.AttemptID, domain.AttemptStatusInterrupted,
			terminateAttemptReason{InterruptionReasonCode: &reason}, now)
		if err != nil {
			return err
		}
		if !terminated {
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

// bindOpenReviewRequestForAttempt looks up issueID's open review request (if
// any) and transitions it to claimed with active_attempt_id = attemptID,
// appending a review_claimed event -- the claim-time half of the binding
// ClaimReviewRequest performs explicitly; ClaimIssue calls this
// automatically whenever it starts a review attempt. No-op (returns nil,
// nil) when no open request exists for the issue.
func bindOpenReviewRequestForAttempt(ctx context.Context, tx Executor, issueID, attemptID string, now time.Time) error {
	var requestID, targetID string
	var version int64
	err := tx.QueryRowContext(ctx, `SELECT id, target_id, version FROM review_requests
		WHERE issue_id = ? AND status = 'open' ORDER BY created_at DESC, id DESC LIMIT 1`, issueID).
		Scan(&requestID, &targetID, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	claimedAt := formatStorageTime(now)
	res, err := tx.ExecContext(ctx, `UPDATE review_requests SET status = 'claimed', active_attempt_id = ?, resolved_at = NULL, version = version + 1
		WHERE id = ? AND version = ? AND status = 'open'`, attemptID, requestID, version)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return domain.NewError(domain.CodeStorageCorrupt, "review request claim-time binding lost a race inside its own write transaction", false)
	}
	return appendReviewEvent(ctx, tx, issueID, "review_claimed", &attemptID,
		payloadForReviewEvent(requestID, targetID, &attemptID, nil, nil), claimedAt)
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
	resolvedAt := formatStorageTime(occurredAt)
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
	if err := appendReviewEvent(ctx, tx, request.IssueID, "review_superseded", &attemptID,
		payloadForReviewEvent(request.ID, request.TargetID, &attemptID, nil, nil), resolvedAt); err != nil {
		return err
	}
	return nil
}

func resolveReviewRequestForAttempt(ctx context.Context, tx Executor, request domain.ReviewRequest, attemptID string, occurredAt time.Time, outcome domain.ReviewOutcome, reason *string) error {
	nextStatus := reviewRequestStatusForOutcome(outcome)
	resolvedAt := formatStorageTime(occurredAt)
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
	if err := appendReviewEvent(ctx, tx, request.IssueID, string(reviewEventTypeForOutcome(outcome)), &attemptID,
		payloadForReviewEvent(request.ID, request.TargetID, &attemptID, &outcome, reason), resolvedAt); err != nil {
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
	var allChangedFields []string
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
		allChangedFields = append(allChangedFields, changedFields...)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	warnings, required := domain.ClassifyIssueChanges(allChangedFields)
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
		WHERE issue_id = ? AND status = 'active' AND lease_expires_at <= ?`, issueID, formatStorageTime(now))
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
		if !expired {
			return domain.NewError(domain.CodeStorageCorrupt,
				"attempt lease expiry state disagreed between the sweep's own select and expireAttempt's guard", false)
		}
	}
	return nil
}

func expireAttempt(ctx context.Context, tx Executor, attemptID string, now time.Time) (bool, error) {
	timestamp := formatStorageTime(now)
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
	if err := releaseClaimedReviewRequest(ctx, tx, attemptID, now); err != nil {
		return false, err
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
