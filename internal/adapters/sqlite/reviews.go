package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

// ReviewRepository persists review workflow requests and their transitions.
type ReviewRepository struct {
	db *DB
}

const replaceReviewRequestOperation = "replace_review_request"

var _ ports.ReviewRepository = (*ReviewRepository)(nil)

// NewReviewRepository constructs a review repository.
func NewReviewRepository(db *DB) (*ReviewRepository, error) {
	if db == nil {
		return nil, domain.NewError(domain.CodeStorageConfiguration, "SQLite database is required", false)
	}
	return &ReviewRepository{db: db}, nil
}

// CreateReviewRequest inserts a new review request and its target snapshot.
func (repository *ReviewRepository) CreateReviewRequest(ctx context.Context, command ports.CreateReviewRequestCommand) (ports.CreateReviewRequestResult, error) {
	if repository == nil || repository.db == nil {
		return ports.CreateReviewRequestResult{}, domain.NewError(domain.CodeStorageConfiguration, "SQLite database is required", false)
	}
	if stringsTrimmed(command.RequestID) == "" {
		return ports.CreateReviewRequestResult{}, domain.NewError(domain.CodeInvalidArgument, "request_id is required", false)
	}
	if _, err := ids.ParseStrict(command.RequestID); err != nil {
		return ports.CreateReviewRequestResult{}, domain.NewError(domain.CodeInvalidArgument, "request_id is invalid", false)
	}
	if stringsTrimmed(command.TargetID) == "" {
		return ports.CreateReviewRequestResult{}, domain.NewError(domain.CodeInvalidArgument, "target_id is required", false)
	}
	if _, err := ids.ParseStrict(command.TargetID); err != nil {
		return ports.CreateReviewRequestResult{}, domain.NewError(domain.CodeInvalidArgument, "target_id is invalid", false)
	}
	if stringsTrimmed(command.IssueID) == "" {
		return ports.CreateReviewRequestResult{}, domain.NewError(domain.CodeInvalidArgument, "issue_id is required", false)
	}
	if command.TargetIssueVersion < 1 {
		return ports.CreateReviewRequestResult{}, domain.NewError(domain.CodeInvalidArgument, "target_issue_version must be >= 1", false)
	}
	if command.TargetEventID < 0 {
		return ports.CreateReviewRequestResult{}, domain.NewError(domain.CodeInvalidArgument, "target_event_id must be >= 0", false)
	}
	var result ports.CreateReviewRequestResult
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		target, err := repository.ensureTarget(ctx, tx, command)
		if err != nil {
			return err
		}
		activeRequest, err := repository.loadActiveRequestForTarget(ctx, tx, target.ID)
		if err != nil {
			return err
		}
		if activeRequest != nil {
			if sameArtifactIDs(activeRequest.ArtifactIDs, command.ArtifactIDs) && sameSupersedesID(activeRequest.SupersedesID, command.SupersedesID) && activeRequest.TargetIssueVersion == command.TargetIssueVersion && activeRequest.TargetEventID == command.TargetEventID && activeRequest.IssueID == command.IssueID {
				result.Request = *activeRequest
				result.Target = target
				return nil
			}
			return domain.NewError(domain.CodeReviewAlreadyExists, "review request already exists for target", false)
		}
		requestID := command.RequestID
		artifactIDsJSON, err := jsonMarshalArtifacts(command.ArtifactIDs)
		if err != nil {
			return err
		}
		requestVersion := int64(1)
		createdAt := formatStorageTime(command.OccurredAt)
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_requests(
            id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json,
            status, supersedes_id, active_attempt_id, version, created_at, resolved_at
        ) VALUES (?, ?, ?, ?, ?, ?, 'open', ?, NULL, ?, ?, NULL)`,
			requestID, target.ID, command.IssueID, command.TargetIssueVersion, command.TargetEventID, string(artifactIDsJSON),
			stringOrNil(command.SupersedesID), requestVersion, createdAt,
		); err != nil {
			activeRequest, err := repository.loadActiveRequestForTarget(ctx, tx, target.ID)
			if err != nil {
				return err
			}
			if activeRequest != nil && sameArtifactIDs(activeRequest.ArtifactIDs, command.ArtifactIDs) && sameSupersedesID(activeRequest.SupersedesID, command.SupersedesID) && activeRequest.TargetIssueVersion == command.TargetIssueVersion && activeRequest.TargetEventID == command.TargetEventID && activeRequest.IssueID == command.IssueID {
				result.Request = *activeRequest
				result.Target = target
				return nil
			}
			return err
		}
		if err := appendReviewEvent(ctx, tx, command.IssueID, "review_requested", nil, payloadForReviewEvent(requestID, target.ID, nil, nil, nil), createdAt); err != nil {
			return err
		}
		result.Request = domain.ReviewRequest{
			ID:                 requestID,
			IssueID:            command.IssueID,
			TargetID:           target.ID,
			TargetIssueVersion: command.TargetIssueVersion,
			TargetEventID:      command.TargetEventID,
			ArtifactIDs:        append([]string(nil), command.ArtifactIDs...),
			Status:             domain.ReviewRequestStatusOpen,
			SupersedesID:       copyOptionalString(command.SupersedesID),
			Version:            requestVersion,
			CreatedAt:          parseTimestamp(createdAt),
		}
		result.Target = target
		return nil
	})
	if err != nil {
		return ports.CreateReviewRequestResult{}, err
	}
	return result, nil
}

// GetReviewRequest loads one review request and its target snapshot.
func (repository *ReviewRepository) GetReviewRequest(ctx context.Context, requestID string) (ports.GetReviewRequestResult, error) {
	if repository == nil || repository.db == nil {
		return ports.GetReviewRequestResult{}, domain.NewError(domain.CodeStorageConfiguration, "SQLite database is required", false)
	}
	var request domain.ReviewRequest
	var target domain.ReviewTarget
	err := repository.db.Read(ctx, func(ctx context.Context, queryer Queryer) error {
		var err error
		request, target, err = repository.loadRequestForMutation(ctx, queryer, requestID)
		return err
	})
	if err != nil {
		return ports.GetReviewRequestResult{}, err
	}
	return ports.GetReviewRequestResult{Request: request, Target: target}, nil
}

// ListReviewRequests loads review requests with optional status filtering and offset pagination.
func (repository *ReviewRepository) ListReviewRequests(ctx context.Context, query ports.ListReviewRequestsQuery) (ports.ListReviewRequestsResult, error) {
	if repository == nil || repository.db == nil {
		return ports.ListReviewRequestsResult{}, domain.NewError(domain.CodeStorageConfiguration, "SQLite database is required", false)
	}
	if query.Limit < 1 {
		query.Limit = 20
	}
	if query.Limit > 100 {
		query.Limit = 100
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	var where string
	var args []any
	if query.Status != nil {
		where = "WHERE review_requests.status = ?"
		args = append(args, string(*query.Status))
	}
	var items []domain.ReviewRequest
	err := repository.db.Read(ctx, func(ctx context.Context, queryer Queryer) error {
		rows, err := queryer.QueryContext(ctx, `SELECT review_requests.id, review_requests.target_id, review_requests.issue_id, review_requests.target_issue_version, review_requests.target_event_id, review_requests.artifact_ids_json, review_requests.status, review_requests.supersedes_id, review_requests.active_attempt_id, review_requests.version, review_requests.created_at, review_requests.resolved_at
            FROM review_requests
            LEFT JOIN review_targets ON review_targets.id = review_requests.target_id
            `+where+` ORDER BY review_requests.created_at DESC, review_requests.id DESC LIMIT ? OFFSET ?`, append(args, query.Limit+1, query.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var request domain.ReviewRequest
			var artifactIDsJSON []byte
			var status string
			var supersedesID sql.NullString
			var activeAttemptID sql.NullString
			var createdAtText string
			var resolvedAtText sql.NullString
			if err := rows.Scan(&request.ID, &request.TargetID, &request.IssueID, &request.TargetIssueVersion, &request.TargetEventID, &artifactIDsJSON, &status, &supersedesID, &activeAttemptID, &request.Version, &createdAtText, &resolvedAtText); err != nil {
				return err
			}
			request.ArtifactIDs, err = unmarshalArtifactIDs(artifactIDsJSON)
			if err != nil {
				return err
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
			items = append(items, request)
		}
		return rows.Err()
	})
	if err != nil {
		return ports.ListReviewRequestsResult{}, err
	}
	hasMore := len(items) > query.Limit
	if hasMore {
		items = items[:query.Limit]
	}
	return ports.ListReviewRequestsResult{Items: items, HasMore: hasMore, NextOffset: query.Offset + len(items)}, nil
}

// CancelReviewRequest transitions an open or claimed request to cancelled.
func (repository *ReviewRepository) CancelReviewRequest(ctx context.Context, command ports.ReviewMutationCommand) (ports.ReviewMutationResult, error) {
	if repository == nil || repository.db == nil {
		return ports.ReviewMutationResult{}, domain.NewError(domain.CodeStorageConfiguration, "SQLite database is required", false)
	}
	var result ports.ReviewMutationResult
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		request, target, err := repository.loadRequestForMutation(ctx, tx, command.RequestID)
		if err != nil {
			return err
		}
		if request.Version != command.ExpectedVersion {
			return domain.NewError(domain.CodeVersionConflict, "review request version conflict", true)
		}
		if request.Status != domain.ReviewRequestStatusOpen && request.Status != domain.ReviewRequestStatusClaimed {
			return domain.NewError(domain.CodeInvalidArgument, "review request cannot be cancelled", false)
		}
		resolvedAt := formatStorageTime(command.OccurredAt)
		if _, err := tx.ExecContext(ctx, `UPDATE review_requests SET status = ?, active_attempt_id = NULL, resolved_at = ?, version = version + 1 WHERE id = ? AND version = ?`, domain.ReviewRequestStatusCancelled, resolvedAt, request.ID, request.Version); err != nil {
			return err
		}
		if err := appendReviewEvent(ctx, tx, request.IssueID, "review_cancelled", nil, payloadForReviewEvent(request.ID, request.TargetID, nil, nil, nil), resolvedAt); err != nil {
			return err
		}
		request.Status = domain.ReviewRequestStatusCancelled
		request.ActiveAttemptID = nil
		request.ResolvedAt = pointerTime(parseTimestamp(resolvedAt))
		request.Version += 1
		result.Request = request
		result.Target = target
		return nil
	})
	if err != nil {
		return ports.ReviewMutationResult{}, err
	}
	return result, nil
}

// LookupReplaceReviewRequest serves a replay before any write is attempted.
// ReplaceReviewRequest still repeats this check in its writer transaction to
// close the lookup/write race.
func (repository *ReviewRepository) LookupReplaceReviewRequest(ctx context.Context, key string, hash []byte) (ports.ReplaceReviewRequestResult, bool, error) {
	var result ports.ReplaceReviewRequestResult
	var found bool
	err := repository.db.Read(ctx, func(ctx context.Context, query Queryer) error {
		var savedHash []byte
		var savedResponse string
		err := query.QueryRowContext(ctx, `SELECT request_hash, response_json FROM idempotency_records
			WHERE operation = ? AND idempotency_key = ?`, replaceReviewRequestOperation, key).Scan(&savedHash, &savedResponse)
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

// ReplaceReviewRequest atomically supersedes a predecessor review request and
// creates its open successor. Rejecting a claimed predecessor here (rather
// than detaching its active attempt) is deliberate: this operation does not
// hold the attempt's lease token, so the lease holder must finish or
// interrupt its own attempt first.
func (repository *ReviewRepository) ReplaceReviewRequest(ctx context.Context, command ports.ReplaceReviewRequestCommand) (ports.ReplaceReviewRequestResult, error) {
	if repository == nil || repository.db == nil {
		return ports.ReplaceReviewRequestResult{}, domain.NewError(domain.CodeStorageConfiguration, "SQLite database is required", false)
	}
	if stringsTrimmed(command.SuccessorID) == "" {
		return ports.ReplaceReviewRequestResult{}, domain.NewError(domain.CodeInvalidArgument, "successor_id is required", false)
	}
	if _, err := ids.ParseStrict(command.SuccessorID); err != nil {
		return ports.ReplaceReviewRequestResult{}, domain.NewError(domain.CodeInvalidArgument, "successor_id is invalid", false)
	}
	if stringsTrimmed(command.SuccessorTargetID) == "" {
		return ports.ReplaceReviewRequestResult{}, domain.NewError(domain.CodeInvalidArgument, "successor_target_id is required", false)
	}
	if _, err := ids.ParseStrict(command.SuccessorTargetID); err != nil {
		return ports.ReplaceReviewRequestResult{}, domain.NewError(domain.CodeInvalidArgument, "successor_target_id is invalid", false)
	}
	var result ports.ReplaceReviewRequestResult
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		if command.IdempotencyKey != "" {
			var savedHash []byte
			var savedResponse string
			err := tx.QueryRowContext(ctx, `SELECT request_hash, response_json FROM idempotency_records
				WHERE operation = ? AND idempotency_key = ?`, replaceReviewRequestOperation, command.IdempotencyKey).Scan(&savedHash, &savedResponse)
			switch {
			case err == nil:
				if !bytes.Equal(savedHash, command.RequestHash) {
					return domain.NewError(domain.CodeIdempotencyConflict, "idempotency key was used with a different request", false,
						domain.Detail{Field: "idempotency_key", Code: domain.CodeIdempotencyConflict})
				}
				return json.Unmarshal([]byte(savedResponse), &result)
			case err == sql.ErrNoRows:
			default:
				return err
			}
		}

		predecessor, _, err := repository.loadRequestForMutation(ctx, tx, command.PredecessorRequestID)
		if err != nil {
			return err
		}
		if predecessor.Version != command.PredecessorExpectedVersion {
			return domain.NewError(domain.CodeVersionConflict, "review request version conflict", true)
		}
		switch predecessor.Status {
		case domain.ReviewRequestStatusOpen:
			// only a currently open predecessor can be replaced.
		case domain.ReviewRequestStatusClaimed:
			return domain.NewError(domain.CodeReviewRequestClaimed,
				"review request is claimed; finish or interrupt the active attempt before replacing it", false)
		default:
			return domain.NewError(domain.CodeReviewRequestNotReplaceable, "review request cannot be replaced", false)
		}

		target, err := repository.ensureTarget(ctx, tx, ports.CreateReviewRequestCommand{
			RequestID:          "",
			TargetID:           command.SuccessorTargetID,
			IssueID:            predecessor.IssueID,
			TargetIssueVersion: command.TargetIssueVersion,
			TargetEventID:      command.TargetEventID,
			ArtifactIDs:        command.ArtifactIDs,
			OccurredAt:         command.OccurredAt,
		})
		if err != nil {
			return err
		}
		activeForTarget, err := repository.loadActiveRequestForTarget(ctx, tx, target.ID)
		if err != nil {
			return err
		}
		if activeForTarget != nil && activeForTarget.ID != predecessor.ID {
			return domain.NewError(domain.CodeReviewAlreadyExists, "review request already exists for target", false)
		}

		occurredAt := formatStorageTime(command.OccurredAt)
		if _, err := tx.ExecContext(ctx, `UPDATE review_requests SET status = ?, active_attempt_id = NULL, resolved_at = ?, version = version + 1 WHERE id = ? AND version = ?`,
			domain.ReviewRequestStatusSuperseded, occurredAt, predecessor.ID, predecessor.Version); err != nil {
			return err
		}

		successorID := command.SuccessorID
		artifactIDsJSON, err := jsonMarshalArtifacts(command.ArtifactIDs)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_requests(
            id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json,
            status, supersedes_id, active_attempt_id, version, created_at, resolved_at
        ) VALUES (?, ?, ?, ?, ?, ?, 'open', ?, NULL, 1, ?, NULL)`,
			successorID, target.ID, predecessor.IssueID, command.TargetIssueVersion, command.TargetEventID, string(artifactIDsJSON),
			predecessor.ID, occurredAt,
		); err != nil {
			return err
		}

		if err := appendReviewEvent(ctx, tx, predecessor.IssueID, "review_superseded", nil,
			payloadForReplaceReviewEvent(predecessor.ID, predecessor.TargetID, successorID, ""), occurredAt); err != nil {
			return err
		}
		if err := appendReviewEvent(ctx, tx, predecessor.IssueID, "review_requested", nil,
			payloadForReplaceReviewEvent(successorID, target.ID, "", predecessor.ID), occurredAt); err != nil {
			return err
		}

		latestEventID, err := latestIssueEventIDInTransaction(ctx, tx)
		if err != nil {
			return err
		}

		predecessor.Status = domain.ReviewRequestStatusSuperseded
		predecessor.ActiveAttemptID = nil
		predecessor.ResolvedAt = pointerTime(parseTimestamp(occurredAt))
		predecessor.Version++

		supersedesID := predecessor.ID
		result = ports.ReplaceReviewRequestResult{
			Predecessor: predecessor,
			Successor: domain.ReviewRequest{
				ID:                 successorID,
				IssueID:            predecessor.IssueID,
				TargetID:           target.ID,
				TargetIssueVersion: command.TargetIssueVersion,
				TargetEventID:      command.TargetEventID,
				ArtifactIDs:        append([]string(nil), command.ArtifactIDs...),
				Status:             domain.ReviewRequestStatusOpen,
				SupersedesID:       &supersedesID,
				Version:            1,
				CreatedAt:          parseTimestamp(occurredAt),
			},
			SuccessorTarget: target,
			LatestEventID:   latestEventID,
		}

		if command.IdempotencyKey != "" {
			response, err := json.Marshal(result)
			if err != nil {
				return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode replace review request response", false)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO idempotency_records(
				idempotency_key, operation, request_hash, response_json, created_at
			) VALUES (?, ?, ?, ?, ?)`, command.IdempotencyKey, replaceReviewRequestOperation, command.RequestHash, string(response), occurredAt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ports.ReplaceReviewRequestResult{}, err
	}
	return result, nil
}

// ClaimReviewRequest transitions an open request to claimed with a review attempt.
func (repository *ReviewRepository) ClaimReviewRequest(ctx context.Context, command ports.ReviewMutationCommand) (ports.ReviewMutationResult, error) {
	if repository == nil || repository.db == nil {
		return ports.ReviewMutationResult{}, domain.NewError(domain.CodeStorageConfiguration, "SQLite database is required", false)
	}
	if command.ActiveAttemptID == nil || stringsTrimmed(*command.ActiveAttemptID) == "" {
		return ports.ReviewMutationResult{}, domain.NewError(domain.CodeInvalidArgument, "active_attempt_id is required", false)
	}
	var result ports.ReviewMutationResult
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		request, target, err := repository.loadRequestForMutation(ctx, tx, command.RequestID)
		if err != nil {
			return err
		}
		if request.Version != command.ExpectedVersion {
			return domain.NewError(domain.CodeVersionConflict, "review request version conflict", true)
		}
		if request.Status != domain.ReviewRequestStatusOpen {
			return domain.NewError(domain.CodeInvalidArgument, "review request cannot be claimed", false)
		}
		var attemptIssueID, attemptKind, attemptStatus, leaseExpiresAtText string
		if err := tx.QueryRowContext(ctx, `SELECT issue_id, kind, status, lease_expires_at FROM work_attempts WHERE id = ?`, *command.ActiveAttemptID).Scan(&attemptIssueID, &attemptKind, &attemptStatus, &leaseExpiresAtText); err != nil {
			if isNoRowsError(err) {
				return domain.NewError(domain.CodeAttemptNotFound, "review attempt not found", false)
			}
			return err
		}
		if attemptStatus != string(domain.AttemptStatusActive) {
			return domain.NewError(domain.CodeAttemptNotActive, "review attempt is not active", false)
		}
		if attemptKind != string(domain.AttemptKindReview) {
			return domain.NewError(domain.CodeInvalidArgument, "attempt is not a review attempt", false)
		}
		if attemptIssueID != request.IssueID {
			return domain.NewError(domain.CodeInvalidArgument, "attempt does not belong to the review request issue", false)
		}
		leaseExpiresAt, err := parseIssueTimestamp("lease_expires_at", leaseExpiresAtText)
		if err != nil {
			return err
		}
		if !leaseExpiresAt.After(command.OccurredAt.UTC()) {
			return domain.NewError(domain.CodeLeaseExpired, "review attempt lease has expired", false)
		}
		var assignedRequestID string
		err = tx.QueryRowContext(ctx, `SELECT id FROM review_requests WHERE active_attempt_id = ? AND id <> ? AND status IN ('open','claimed')`, *command.ActiveAttemptID, request.ID).Scan(&assignedRequestID)
		switch {
		case err == nil:
			return domain.NewError(domain.CodeActiveAttemptExists, "review attempt is already assigned to another review request", false)
		case !isNoRowsError(err):
			return err
		}
		claimedAt := formatStorageTime(command.OccurredAt)
		if _, err := tx.ExecContext(ctx, `UPDATE review_requests SET status = ?, active_attempt_id = ?, resolved_at = NULL, version = version + 1 WHERE id = ? AND version = ?`, domain.ReviewRequestStatusClaimed, *command.ActiveAttemptID, request.ID, request.Version); err != nil {
			return err
		}
		if err := appendReviewEvent(ctx, tx, request.IssueID, "review_claimed", command.ActiveAttemptID,
			payloadForReviewEvent(request.ID, request.TargetID, command.ActiveAttemptID, nil, nil), claimedAt); err != nil {
			return err
		}
		request.Status = domain.ReviewRequestStatusClaimed
		request.ActiveAttemptID = copyOptionalString(command.ActiveAttemptID)
		request.ResolvedAt = nil
		request.Version += 1
		result.Request = request
		result.Target = target
		return nil
	})
	if err != nil {
		return ports.ReviewMutationResult{}, err
	}
	return result, nil
}

// ResolveReviewRequest transitions a claimed request to an outcome state.
func (repository *ReviewRepository) ResolveReviewRequest(ctx context.Context, command ports.ResolveReviewRequestCommand) (ports.ResolveReviewRequestResult, error) {
	if repository == nil || repository.db == nil {
		return ports.ResolveReviewRequestResult{}, domain.NewError(domain.CodeStorageConfiguration, "SQLite database is required", false)
	}
	if stringsTrimmed(command.OutcomeID) == "" {
		return ports.ResolveReviewRequestResult{}, domain.NewError(domain.CodeInvalidArgument, "outcome_id is required", false)
	}
	if _, err := ids.ParseStrict(command.OutcomeID); err != nil {
		return ports.ResolveReviewRequestResult{}, domain.NewError(domain.CodeInvalidArgument, "outcome_id is invalid", false)
	}
	if command.AttemptID == "" {
		return ports.ResolveReviewRequestResult{}, domain.NewError(domain.CodeInvalidArgument, "attempt_id is required", false)
	}
	if !command.Outcome.Valid() {
		return ports.ResolveReviewRequestResult{}, domain.NewError(domain.CodeInvalidArgument, "review outcome is invalid", false)
	}
	var result ports.ResolveReviewRequestResult
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		request, target, err := repository.loadRequestForMutation(ctx, tx, command.RequestID)
		if err != nil {
			return err
		}
		if request.Version != command.ExpectedVersion {
			return domain.NewError(domain.CodeVersionConflict, "review request version conflict", true)
		}
		if request.Status != domain.ReviewRequestStatusClaimed {
			return domain.NewError(domain.CodeInvalidArgument, "review request cannot be resolved", false)
		}
		if request.ActiveAttemptID == nil || *request.ActiveAttemptID != command.AttemptID {
			return domain.NewError(domain.CodeInvalidArgument, "attempt_id does not match the active review attempt", false)
		}
		nextStatus := reviewRequestStatusForOutcome(command.Outcome)
		resolvedAt := formatStorageTime(command.OccurredAt)
		outcomeID := command.OutcomeID
		if _, err := tx.ExecContext(ctx, `UPDATE review_requests SET status = ?, active_attempt_id = NULL, resolved_at = ?, version = version + 1 WHERE id = ? AND version = ?`, nextStatus, resolvedAt, request.ID, request.Version); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_outcomes(id, request_id, attempt_id, outcome, reason, version, created_at) VALUES (?, ?, ?, ?, ?, 1, ?)`, outcomeID, request.ID, command.AttemptID, command.Outcome, stringOrNil(command.Reason), resolvedAt); err != nil {
			return err
		}
		if err := appendReviewEvent(ctx, tx, request.IssueID, string(reviewEventTypeForOutcome(command.Outcome)), &command.AttemptID,
			payloadForReviewEvent(request.ID, request.TargetID, &command.AttemptID, &command.Outcome, command.Reason), resolvedAt); err != nil {
			return err
		}
		request.Status = nextStatus
		request.ActiveAttemptID = nil
		request.ResolvedAt = pointerTime(parseTimestamp(resolvedAt))
		request.Version += 1
		result.Request = request
		result.Target = target
		result.Outcome = domain.ReviewOutcomeRecord{ID: outcomeID, RequestID: request.ID, AttemptID: command.AttemptID, Outcome: command.Outcome, Reason: copyReviewOptionalString(command.Reason), Version: 1, CreatedAt: parseTimestamp(resolvedAt)}
		return nil
	})
	if err != nil {
		return ports.ResolveReviewRequestResult{}, err
	}
	return result, nil
}

