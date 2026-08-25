package sqlite

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

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
// reviewTargetStale reports whether a frozen review target still describes
// the issue as it is right now. Two independent questions, both of which
// docs/09 "Staleness and concurrency" treats as staleness:
//
//   - the issue record itself moved on (its version no longer matches the
//     version the target froze), and
//   - the reviewed work changed after the target's event position, judged by
//     reviewedWorkChangedSince (attempts.go) so the answer is issue-scoped
//     and position-based, never an equality test against a recomputed
//     maximum event id (ISSUE-189).
//
// Both are asked inside the caller's own write transaction, so a request can
// neither be born stale (create/replace) nor be handed to a reviewer after it
// went stale (claim).
func reviewTargetStale(ctx context.Context, queryer Queryer, issueID string, targetIssueVersion, targetEventID int64) (bool, error) {
	var currentVersion int64
	if err := queryer.QueryRowContext(ctx, `SELECT version FROM issues WHERE id = ?`, issueID).Scan(&currentVersion); err != nil {
		if isNoRowsError(err) {
			return false, domain.NewError(domain.CodeIssueNotFound, "issue not found", false)
		}
		return false, err
	}
	if currentVersion != targetIssueVersion {
		return true, nil
	}
	return reviewedWorkChangedSince(ctx, queryer, issueID, targetEventID)
}

