package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

const (
	acquireReservationsOperation = "acquire_reservations"

	reservationColumns = `id, issue_id, attempt_id, kind, display_value, comparison_value, normalized_json,
		status, version, created_at, released_at, release_reason`
)

// ReservationRepository is the SQLite implementation of
// ports.ReservationRepository.
type ReservationRepository struct {
	db         *DB
	transactor *Transactor
}

// NewReservationRepository returns a reservation repository backed by database.
func NewReservationRepository(database *DB) (*ReservationRepository, error) {
	if database == nil {
		return nil, domain.NewError(domain.CodeStorageConfiguration, "reservation database is required", false)
	}
	transactor, err := NewTransactor(database)
	if err != nil {
		return nil, err
	}
	return &ReservationRepository{db: database, transactor: transactor}, nil
}

// reservationIdentityJSON is the minimal, lossless payload persisted in
// resource_reservations.normalized_json and later replayed through
// domain.Normalize to reconstruct an equivalent domain.NormalizedResource,
// without re-exposing that type's private fields. For path kinds (file,
// directory, glob), Path alone round-trips losslessly through Normalize
// because Display() is already redundant-segment-cleaned (see
// FuzzNormalizePathIdempotent). For logical, Namespace/Name are stored
// separately because Display()'s "namespace:name" form cannot be split back
// unambiguously -- Name may itself contain ':'.
type reservationIdentityJSON struct {
	Path      string `json:"path,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
}

// reservationRow is the fully validated, in-memory form of one
// resource_reservations row.
type reservationRow struct {
	id              string
	issueID         string
	attemptID       string
	kind            domain.ResourceKind
	displayValue    string
	comparisonValue string
	normalized      domain.NormalizedResource
	status          domain.ReservationStatus
	version         int64
	createdAt       time.Time
	releasedAt      *time.Time
	releaseReason   *domain.ReservationReleaseReason
}

func (row reservationRow) toDomain() domain.Reservation {
	return domain.Reservation{
		ID: row.id, IssueID: row.issueID, AttemptID: row.attemptID, Kind: row.kind,
		DisplayValue: row.displayValue, ComparisonValue: row.comparisonValue,
		Status: row.status, Version: row.version, CreatedAt: row.createdAt,
		ReleasedAt: row.releasedAt, ReleaseReason: row.releaseReason,
	}
}

// LookupAcquireReservations serves a replay before a new acquisition
// transaction starts. AcquireReservations still repeats this check in its
// writer transaction to close the lookup/write race.
func (repository *ReservationRepository) LookupAcquireReservations(ctx context.Context, key string, hash []byte) ([]domain.Reservation, bool, error) {
	var result []domain.Reservation
	var found bool
	err := repository.db.Read(ctx, func(ctx context.Context, query Queryer) error {
		var savedHash []byte
		var savedResponse string
		err := query.QueryRowContext(ctx, `SELECT request_hash, response_json FROM idempotency_records
			WHERE operation = ? AND idempotency_key = ?`, acquireReservationsOperation, key).Scan(&savedHash, &savedResponse)
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

// AcquireReservations normalizes and validates command's resources, then --
// inside one BEGIN IMMEDIATE write transaction -- checks every candidate
// against every currently active reservation and, only if none overlap,
// inserts all of them and appends one issue event per reservation. All work
// happens in one transaction, so acquisition is all-or-nothing even under
// concurrent callers.
func (repository *ReservationRepository) AcquireReservations(ctx context.Context, command ports.AcquireReservationsCommand) ([]domain.Reservation, error) {
	if _, err := ids.ParseStrict(command.IssueID); err != nil {
		return nil, domain.WrapError(err, domain.CodeInvalidArgument, "issue id is invalid", false)
	}
	if _, err := ids.ParseStrict(command.AttemptID); err != nil {
		return nil, domain.WrapError(err, domain.CodeInvalidArgument, "attempt id is invalid", false)
	}
	if command.OccurredAt.IsZero() {
		return nil, domain.NewError(domain.CodeInvalidArgument, "acquire reservations command is invalid", false)
	}
	if len(command.Resources) == 0 {
		return nil, domain.NewError(domain.CodeInvalidArgument, "resources must not be empty", false)
	}

	rawResources := make([]domain.Resource, len(command.Resources))
	for index, item := range command.Resources {
		if _, err := ids.ParseStrict(item.ID); err != nil {
			return nil, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate reservation identifier", false)
		}
		rawResources[index] = item.Resource
	}
	prepared, err := domain.PrepareReservationRequest(rawResources)
	if err != nil {
		return nil, err
	}

	issueID, attemptID := command.IssueID, command.AttemptID
	now := command.OccurredAt.UTC()
	var reservations []domain.Reservation
	err = repository.transactor.RunWrite(ctx, func(ctx context.Context, uow ports.UnitOfWork) error {
		if command.IdempotencyKey != "" {
			requestHash, responseJSON, found, err := uow.LookupIdempotency(ctx, acquireReservationsOperation, command.IdempotencyKey)
			if err != nil {
				return err
			}
			if found {
				if !bytes.Equal(requestHash, command.RequestHash) {
					return domain.NewError(domain.CodeIdempotencyConflict, "idempotency key was used with a different request", false,
						domain.Detail{Field: "idempotency_key", Code: domain.CodeIdempotencyConflict})
				}
				if err := json.Unmarshal([]byte(responseJSON), &reservations); err != nil {
					return domain.WrapError(err, domain.CodeStorageCorrupt, "stored idempotency response is invalid", false)
				}
				return nil
			}
		}

		tx := uow.(unitOfWork).executor()
		active, err := loadActiveReservations(ctx, tx)
		if err != nil {
			return err
		}

		var conflicts []domain.Detail
		for _, candidate := range prepared {
			for _, existing := range active {
				if domain.Overlaps(candidate.Resource, existing.normalized) {
					conflicts = append(conflicts, conflictDetail(ctx, tx, candidate, existing))
					break
				}
			}
		}
		if len(conflicts) > 0 {
			return domain.NewError(domain.CodeResourceReservationConflict, "requested resources conflict with active reservations", false, conflicts...)
		}

		reservations = make([]domain.Reservation, len(prepared))
		for index, candidate := range prepared {
			id := command.Resources[candidate.Index].ID
			identityJSON, err := marshalIdentity(candidate.Resource)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO resource_reservations(
				id, issue_id, attempt_id, kind, display_value, comparison_value, normalized_json,
				status, version, created_at, released_at, release_reason
			) VALUES (?, ?, ?, ?, ?, ?, ?, 'active', 1, ?, NULL, NULL)`,
				id, issueID, attemptID, string(candidate.Resource.Kind()),
				candidate.Resource.Display(), candidate.Resource.Key(), string(identityJSON), formatStorageTime(now),
			); err != nil {
				return err
			}

			eventPayload, err := json.Marshal(struct {
				ReservationID   string `json:"reservation_id"`
				Kind            string `json:"kind"`
				DisplayValue    string `json:"display_value"`
				ComparisonValue string `json:"comparison_value"`
			}{
				ReservationID: id, Kind: string(candidate.Resource.Kind()),
				DisplayValue: candidate.Resource.Display(), ComparisonValue: candidate.Resource.Key(),
			})
			if err != nil {
				return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode reservation event", false)
			}
			if _, err := uow.AppendIssueEvent(ctx, &issueID, "reservation_reserved", command.SessionID, &attemptID, eventPayload, now); err != nil {
				return err
			}

			reservations[index] = domain.Reservation{
				ID: id, IssueID: issueID, AttemptID: attemptID, Kind: candidate.Resource.Kind(),
				DisplayValue: candidate.Resource.Display(), ComparisonValue: candidate.Resource.Key(),
				Status: domain.ReservationStatusActive, Version: 1, CreatedAt: now,
			}
		}

		if command.IdempotencyKey != "" {
			response, err := json.Marshal(reservations)
			if err != nil {
				return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode reservation response", false)
			}
			if err := uow.StoreIdempotency(ctx, acquireReservationsOperation, command.IdempotencyKey, command.RequestHash, response, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	clones := make([]domain.Reservation, len(reservations))
	for index, reservation := range reservations {
		clones[index] = domain.CloneReservation(reservation)
	}
	return clones, nil
}

// conflictDetail builds one deterministic, bounded conflict detail for
// candidate against the active reservation it overlaps: the requested
// resource, the conflicting reservation's id/kind/value, its owning
// issue/attempt (and session label, when present), and its owning lease
// expiry. It never includes a lease token or token hash.
func conflictDetail(ctx context.Context, query Queryer, candidate domain.PreparedResource, existing reservationRow) domain.Detail {
	index := candidate.Index
	message := fmt.Sprintf("requested %s %q conflicts with active reservation %s (%s %q) held by issue=%s attempt=%s",
		candidate.Resource.Kind(), candidate.Resource.Display(), existing.id, existing.kind, existing.displayValue, existing.issueID, existing.attemptID)
	if leaseExpiresAt, agentLabel, err := lookupAttemptConflictInfo(ctx, query, existing.attemptID); err == nil {
		if agentLabel != "" {
			message += fmt.Sprintf(" session=%q", agentLabel)
		}
		message += fmt.Sprintf(", lease expires %s", formatStorageTime(leaseExpiresAt))
	}
	return domain.Detail{
		EntityIndex: &index, Field: fmt.Sprintf("resources.%d", index),
		Code: domain.CodeResourceReservationConflict, Message: message,
	}
}

func lookupAttemptConflictInfo(ctx context.Context, query Queryer, attemptID string) (time.Time, string, error) {
	var leaseExpiresAtText string
	var agentLabel sql.NullString
	err := query.QueryRowContext(ctx, `SELECT lease_expires_at, agent_label FROM work_attempts WHERE id = ?`, attemptID).
		Scan(&leaseExpiresAtText, &agentLabel)
	if err != nil {
		return time.Time{}, "", err
	}
	leaseExpiresAt, err := parseIssueTimestamp("lease_expires_at", leaseExpiresAtText)
	if err != nil {
		return time.Time{}, "", err
	}
	return leaseExpiresAt, agentLabel.String, nil
}

// ReleaseReservation transitions one active reservation to released,
// guarded by ExpectedVersion.
func (repository *ReservationRepository) ReleaseReservation(ctx context.Context, command ports.ReleaseReservationCommand) (domain.Reservation, error) {
	if _, err := ids.ParseStrict(command.ID); err != nil {
		return domain.Reservation{}, domain.WrapError(err, domain.CodeInvalidArgument, "reservation id is invalid", false)
	}
	if !command.Reason.Valid() {
		return domain.Reservation{}, domain.NewError(domain.CodeInvalidArgument, "release reason is invalid", false)
	}
	if command.OccurredAt.IsZero() || command.ExpectedVersion < 1 {
		return domain.Reservation{}, domain.NewError(domain.CodeInvalidArgument, "release reservation command is invalid", false)
	}

	now := command.OccurredAt.UTC()
	var reservation domain.Reservation
	err := repository.transactor.RunWrite(ctx, func(ctx context.Context, uow ports.UnitOfWork) error {
		tx := uow.(unitOfWork).executor()
		row, err := loadReservationByID(ctx, tx, command.ID)
		if err != nil {
			return err
		}
		if row.status != domain.ReservationStatusActive {
			return domain.NewError(domain.CodeReservationNotActive, "reservation is not active", false)
		}
		if row.version != command.ExpectedVersion {
			return domain.NewError(domain.CodeVersionConflict, "reservation version does not match", false)
		}

		result, err := tx.ExecContext(ctx, `UPDATE resource_reservations
			SET status = 'released', released_at = ?, release_reason = ?, version = version + 1
			WHERE id = ? AND version = ?`,
			formatStorageTime(now), string(command.Reason), command.ID, command.ExpectedVersion)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return domain.NewError(domain.CodeVersionConflict, "reservation version does not match", false)
		}

		payload, err := json.Marshal(struct {
			ReservationID string `json:"reservation_id"`
			ReleaseReason string `json:"release_reason"`
		}{ReservationID: command.ID, ReleaseReason: string(command.Reason)})
		if err != nil {
			return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode reservation event", false)
		}
		if _, err := uow.AppendIssueEvent(ctx, &row.issueID, "reservation_released", nil, &row.attemptID, payload, now); err != nil {
			return err
		}

		updated, err := loadReservationByID(ctx, tx, command.ID)
		if err != nil {
			return err
		}
		reservation = updated.toDomain()
		return nil
	})
	if err != nil {
		return domain.Reservation{}, err
	}
	return domain.CloneReservation(reservation), nil
}