func (repository *ReviewRepository) ensureTarget(ctx context.Context, tx Executor, command ports.CreateReviewRequestCommand) (domain.ReviewTarget, error) {
	artifactIDsJSON, err := jsonMarshalArtifacts(command.ArtifactIDs)
	if err != nil {
		return domain.ReviewTarget{}, err
	}
	createdAt := formatStorageTime(command.OccurredAt)

	var row reviewTargetRow
	err = tx.QueryRowContext(ctx, `SELECT id, issue_id, issue_version, latest_event_id, artifact_ids_json, version, created_at
        FROM review_targets WHERE issue_id = ? AND issue_version = ?`, command.IssueID, command.TargetIssueVersion).Scan(
		&row.ID, &row.IssueID, &row.IssueVersion, &row.LatestEventID, &row.ArtifactIDsJSON, &row.Version, &row.CreatedAtText,
	)
	switch {
	case err == nil:
		artifactIDs, err := unmarshalArtifactIDs(row.ArtifactIDsJSON)
		if err != nil {
			return domain.ReviewTarget{}, err
		}
		if sameArtifactIDs(artifactIDs, command.ArtifactIDs) && row.LatestEventID == command.TargetEventID {
			return reviewTargetFromRow(row), nil
		}
		return domain.ReviewTarget{}, domain.NewError(domain.CodeReviewAlreadyExists, "review request target does not match the existing target", false)
	case isNoRowsError(err):
		targetID := command.TargetID
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_targets(id, issue_id, issue_version, latest_event_id, artifact_ids_json, version, created_at)
			VALUES (?, ?, ?, ?, ?, 1, ?)`, targetID, command.IssueID, command.TargetIssueVersion, command.TargetEventID, string(artifactIDsJSON), createdAt); err != nil {
			var existing reviewTargetRow
			err = tx.QueryRowContext(ctx, `SELECT id, issue_id, issue_version, latest_event_id, artifact_ids_json, version, created_at
                FROM review_targets WHERE issue_id = ? AND issue_version = ?`, command.IssueID, command.TargetIssueVersion).Scan(
				&existing.ID, &existing.IssueID, &existing.IssueVersion, &existing.LatestEventID, &existing.ArtifactIDsJSON, &existing.Version, &existing.CreatedAtText,
			)
			if err != nil {
				if isNoRowsError(err) {
					return domain.ReviewTarget{}, err
				}
				return domain.ReviewTarget{}, err
			}
			artifactIDs, err := unmarshalArtifactIDs(existing.ArtifactIDsJSON)
			if err != nil {
				return domain.ReviewTarget{}, err
			}
			if sameArtifactIDs(artifactIDs, command.ArtifactIDs) && existing.LatestEventID == command.TargetEventID {
				return reviewTargetFromRow(existing), nil
			}
			return domain.ReviewTarget{}, domain.NewError(domain.CodeReviewAlreadyExists, "review request target does not match the existing target", false)
		}
		return domain.ReviewTarget{
			ID:            targetID,
			IssueID:       command.IssueID,
			IssueVersion:  command.TargetIssueVersion,
			LatestEventID: command.TargetEventID,
			ArtifactIDs:   append([]string(nil), command.ArtifactIDs...),
			Version:       1,
			CreatedAt:     parseTimestamp(createdAt),
		}, nil
	default:
		return domain.ReviewTarget{}, err
	}
}

func (repository *ReviewRepository) loadActiveRequestForTarget(ctx context.Context, queryer Queryer, targetID string) (*domain.ReviewRequest, error) {
	var request domain.ReviewRequest
	var artifactIDsJSON []byte
	var status string
	var supersedesID sql.NullString
	var activeAttemptID sql.NullString
	var createdAtText string
	var resolvedAtText sql.NullString
	err := queryer.QueryRowContext(ctx, `SELECT id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, status, supersedes_id, active_attempt_id, version, created_at, resolved_at
        FROM review_requests WHERE target_id = ? AND status IN ('open','claimed') ORDER BY created_at DESC LIMIT 1`, targetID).Scan(
		&request.ID, &request.TargetID, &request.IssueID, &request.TargetIssueVersion, &request.TargetEventID, &artifactIDsJSON, &status, &supersedesID, &activeAttemptID, &request.Version, &createdAtText, &resolvedAtText,
	)
	if err != nil {
		if isNoRowsError(err) {
			return nil, nil
		}
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

func (repository *ReviewRepository) loadRequestForMutation(ctx context.Context, queryer Queryer, requestID string) (domain.ReviewRequest, domain.ReviewTarget, error) {
	var request domain.ReviewRequest
	var target domain.ReviewTarget
	var artifactIDsJSON []byte
	var status string
	var supersedesID sql.NullString
	var activeAttemptID sql.NullString
	var createdAtText string
	var resolvedAtText sql.NullString
	var targetArtifactIDsJSON []byte
	var targetCreatedAtText string
	err := queryer.QueryRowContext(ctx, `SELECT review_requests.id, review_requests.target_id, review_requests.issue_id, review_requests.target_issue_version, review_requests.target_event_id, review_requests.artifact_ids_json, review_requests.status, review_requests.supersedes_id, review_requests.active_attempt_id, review_requests.version, review_requests.created_at, review_requests.resolved_at,
        review_targets.id, review_targets.issue_id, review_targets.issue_version, review_targets.latest_event_id, review_targets.artifact_ids_json, review_targets.version, review_targets.created_at
        FROM review_requests
        LEFT JOIN review_targets ON review_targets.id = review_requests.target_id
        WHERE review_requests.id = ?`, requestID).Scan(
		&request.ID, &request.TargetID, &request.IssueID, &request.TargetIssueVersion, &request.TargetEventID, &artifactIDsJSON, &status, &supersedesID, &activeAttemptID, &request.Version, &createdAtText, &resolvedAtText,
		&target.ID, &target.IssueID, &target.IssueVersion, &target.LatestEventID, &targetArtifactIDsJSON, &target.Version, &targetCreatedAtText,
	)
	if err != nil {
		if isNoRowsError(err) {
			return domain.ReviewRequest{}, domain.ReviewTarget{}, domain.NewError(domain.CodeIssueNotFound, "review request not found", false)
		}
		return domain.ReviewRequest{}, domain.ReviewTarget{}, err
	}
	request.ArtifactIDs, err = unmarshalArtifactIDs(artifactIDsJSON)
	if err != nil {
		return domain.ReviewRequest{}, domain.ReviewTarget{}, err
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
	target.ArtifactIDs, err = unmarshalArtifactIDs(targetArtifactIDsJSON)
	if err != nil {
		return domain.ReviewRequest{}, domain.ReviewTarget{}, err
	}
	if targetCreatedAtText != "" {
		target.CreatedAt = parseTimestamp(targetCreatedAtText)
	}
	return request, target, nil
}

func reviewRequestStatusForOutcome(outcome domain.ReviewOutcome) domain.ReviewRequestStatus {
	switch outcome {
	case domain.ReviewOutcomeApproved:
		return domain.ReviewRequestStatusApproved
	case domain.ReviewOutcomeChangesRequested:
		return domain.ReviewRequestStatusChangesRequested
	case domain.ReviewOutcomeBlocked:
		return domain.ReviewRequestStatusBlocked
	default:
		return domain.ReviewRequestStatusCancelled
	}
}

func reviewEventTypeForOutcome(outcome domain.ReviewOutcome) domain.ReviewEventType {
	switch outcome {
	case domain.ReviewOutcomeApproved:
		return domain.ReviewEventTypeApproved
	case domain.ReviewOutcomeChangesRequested:
		return domain.ReviewEventTypeChangesRequested
	case domain.ReviewOutcomeBlocked:
		return domain.ReviewEventTypeBlocked
	default:
		return domain.ReviewEventTypeCancelled
	}
}

// appendReviewEvent inserts one review-lifecycle event into the unified
// issue_events table (source='review'). This is the single insertion point
// every review event append site in this package uses, so review events
// fully participate in GetChanges, staleness, and activity alongside plain
// issue events (docs/02, docs/04 §7.1, ISSUE-190) -- there is no longer a
// second, independently-sequenced review_events table to fall out of sync
// with issue_events' AUTOINCREMENT sequence. requestID/targetID are not
// separate columns; they are already embedded in payload by
// payloadForReviewEvent, the same place they were always recorded.
func appendReviewEvent(ctx context.Context, tx Executor, issueID, eventType string, attemptID *string, payload []byte, createdAt string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_type, session_id, attempt_id, payload, created_at, source)
		VALUES (?, ?, NULL, ?, ?, ?, 'review')`, issueID, eventType, nullableStringValuePtr(attemptID), string(payload), createdAt)
	return err
}