func staleReviewTargetError() *domain.Error {
	return domain.NewError(domain.CodeReviewTargetStale, "review target is stale", false)
}

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
	purposes, err := domain.ValidateReviewPurposes(command.Purposes)
	if err != nil {
		return ports.CreateReviewRequestResult{}, err
	}
	command.Purposes = purposes
	var result ports.CreateReviewRequestResult
	err = repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		target, err := repository.ensureTarget(ctx, tx, command)
		if err != nil {
			return err
		}
		activeRequest, err := repository.loadActiveRequestForTarget(ctx, tx, target.ID)
		if err != nil {
			return err
		}
		if activeRequest != nil {
			if sameArtifactIDs(activeRequest.ArtifactIDs, command.ArtifactIDs) && sameSupersedesID(activeRequest.SupersedesID, command.SupersedesID) && activeRequest.TargetIssueVersion == command.TargetIssueVersion && activeRequest.TargetEventID == command.TargetEventID && activeRequest.IssueID == command.IssueID && samePurposes(activeRequest.Purposes, command.Purposes) {
				result.Request = *activeRequest
				result.Target = target
				return nil
			}
			return domain.NewError(domain.CodeReviewAlreadyExists, "review request already exists for target", false)
		}
		// A request whose target no longer matches the issue must never be
		// born: it would be advertised as claimable, consume a reviewer's
		// attempt, and only fail at finish (ISSUE-188).
		stale, err := reviewTargetStale(ctx, tx, command.IssueID, command.TargetIssueVersion, command.TargetEventID)
		if err != nil {
			return err
		}
		if stale {
			return staleReviewTargetError()
		}
		if err := requireReviewPurposeCoverage(ctx, tx, target.ID, command.Purposes); err != nil {
			return err
		}
		requestID := command.RequestID
		artifactIDsJSON, err := jsonMarshalArtifacts(command.ArtifactIDs)
		if err != nil {
			return err
		}
		purposesJSON, err := marshalReviewPurposes(command.Purposes)
		if err != nil {
			return err
		}
		requestVersion := int64(1)
		createdAt := formatStorageTime(command.OccurredAt)
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_requests(
            id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, purposes_json,
            status, supersedes_id, active_attempt_id, version, created_at, resolved_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, 'open', ?, NULL, ?, ?, NULL)`,
			requestID, target.ID, command.IssueID, command.TargetIssueVersion, command.TargetEventID, string(artifactIDsJSON), string(purposesJSON),
			stringOrNil(command.SupersedesID), requestVersion, createdAt,
		); err != nil {
			activeRequest, err := repository.loadActiveRequestForTarget(ctx, tx, target.ID)
			if err != nil {
				return err
			}
			if activeRequest != nil && sameArtifactIDs(activeRequest.ArtifactIDs, command.ArtifactIDs) && sameSupersedesID(activeRequest.SupersedesID, command.SupersedesID) && activeRequest.TargetIssueVersion == command.TargetIssueVersion && activeRequest.TargetEventID == command.TargetEventID && activeRequest.IssueID == command.IssueID && samePurposes(activeRequest.Purposes, command.Purposes) {
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
			Purposes:           append([]string(nil), command.Purposes...),
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
	var stale bool
	err := repository.db.Read(ctx, func(ctx context.Context, queryer Queryer) error {
		var err error
		request, target, err = repository.loadRequestForMutation(ctx, queryer, requestID)
		if err != nil {
			return err
		}
		stale, err = reviewTargetStale(ctx, queryer, request.IssueID, request.TargetIssueVersion, request.TargetEventID)
		return err
	})
	if err != nil {
		return ports.GetReviewRequestResult{}, err
	}
	return ports.GetReviewRequestResult{Request: request, Target: target, TargetStale: stale}, nil
}

// ListReviewRequests loads review requests with optional status filtering and offset pagination.
func (repository *ReviewRepository) ListReviewRequests(ctx context.Context, query ports.ListReviewRequestsQuery) (ports.ListReviewRequestsResult, error) {
	if repository == nil || repository.db == nil {
		return ports.ListReviewRequestsResult{}, domain.NewError(domain.CodeStorageConfiguration, "SQLite database is required", false)
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
	staleTargets := map[string]bool{}
	err := repository.db.Read(ctx, func(ctx context.Context, queryer Queryer) error {
		rows, err := queryer.QueryContext(ctx, `SELECT `+reviewRequestColumnsQualified+`
            FROM review_requests
            LEFT JOIN review_targets ON review_targets.id = review_requests.target_id
            `+where+` ORDER BY review_requests.created_at DESC, review_requests.id DESC LIMIT ? OFFSET ?`, append(args, query.Limit+1, query.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			request, err := scanReviewRequestRow(rows)
			if err != nil {
				return err
			}
			items = append(items, request)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		// The cursor is closed before the per-request staleness queries run:
		// they reuse this same connection, and issuing them while the page's
		// own rows are still open would contend with it.
		if err := rows.Close(); err != nil {
			return err
		}
		for _, request := range items {
			stale, err := reviewTargetStale(ctx, queryer, request.IssueID, request.TargetIssueVersion, request.TargetEventID)
			if err != nil {
				return err
			}
			if stale {
				staleTargets[request.ID] = true
			}
		}
		return nil
	})
	if err != nil {
		return ports.ListReviewRequestsResult{}, err
	}
	hasMore := len(items) > query.Limit
	if hasMore {
		items = items[:query.Limit]
	}
	return ports.ListReviewRequestsResult{Items: items, HasMore: hasMore, NextOffset: query.Offset + len(items), StaleTargets: staleTargets}, nil
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
		// Cancelling a claimed request must end the review attempt it bound,
		// not merely detach it. A detached-but-active review lease keeps its
		// full authority: FinishAttempt reads an unbound review attempt whose
		// issue has no unresolved request as an *optional* review and accepts
		// review_outcome=approved, so a cancelled reviewer could still drive
		// the reviewed issue to done (ISSUE-228). Terminating the attempt in
		// this same transaction revokes that authority irrevocably, because
		// every attempt operation authenticates against status = 'active'
		// first (authenticateActiveAttempt in attempts.go).
		terminatedAttemptID := copyOptionalString(request.ActiveAttemptID)
		if _, err := tx.ExecContext(ctx, `UPDATE review_requests SET status = ?, active_attempt_id = NULL, resolved_at = ?, version = version + 1 WHERE id = ? AND version = ?`, domain.ReviewRequestStatusCancelled, resolvedAt, request.ID, request.Version); err != nil {
			return err
		}
		if terminatedAttemptID != nil {
			// AttemptStatusCancelled deliberately does not abandon a review
			// claim (terminalStatusAbandonsReviewClaim): the request is
			// already resolved as cancelled above and must not be handed back
			// to the next reviewer as open. Review attempts hold no
			// reservations (docs/12 §12.3), so the release reason only
			// matters if that rule is ever relaxed; force_released matches
			// the other termination a party outside the lease performs.
			terminated, err := terminateAttempt(ctx, tx, *terminatedAttemptID, domain.AttemptStatusCancelled,
				terminateAttemptReason{ReservationReleaseReason: domain.ReservationReleaseReasonForceReleased}, command.OccurredAt)
			if err != nil {
				return err
			}
			// A bound attempt that was not active is already terminal; its
			// authority is gone and there is nothing to record.
			if !terminated {
				terminatedAttemptID = nil
			}
		}
		if err := appendReviewEvent(ctx, tx, request.IssueID, "review_cancelled", terminatedAttemptID, payloadForReviewEvent(request.ID, request.TargetID, terminatedAttemptID, nil, nil), resolvedAt); err != nil {
			return err
		}
		if terminatedAttemptID != nil {
			if err := appendAttemptCancelledEvent(ctx, tx, request.IssueID, *terminatedAttemptID, resolvedAt); err != nil {
				return err
			}
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

		// The successor freezes a new target, so it is held to the same rule
		// as a fresh create: replacing a stale request with another stale
		// one just moves the eventual finish-time failure (ISSUE-188).
		stale, err := reviewTargetStale(ctx, tx, predecessor.IssueID, command.TargetIssueVersion, command.TargetEventID)
		if err != nil {
			return err
		}
		if stale {
			return staleReviewTargetError()
		}

		// A caller that names no purposes inherits the predecessor's --
		// there is nothing else to inherit from at this layer (domain
		// validation has no predecessor to read), and re-review typically
		// continues covering the same scope. A caller that does name
		// purposes gets exactly those, already validated and normalized.
		purposes := command.Purposes
		if len(purposes) == 0 {
			purposes = predecessor.Purposes
		}

		target, err := repository.ensureTarget(ctx, tx, ports.CreateReviewRequestCommand{
			RequestID:          "",
			TargetID:           command.SuccessorTargetID,
			IssueID:            predecessor.IssueID,
			TargetIssueVersion: command.TargetIssueVersion,
			TargetEventID:      command.TargetEventID,
			ArtifactIDs:        command.ArtifactIDs,
			Purposes:           purposes,
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
		if err := requireReviewPurposeCoverage(ctx, tx, target.ID, purposes); err != nil {
			return err
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
		purposesJSON, err := marshalReviewPurposes(purposes)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_requests(
            id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, purposes_json,
            status, supersedes_id, active_attempt_id, version, created_at, resolved_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, 'open', ?, NULL, 1, ?, NULL)`,
			successorID, target.ID, predecessor.IssueID, command.TargetIssueVersion, command.TargetEventID, string(artifactIDsJSON), string(purposesJSON),
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
				Purposes:           append([]string(nil), purposes...),
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
	// staleReviewTargetErr is reported to the caller after the transaction
	// commits, not returned from inside it: the supersede this path performs
	// is the point of the call and must survive, exactly as FinishAttempt's
	// own stale-target handling does (attempts.go).
	var staleReviewTargetErr error
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		staleReviewTargetErr = nil
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
		// A stale request is not claimable (docs/09): supersede it here
		// rather than letting a reviewer spend an attempt discovering the
		// staleness at finish.
		stale, err := reviewTargetStale(ctx, tx, request.IssueID, request.TargetIssueVersion, request.TargetEventID)
		if err != nil {
			return err
		}
		if stale {
			if err := supersedeOpenReviewRequest(ctx, tx, request, command.OccurredAt); err != nil {
				return err
			}
			staleReviewTargetErr = staleReviewTargetError()
			return nil
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
	if staleReviewTargetErr != nil {
		return ports.ReviewMutationResult{}, staleReviewTargetErr
	}
	return result, nil
}