// ListActiveReservations returns active reservations ordered by id ascending.
func (repository *ReservationRepository) ListActiveReservations(ctx context.Context, query ports.ListActiveReservationsQuery) ([]domain.Reservation, error) {
	var result []domain.Reservation
	err := repository.db.Read(ctx, func(ctx context.Context, reader Queryer) error {
		sqlQuery := `SELECT ` + reservationColumns + ` FROM resource_reservations WHERE status = 'active'`
		var args []any
		if query.IssueID != "" {
			sqlQuery += ` AND issue_id = ?`
			args = append(args, query.IssueID)
		}
		if query.AttemptID != "" {
			sqlQuery += ` AND attempt_id = ?`
			args = append(args, query.AttemptID)
		}
		sqlQuery += ` ORDER BY id ASC`
		rows, err := reader.QueryContext(ctx, sqlQuery, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			row, err := scanReservationRow(rows)
			if err != nil {
				return err
			}
			result = append(result, row.toDomain())
		}
		return rows.Err()
	})
	return result, err
}

// ListReservationHistory returns released reservations ordered by
// released_at descending, then id descending, bounded by query.Limit.
func (repository *ReservationRepository) ListReservationHistory(ctx context.Context, query ports.ListReservationHistoryQuery) ([]domain.Reservation, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = domain.DefaultReservationHistoryLimit
	}
	if limit > domain.MaxReservationHistoryLimit {
		return nil, domain.NewError(domain.CodeLimitExceeded,
			fmt.Sprintf("limit exceeds the maximum of %d", domain.MaxReservationHistoryLimit), false,
			domain.Detail{Field: "limit", Code: "MAX_ITEMS"})
	}

	var result []domain.Reservation
	err := repository.db.Read(ctx, func(ctx context.Context, reader Queryer) error {
		sqlQuery := `SELECT ` + reservationColumns + ` FROM resource_reservations WHERE status = 'released'`
		var args []any
		if query.IssueID != "" {
			sqlQuery += ` AND issue_id = ?`
			args = append(args, query.IssueID)
		}
		if query.AttemptID != "" {
			sqlQuery += ` AND attempt_id = ?`
			args = append(args, query.AttemptID)
		}
		sqlQuery += ` ORDER BY released_at DESC, id DESC LIMIT ?`
		args = append(args, limit)
		rows, err := reader.QueryContext(ctx, sqlQuery, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			row, err := scanReservationRow(rows)
			if err != nil {
				return err
			}
			result = append(result, row.toDomain())
		}
		return rows.Err()
	})
	return result, err
}