func payloadForReviewEvent(requestID, targetID string, attemptID *string, outcome *domain.ReviewOutcome, reason *string) []byte {
	payload := struct {
		RequestID string  `json:"request_id"`
		TargetID  string  `json:"target_id"`
		AttemptID *string `json:"attempt_id,omitempty"`
		Outcome   *string `json:"outcome,omitempty"`
		Reason    *string `json:"reason,omitempty"`
	}{
		RequestID: requestID,
		TargetID:  targetID,
		AttemptID: copyReviewOptionalString(attemptID),
	}
	if outcome != nil {
		value := string(*outcome)
		payload.Outcome = &value
	}
	if reason != nil {
		payload.Reason = copyReviewOptionalString(reason)
	}
	data, _ := json.Marshal(payload)
	return data
}

// payloadForReplaceReviewEvent builds the review_events payload for one side
// of an atomic replacement, cross-referencing the other request so the event
// stream alone shows which request replaced (or was replaced by) which.
// Exactly one of successorID/predecessorID is set per call.
func payloadForReplaceReviewEvent(requestID, targetID, successorID, predecessorID string) []byte {
	payload := struct {
		RequestID     string  `json:"request_id"`
		TargetID      string  `json:"target_id"`
		SuccessorID   *string `json:"successor_id,omitempty"`
		PredecessorID *string `json:"predecessor_id,omitempty"`
	}{RequestID: requestID, TargetID: targetID}
	if successorID != "" {
		payload.SuccessorID = &successorID
	}
	if predecessorID != "" {
		payload.PredecessorID = &predecessorID
	}
	data, _ := json.Marshal(payload)
	return data
}