// supersedeOpenReviewRequest resolves an open request as superseded and
// records the matching review event. Used wherever an open request is found
// to have gone stale (claim time, and the claim-time binding in attempts.go).
func supersedeOpenReviewRequest(ctx context.Context, tx Executor, request domain.ReviewRequest, occurredAt time.Time) error {
	resolvedAt := formatStorageTime(occurredAt)
	res, err := tx.ExecContext(ctx, `UPDATE review_requests SET status = ?, active_attempt_id = NULL, resolved_at = ?, version = version + 1
		WHERE id = ? AND status = 'open'`, domain.ReviewRequestStatusSuperseded, resolvedAt, request.ID)
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
	return appendReviewEvent(ctx, tx, request.IssueID, "review_superseded", nil,
		payloadForReviewEvent(request.ID, request.TargetID, nil, nil, nil), resolvedAt)
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

const reviewRequestColumns = `id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, purposes_json, status, supersedes_id, active_attempt_id, version, created_at, resolved_at`

const reviewRequestColumnsQualified = `review_requests.id, review_requests.target_id, review_requests.issue_id, review_requests.target_issue_version, review_requests.target_event_id, review_requests.artifact_ids_json, review_requests.purposes_json, review_requests.status, review_requests.supersedes_id, review_requests.active_attempt_id, review_requests.version, review_requests.created_at, review_requests.resolved_at`

// scanReviewRequestRow scans one row shaped like reviewRequestColumns (or its
// qualified counterpart) into a domain.ReviewRequest.
func scanReviewRequestRow(row scanner) (domain.ReviewRequest, error) {
	var request domain.ReviewRequest
	var artifactIDsJSON, purposesJSON []byte
	var status string
	var supersedesID, activeAttemptID sql.NullString
	var createdAtText string
	var resolvedAtText sql.NullString
	if err := row.Scan(&request.ID, &request.TargetID, &request.IssueID, &request.TargetIssueVersion, &request.TargetEventID,
		&artifactIDsJSON, &purposesJSON, &status, &supersedesID, &activeAttemptID, &request.Version, &createdAtText, &resolvedAtText); err != nil {
		return domain.ReviewRequest{}, err
	}
	artifactIDs, err := unmarshalArtifactIDs(artifactIDsJSON)
	if err != nil {
		return domain.ReviewRequest{}, err
	}
	purposes, err := unmarshalReviewPurposes(purposesJSON)
	if err != nil {
		return domain.ReviewRequest{}, err
	}
	request.ArtifactIDs = artifactIDs
	request.Purposes = purposes
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
	return request, nil
}

const reviewTargetColumns = `id, issue_id, issue_version, latest_event_id, artifact_ids_json, purposes_json, version, created_at`

const reviewTargetColumnsQualified = `review_targets.id, review_targets.issue_id, review_targets.issue_version, review_targets.latest_event_id, review_targets.artifact_ids_json, review_targets.purposes_json, review_targets.version, review_targets.created_at`

func scanReviewTargetRow(row scanner) (reviewTargetRow, error) {
	var target reviewTargetRow
	err := row.Scan(&target.ID, &target.IssueID, &target.IssueVersion, &target.LatestEventID,
		&target.ArtifactIDsJSON, &target.PurposesJSON, &target.Version, &target.CreatedAtText)
	return target, err
}

// ensureTarget returns the review target for (command.IssueID,
// command.TargetIssueVersion), creating it -- together with its immutable
// review_approval gate snapshot (docs/02 §17.6, ISSUE-173) -- the first time
// any request names that issue version. A target is frozen once and reused
// by every later request against the same issue version (the existing-row
// branch below), so its own purposes and snapshot never change after
// creation; callers must instead check their OWN purposes against the
// target's frozen snapshot (done by CreateReviewRequest/ReplaceReviewRequest
// right after this returns), not re-derive requirements here.
func (repository *ReviewRepository) ensureTarget(ctx context.Context, tx Executor, command ports.CreateReviewRequestCommand) (domain.ReviewTarget, error) {
	artifactIDsJSON, err := jsonMarshalArtifacts(command.ArtifactIDs)
	if err != nil {
		return domain.ReviewTarget{}, err
	}
	createdAt := formatStorageTime(command.OccurredAt)

	row, err := scanReviewTargetRow(tx.QueryRowContext(ctx, `SELECT `+reviewTargetColumns+`
        FROM review_targets WHERE issue_id = ? AND issue_version = ?`, command.IssueID, command.TargetIssueVersion))
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
		purposesJSON, err := marshalReviewPurposes(command.Purposes)
		if err != nil {
			return domain.ReviewTarget{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_targets(id, issue_id, issue_version, latest_event_id, artifact_ids_json, purposes_json, version, created_at)
			VALUES (?, ?, ?, ?, ?, ?, 1, ?)`, targetID, command.IssueID, command.TargetIssueVersion, command.TargetEventID,
			string(artifactIDsJSON), string(purposesJSON), createdAt); err != nil {
			existing, existingErr := scanReviewTargetRow(tx.QueryRowContext(ctx, `SELECT `+reviewTargetColumns+`
                FROM review_targets WHERE issue_id = ? AND issue_version = ?`, command.IssueID, command.TargetIssueVersion))
			if existingErr != nil {
				return domain.ReviewTarget{}, existingErr
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
		if err := freezeReviewTargetGateSnapshot(ctx, tx, targetID, command.IssueID, command.TargetIssueVersion, command.OccurredAt); err != nil {
			return domain.ReviewTarget{}, err
		}
		return domain.ReviewTarget{
			ID:            targetID,
			IssueID:       command.IssueID,
			IssueVersion:  command.TargetIssueVersion,
			LatestEventID: command.TargetEventID,
			ArtifactIDs:   append([]string(nil), command.ArtifactIDs...),
			Purposes:      append([]string(nil), command.Purposes...),
			Version:       1,
			CreatedAt:     parseTimestamp(createdAt),
		}, nil
	default:
		return domain.ReviewTarget{}, err
	}
}

// freezeReviewTargetGateSnapshot resolves every currently active
// review_approval requirement matching issueID's type/labels and freezes it
// onto targetID's immutable gate snapshot (docs/02 §17.6), the review-target
// counterpart of the full-requirement-set snapshot claim_work freezes onto a
// work attempt. Unlike that full snapshot, this one is deliberately narrowed
// to review_approval requirements: no other requirement kind ever applies at
// approve_review (docs/02 §17.4), and complete_work_to_done -- the kind's
// other applicable point -- re-evaluates against a live issue-scoped
// approval lookup, not this snapshot.
func freezeReviewTargetGateSnapshot(ctx context.Context, tx Executor, targetID, issueID string, issueVersion int64, now time.Time) error {
	issue, err := loadIssueForMutation(ctx, tx, domain.IssueIdentifier{Kind: domain.IssueIdentifierInternalID, Value: issueID})
	if err != nil {
		return err
	}
	policies, err := loadActiveWorkflowPolicies(ctx, tx)
	if err != nil {
		return err
	}
	matched := domain.MatchWorkflowPolicies(policies, issue.Type, labelNames(issue.Labels))
	sourcePolicies := matchingSourcePolicies(policies, issue.Type, labelNames(issue.Labels))
	requirements := make([]domain.PolicyRequirement, 0, len(matched))
	for _, requirement := range matched {
		if requirement.Kind == domain.RequirementKindReviewApproval {
			requirements = append(requirements, requirement)
		}
	}
	snapshot, err := domain.NewGateSnapshot(requirements, sourcePolicies, issueVersion, now)
	if err != nil {
		return err
	}
	return insertReviewTargetGateSnapshot(ctx, tx, targetID, snapshot)
}

// reviewApprovalPurposesRequired returns the distinct, sorted set of
// purposes a frozen review_approval requirement snapshot requires. Every
// requirement in a review-target snapshot is review_approval-kind by
// construction (freezeReviewTargetGateSnapshot never stores any other
// kind), so this does not re-filter by kind.
func reviewApprovalPurposesRequired(requirements []domain.PolicyRequirement) []string {
	seen := make(map[string]bool, len(requirements))
	var purposes []string
	for _, requirement := range requirements {
		if seen[requirement.Purpose] {
			continue
		}
		seen[requirement.Purpose] = true
		purposes = append(purposes, requirement.Purpose)
	}
	sort.Strings(purposes)
	return purposes
}

// missingReviewPurposes returns the entries of required not present in
// covered, sorted, for a deterministic error detail order.
func missingReviewPurposes(required, covered []string) []string {
	have := make(map[string]bool, len(covered))
	for _, purpose := range covered {
		have[purpose] = true
	}
	var missing []string
	for _, purpose := range required {
		if !have[purpose] {
			missing = append(missing, purpose)
		}
	}
	sort.Strings(missing)
	return missing
}

// requireReviewPurposeCoverage loads targetID's frozen review_approval
// snapshot and rejects with CodeReviewPurposeRequired if purposes omits any
// requirement it lists (docs/02 §17.5's "resolves current approve_review
// requirements and rejects a request that does not include every required
// purpose for its target"). A missing snapshot is treated as no
// requirements, not an error, mirroring evaluateGateAgainstAttemptSnapshot's
// same defensive handling -- every target created through ensureTarget
// always has one, so this only guards against a target that somehow
// predates this code path.
func requireReviewPurposeCoverage(ctx context.Context, tx Executor, targetID string, purposes []string) error {
	snapshot, err := loadGateSnapshot(ctx, tx, "review_target_gate_snapshots", "target_id", targetID)
	if err != nil {
		var domainErr *domain.Error
		if errors.As(err, &domainErr) && domainErr.Code == domain.CodeGateSnapshotNotFound {
			return nil
		}
		return err
	}
	required := reviewApprovalPurposesRequired(snapshot.Requirements)
	missing := missingReviewPurposes(required, purposes)
	if len(missing) == 0 {
		return nil
	}
	details := make([]domain.Detail, len(missing))
	for index, purpose := range missing {
		details[index] = domain.Detail{Field: "purposes", Code: domain.CodeReviewPurposeRequired,
			Message: fmt.Sprintf("purpose %q is required by an active review_approval policy for this target", purpose)}
	}
	return domain.NewError(domain.CodeReviewPurposeRequired, "review request purposes do not cover every required purpose", false, details...)
}

func (repository *ReviewRepository) loadActiveRequestForTarget(ctx context.Context, queryer Queryer, targetID string) (*domain.ReviewRequest, error) {
	request, err := scanReviewRequestRow(queryer.QueryRowContext(ctx, `SELECT `+reviewRequestColumns+`
        FROM review_requests WHERE target_id = ? AND status IN ('open','claimed') ORDER BY created_at DESC LIMIT 1`, targetID))
	if err != nil {
		if isNoRowsError(err) {
			return nil, nil
		}
		return nil, err
	}
	return &request, nil
}

func (repository *ReviewRepository) loadRequestForMutation(ctx context.Context, queryer Queryer, requestID string) (domain.ReviewRequest, domain.ReviewTarget, error) {
	row := queryer.QueryRowContext(ctx, `SELECT `+reviewRequestColumnsQualified+`,
        `+reviewTargetColumnsQualified+`
        FROM review_requests
        LEFT JOIN review_targets ON review_targets.id = review_requests.target_id
        WHERE review_requests.id = ?`, requestID)
	request, target, err := scanReviewRequestAndTargetRow(row)
	if err != nil {
		if isNoRowsError(err) {
			return domain.ReviewRequest{}, domain.ReviewTarget{}, domain.NewError(domain.CodeIssueNotFound, "review request not found", false)
		}
		return domain.ReviewRequest{}, domain.ReviewTarget{}, err
	}
	return request, target, nil
}

// scanReviewRequestAndTargetRow scans one row shaped like
// reviewRequestColumnsQualified followed by reviewTargetColumnsQualified --
// the LEFT JOIN loadRequestForMutation runs to load a request with its
// target in one round trip.
func scanReviewRequestAndTargetRow(row scanner) (domain.ReviewRequest, domain.ReviewTarget, error) {
	var request domain.ReviewRequest
	var target reviewTargetRow
	var artifactIDsJSON, purposesJSON []byte
	var status string
	var supersedesID, activeAttemptID sql.NullString
	var createdAtText string
	var resolvedAtText sql.NullString
	if err := row.Scan(&request.ID, &request.TargetID, &request.IssueID, &request.TargetIssueVersion, &request.TargetEventID,
		&artifactIDsJSON, &purposesJSON, &status, &supersedesID, &activeAttemptID, &request.Version, &createdAtText, &resolvedAtText,
		&target.ID, &target.IssueID, &target.IssueVersion, &target.LatestEventID, &target.ArtifactIDsJSON, &target.PurposesJSON, &target.Version, &target.CreatedAtText,
	); err != nil {
		return domain.ReviewRequest{}, domain.ReviewTarget{}, err
	}
	artifactIDs, err := unmarshalArtifactIDs(artifactIDsJSON)
	if err != nil {
		return domain.ReviewRequest{}, domain.ReviewTarget{}, err
	}
	purposes, err := unmarshalReviewPurposes(purposesJSON)
	if err != nil {
		return domain.ReviewRequest{}, domain.ReviewTarget{}, err
	}
	request.ArtifactIDs = artifactIDs
	request.Purposes = purposes
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
	return request, reviewTargetFromRow(target), nil
}

// newReviewApprovalID mints a fresh ULID for one review_approvals row. Every
// other write command's IDs are generated at the application layer (the
// implementation baseline decision: "generate ULIDs from injected time and
// cryptographic entropy" -- describing the mechanism, not literally
// forbidding this), but the number of approvals one approve_review
// resolution writes -- one per request.Purposes entry -- is discovered only
// after loading, mid-transaction, the review request bound to the resolving
// attempt (internal/adapters/sqlite/attempts.go's
// loadActiveReviewRequestForAttempt). Unlike finish_attempt's artifact IDs,
// whose count the caller's own input already fixes before the transaction
// starts, no pre-transaction read can supply this count without adding a
// speculative extra round trip and a TOCTOU window against the very request
// this resolution is about to mutate. now is the same injected transaction
// timestamp used everywhere else; only the entropy source (crypto/rand,
// matching every other ID in this codebase) is sourced locally.
func newReviewApprovalID(now time.Time) (string, error) {
	id, err := ulid.New(ulid.Timestamp(now), rand.Reader)
	if err != nil {
		return "", domain.WrapError(err, domain.CodeIDGeneration, "cannot generate review approval identifier", false)
	}
	return id.String(), nil
}

// insertReviewApprovals writes one immutable review_approvals row per
// purpose in request.Purposes, granted by attemptID's approval of request
// (docs/02 §17.5, ISSUE-173). Called only for the approved outcome, in the
// same transaction that resolves the request -- an approval is durable proof
// tied to the exact target version/event position the request was bound to,
// never re-derived later.
func insertReviewApprovals(ctx context.Context, tx Executor, request domain.ReviewRequest, attemptID string, now time.Time) error {
	timestamp := formatStorageTime(now)
	for _, purpose := range request.Purposes {
		id, err := newReviewApprovalID(now)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_approvals(
			id, issue_id, target_id, request_id, attempt_id, purpose, target_issue_version, target_event_id, version, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
			id, request.IssueID, request.TargetID, request.ID, attemptID, purpose, request.TargetIssueVersion, request.TargetEventID, timestamp,
		); err != nil {
			return err
		}
	}
	return nil
}

// loadIssueReviewApprovalPurposes returns the set of purposes issueID holds
// at least one *still-fresh* approval for. An approval contributes its
// purpose only while nothing disqualifying has happened to the issue after
// the target_event_id it was granted against -- the same
// reviewedWorkChangedSince predicate approve_review applies to its own
// frozen target (docs/02 §17.5). Used to populate
// GateEvidence.ReviewApprovalPurposes at complete_work_to_done, and by the
// work-context summary and the evaluate_gates diagnostic, so all three
// report the same freshness.
//
// ISSUE-223: the lookup used to be plain existence. That made the
// reviewer-free path laxer than the reviewer-involving one, which is
// backwards for a gate whose whole point is that a human looked: an issue
// approved for `security`, reopened (done -> ready is an ordinary
// transition), then modified and completed straight to done with no new
// review, satisfied the gate with an approval granted for code nobody had
// reviewed. "Ever signed off" is a legitimately weaker check, but it must
// not be the invisible default meaning of review_approval.
//
// Freshness is monotone in target_event_id: if nothing disqualifying
// happened after position E, nothing happened after any later position
// either. So the distinct positions are probed in ascending order and the
// first fresh one makes every later position fresh too, bounding the probes
// to the number of distinct approval positions -- in practice one.
func loadIssueReviewApprovalPurposes(ctx context.Context, tx Executor, issueID string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT target_event_id, purpose FROM review_approvals
		WHERE issue_id = ? ORDER BY target_event_id ASC`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var positions []int64
	purposesAt := make(map[int64][]string)
	for rows.Next() {
		var targetEventID int64
		var purpose string
		if err := rows.Scan(&targetEventID, &purpose); err != nil {
			return nil, err
		}
		if _, seen := purposesAt[targetEventID]; !seen {
			positions = append(positions, targetEventID)
		}
		purposesAt[targetEventID] = append(purposesAt[targetEventID], purpose)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	purposes := make(map[string]bool)
	fresh := false
	for _, position := range positions {
		if !fresh {
			changed, err := reviewedWorkChangedSince(ctx, tx, issueID, position)
			if err != nil {
				return nil, err
			}
			if changed {
				continue
			}
			fresh = true
		}
		for _, purpose := range purposesAt[position] {
			purposes[purpose] = true
		}
	}
	return purposes, nil
}

// reviewPurposeSet converts a purposes list to the set shape
// domain.GateEvidence.ReviewApprovalPurposes expects.
func reviewPurposeSet(purposes []string) map[string]bool {
	set := make(map[string]bool, len(purposes))
	for _, purpose := range purposes {
		set[purpose] = true
	}
	return set
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

// samePurposes compares two already-normalized (ValidateReviewPurposes)
// purposes lists. Both sides are canonically sorted before storage, so
// order-sensitive equality is correct here, matching sameArtifactIDs.
func samePurposes(left, right []string) bool {
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
	PurposesJSON    []byte
	Version         int64
	CreatedAtText   string
}

func reviewTargetFromRow(row reviewTargetRow) domain.ReviewTarget {
	artifactIDs, _ := unmarshalArtifactIDs(row.ArtifactIDsJSON)
	purposes, _ := unmarshalReviewPurposes(row.PurposesJSON)
	return domain.ReviewTarget{
		ID:            row.ID,
		IssueID:       row.IssueID,
		IssueVersion:  row.IssueVersion,
		LatestEventID: row.LatestEventID,
		ArtifactIDs:   artifactIDs,
		Purposes:      purposes,
		Version:       row.Version,
		CreatedAt:     parseTimestamp(row.CreatedAtText),
	}
}

// unmarshalReviewPurposes decodes one purposes_json column. A nil/empty
// payload (only possible for a pre-migration-012 row read through some path
// that does not select the column) decodes to nil, matching
// unmarshalArtifactIDs' own leniency.
func unmarshalReviewPurposes(data []byte) ([]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var purposes []string
	if err := json.Unmarshal(data, &purposes); err != nil {
		return nil, domain.WrapError(err, domain.CodeStorageCorrupt, "stored review purposes are invalid", false)
	}
	return purposes, nil
}

func marshalReviewPurposes(purposes []string) ([]byte, error) {
	encoded, err := json.Marshal(purposes)
	if err != nil {
		return nil, domain.WrapError(err, domain.CodeStorageFailure, "cannot encode review purposes", false)
	}
	return encoded, nil
}