func loadActiveReservations(ctx context.Context, query Queryer) ([]reservationRow, error) {
	rows, err := query.QueryContext(ctx, `SELECT `+reservationColumns+` FROM resource_reservations WHERE status = 'active' ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []reservationRow
	for rows.Next() {
		row, err := scanReservationRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func loadReservationByID(ctx context.Context, query Queryer, id string) (reservationRow, error) {
	row, err := scanReservationRow(query.QueryRowContext(ctx, `SELECT `+reservationColumns+` FROM resource_reservations WHERE id = ?`, id))
	if err != nil {
		if isNoRowsError(err) {
			return reservationRow{}, domain.NewError(domain.CodeReservationNotFound, "reservation not found", false)
		}
		return reservationRow{}, err
	}
	return row, nil
}

// scanReservationRow accepts the package-private scanner interface (see
// activity.go), satisfied by both *sql.Row and *sql.Rows.
func scanReservationRow(row scanner) (reservationRow, error) {
	var (
		id, issueID, attemptID, kindText, displayValue, comparisonValue, normalizedJSON, statusText, createdAtText string
		version                                                                                                    int64
		releasedAtText, releaseReasonText                                                                          sql.NullString
	)
	if err := row.Scan(&id, &issueID, &attemptID, &kindText, &displayValue, &comparisonValue, &normalizedJSON,
		&statusText, &version, &createdAtText, &releasedAtText, &releaseReasonText); err != nil {
		return reservationRow{}, err
	}

	if _, err := ids.ParseStrict(id); err != nil {
		return reservationRow{}, corruptReservationField(err, "id", "INVALID_ULID")
	}
	if _, err := ids.ParseStrict(issueID); err != nil {
		return reservationRow{}, corruptReservationField(err, "issue_id", "INVALID_ULID")
	}
	if _, err := ids.ParseStrict(attemptID); err != nil {
		return reservationRow{}, corruptReservationField(err, "attempt_id", "INVALID_ULID")
	}
	kind := domain.ResourceKind(kindText)
	if !kind.Valid() {
		return reservationRow{}, corruptReservationField(nil, "kind", "INVALID_ENUM")
	}
	status := domain.ReservationStatus(statusText)
	if status != domain.ReservationStatusActive && status != domain.ReservationStatusReleased {
		return reservationRow{}, corruptReservationField(nil, "status", "INVALID_ENUM")
	}
	created, err := parseIssueTimestamp("created_at", createdAtText)
	if err != nil {
		return reservationRow{}, corruptReservation(err)
	}
	releasedAt, err := parseNullableIssueTimestamp("released_at", releasedAtText)
	if err != nil {
		return reservationRow{}, corruptReservation(err)
	}
	var releaseReason *domain.ReservationReleaseReason
	if releaseReasonText.Valid {
		reason := domain.ReservationReleaseReason(releaseReasonText.String)
		if !reason.Valid() {
			return reservationRow{}, corruptReservationField(nil, "release_reason", "INVALID_ENUM")
		}
		releaseReason = &reason
	}
	if (status == domain.ReservationStatusActive) != (releasedAt == nil) {
		return reservationRow{}, corruptReservationField(nil, "released_at", "STATUS_MISMATCH")
	}
	if (status == domain.ReservationStatusActive) != (releaseReason == nil) {
		return reservationRow{}, corruptReservationField(nil, "release_reason", "STATUS_MISMATCH")
	}

	var identity reservationIdentityJSON
	if err := json.Unmarshal([]byte(normalizedJSON), &identity); err != nil {
		return reservationRow{}, corruptReservationField(err, "normalized_json", "INVALID_JSON")
	}
	normalized, err := domain.Normalize(domain.Resource{
		Kind: kind, Path: identity.Path, Namespace: identity.Namespace, Name: identity.Name,
	})
	if err != nil {
		return reservationRow{}, corruptReservationField(err, "normalized_json", "INVALID_NORMALIZED_RESOURCE")
	}
	if normalized.Key() != comparisonValue {
		return reservationRow{}, corruptReservationField(nil, "comparison_value", "MISMATCH")
	}

	return reservationRow{
		id: id, issueID: issueID, attemptID: attemptID, kind: kind,
		displayValue: displayValue, comparisonValue: comparisonValue, normalized: normalized,
		status: status, version: version, createdAt: created, releasedAt: releasedAt, releaseReason: releaseReason,
	}, nil
}

func marshalIdentity(resource domain.NormalizedResource) ([]byte, error) {
	var payload reservationIdentityJSON
	if resource.Kind() == domain.ResourceKindLogical {
		payload = reservationIdentityJSON{Namespace: resource.Namespace(), Name: resource.Name()}
	} else {
		payload = reservationIdentityJSON{Path: resource.Display()}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, domain.WrapError(err, domain.CodeStorageFailure, "cannot encode reservation identity", false)
	}
	return encoded, nil
}

func corruptReservation(cause error) error {
	return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored reservation is invalid", false)
}

func corruptReservationField(cause error, field, code string) error {
	return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored reservation is invalid", false,
		domain.Detail{Field: field, Code: code})
}

var _ ports.ReservationRepository = (*ReservationRepository)(nil)