func sameArtifactIDs(left []string, right []string) bool {
	return reflect.DeepEqual(left, right)
}

func sameSupersedesID(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func jsonMarshalArtifacts(values []string) ([]byte, error) {
	if values == nil {
		values = []string{}
	}
	return json.Marshal(values)
}

func unmarshalArtifactIDs(data []byte) ([]string, error) {
	if len(data) == 0 {
		return []string{}, nil
	}
	var values []string
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func stringOrNil(value *string) any {
	if value == nil {
		return nil
	}
	copyValue := *value
	return copyValue
}

func copyReviewOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func pointerTime(value time.Time) *time.Time {
	return &value
}

func parseTimestamp(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	parsed, err := parseStorageTime(value)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func stringsTrimmed(value string) string {
	return strings.TrimSpace(value)
}

func isNoRowsError(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || err != nil && err.Error() == sql.ErrNoRows.Error()
}

type reviewTargetRow struct {
	ID              string
	IssueID         string
	IssueVersion    int64
	LatestEventID   int64
	ArtifactIDsJSON []byte
	Version         int64
	CreatedAtText   string
}

func reviewTargetFromRow(row reviewTargetRow) domain.ReviewTarget {
	artifactIDs, _ := unmarshalArtifactIDs(row.ArtifactIDsJSON)
	return domain.ReviewTarget{
		ID:            row.ID,
		IssueID:       row.IssueID,
		IssueVersion:  row.IssueVersion,
		LatestEventID: row.LatestEventID,
		ArtifactIDs:   artifactIDs,
		Version:       row.Version,
		CreatedAt:     parseTimestamp(row.CreatedAtText),
	}
}
