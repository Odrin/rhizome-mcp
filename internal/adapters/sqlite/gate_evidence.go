package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

const submitGateEvidenceOperation = "submit_gate_evidence"

// LookupSubmitGateEvidence serves a replay before a fresh evidence ID is
// allocated. SubmitGateEvidence still repeats this check in its writer
// transaction to close the lookup/write race, matching every other
// idempotent mutation in this package.
func (repository *AttemptRepository) LookupSubmitGateEvidence(ctx context.Context, key string, hash []byte) (ports.SubmitGateEvidenceResult, bool, error) {
	var result ports.SubmitGateEvidenceResult
	var found bool
	err := repository.db.Read(ctx, func(ctx context.Context, query Queryer) error {
		var savedHash []byte
		var savedResponse string
		err := query.QueryRowContext(ctx, `SELECT request_hash, response_json FROM idempotency_records
			WHERE operation = ? AND idempotency_key = ?`, submitGateEvidenceOperation, key).Scan(&savedHash, &savedResponse)
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

// SubmitGateEvidence authenticates the caller's lease against the owning
// work attempt (must be active, kind=work -- review attempts cannot submit
// evidence, docs/02 §17), validates the key against the attempt's frozen
// gate snapshot and its artifact references against the same issue and
// attempt, then upserts one versioned, audited record.
func (repository *AttemptRepository) SubmitGateEvidence(ctx context.Context, command ports.SubmitGateEvidenceCommand) (ports.SubmitGateEvidenceResult, error) {
	if _, err := ids.ParseStrict(command.EvidenceID); err != nil {
		return ports.SubmitGateEvidenceResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate evidence identifier", false)
	}
	if _, err := ids.ParseStrict(command.AttemptID); err != nil || len(command.TokenHash) != 32 {
		return ports.SubmitGateEvidenceResult{}, domain.NewError(domain.CodeInvalidArgument, "gate evidence command is invalid", false)
	}
	now := command.OccurredAt.UTC()
	timestamp := formatStorageTime(now)
	var result ports.SubmitGateEvidenceResult
	var leaseExpired bool
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		if command.IdempotencyKey != "" {
			var savedHash []byte
			var savedResponse string
			err := tx.QueryRowContext(ctx, `SELECT request_hash, response_json FROM idempotency_records
				WHERE operation = ? AND idempotency_key = ?`, submitGateEvidenceOperation, command.IdempotencyKey).Scan(&savedHash, &savedResponse)
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

		workKind := domain.AttemptKindWork
		issueID, _, expired, err := authenticateActiveAttempt(ctx, tx, command.AttemptID, command.TokenHash, now, &workKind)
		if err != nil {
			return err
		}
		if expired {
			leaseExpired = true
			return nil
		}

		if err := validateGateEvidenceKey(ctx, tx, command.AttemptID, command.Key, command.Result); err != nil {
			return err
		}
		if err := validateGateEvidenceArtifacts(ctx, tx, issueID, command.AttemptID, command.ArtifactIDs); err != nil {
			return err
		}

		artifactIDsJSON, err := jsonMarshalArtifacts(command.ArtifactIDs)
		if err != nil {
			return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode evidence artifact IDs", false)
		}

		var existingID string
		var existingVersion int64
		lookupErr := tx.QueryRowContext(ctx, `SELECT id, version FROM gate_evidence WHERE attempt_id = ? AND key = ?`,
			command.AttemptID, command.Key).Scan(&existingID, &existingVersion)
		var evidenceID string
		var version int64
		var eventType string
		switch lookupErr {
		case sql.ErrNoRows:
			evidenceID, version, eventType = command.EvidenceID, 1, "evidence_submitted"
			if _, err := tx.ExecContext(ctx, `INSERT INTO gate_evidence(
				id, attempt_id, issue_id, key, result, summary, details, artifact_ids_json, version, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
				evidenceID, command.AttemptID, issueID, command.Key, string(command.Result), command.Summary,
				nullableString(nonEmptyPointer(command.Details)), string(artifactIDsJSON), timestamp, timestamp); err != nil {
				return err
			}
		case nil:
			evidenceID, version, eventType = existingID, existingVersion+1, "evidence_replaced"
			updateResult, err := tx.ExecContext(ctx, `UPDATE gate_evidence
				SET result = ?, summary = ?, details = ?, artifact_ids_json = ?, version = ?, updated_at = ?
				WHERE id = ? AND version = ?`,
				string(command.Result), command.Summary, nullableString(nonEmptyPointer(command.Details)),
				string(artifactIDsJSON), version, timestamp, evidenceID, existingVersion)
			if err != nil {
				return err
			}
			if err := requireSingleRowAffected(updateResult); err != nil {
				return err
			}
		default:
			return lookupErr
		}

		evidence, err := loadAttemptEvidenceByID(ctx, tx, evidenceID)
		if err != nil {
			return err
		}
		if err := appendGateEvidenceEvent(ctx, tx, evidence, eventType, timestamp); err != nil {
			return err
		}
		result.Evidence = evidence
		if command.IdempotencyKey != "" {
			response, err := json.Marshal(result)
			if err != nil {
				return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode evidence idempotency response", false)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO idempotency_records(
				idempotency_key, operation, request_hash, response_json, created_at
			) VALUES (?, ?, ?, ?, ?)`, command.IdempotencyKey, submitGateEvidenceOperation, command.RequestHash, string(response), timestamp); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ports.SubmitGateEvidenceResult{}, err
	}
	if leaseExpired {
		return ports.SubmitGateEvidenceResult{}, domain.NewError(domain.CodeLeaseExpired, "attempt lease has expired", false)
	}
	result.Evidence = domain.CloneAttemptEvidence(result.Evidence)
	return result, nil
}

// validateGateEvidenceKey enforces that Key names an attempt_evidence
// requirement in the attempt's frozen gate snapshot, and that a
// result=not_applicable submission is accepted only when every matching
// requirement (by evidence_key -- more than one policy can independently
// require the same evidence_key, docs/02 §17.3) allows it.
func validateGateEvidenceKey(ctx context.Context, tx Executor, attemptID, key string, result domain.EvidenceResult) error {
	snapshot, err := loadGateSnapshot(ctx, tx, "attempt_gate_snapshots", "attempt_id", attemptID)
	if err != nil {
		if domainErr, ok := err.(*domain.Error); ok && domainErr.Code == domain.CodeGateSnapshotNotFound {
			// Compatibility (docs/02 §17.10): an attempt with no snapshot is
			// treated as an empty requirement set, never as an error -- but
			// an empty set also means no evidence_key is ever valid.
			return domain.NewError(domain.CodeInvalidArgument, "evidence key does not match any requirement in the attempt's gate snapshot", false,
				domain.Detail{Field: "key", Code: "UNKNOWN_KEY"})
		}
		return err
	}
	matched := false
	allowNotApplicable := true
	for _, requirement := range snapshot.Requirements {
		if requirement.Kind != domain.RequirementKindAttemptEvidence || requirement.EvidenceKey != key {
			continue
		}
		matched = true
		if !requirement.AllowNotApplicable {
			allowNotApplicable = false
		}
	}
	if !matched {
		return domain.NewError(domain.CodeInvalidArgument, "evidence key does not match any requirement in the attempt's gate snapshot", false,
			domain.Detail{Field: "key", Code: "UNKNOWN_KEY"})
	}
	if result == domain.EvidenceResultNotApplicable && !allowNotApplicable {
		return domain.NewError(domain.CodeInvalidArgument, "result=not_applicable is not allowed for this evidence key", false,
			domain.Detail{Field: "result", Code: "NOT_APPLICABLE_FORBIDDEN"})
	}
	return nil
}

// validateGateEvidenceArtifacts enforces that every referenced artifact
// already belongs to the same issue and the same attempt (docs/02 §17):
// evidence never creates artifacts and never accepts a reference to one
// scoped elsewhere.
func validateGateEvidenceArtifacts(ctx context.Context, tx Executor, issueID, attemptID string, artifactIDs []string) error {
	for index, artifactID := range artifactIDs {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM artifacts WHERE id = ? AND issue_id = ? AND attempt_id = ?
		)`, artifactID, issueID, attemptID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return domain.NewError(domain.CodeInvalidArgument, "artifact does not belong to this issue and attempt", false,
				domain.Detail{EntityIndex: &index, Field: "artifact_ids", Code: "NOT_FOUND"})
		}
	}
	return nil
}

func loadAttemptEvidenceByID(ctx context.Context, query Queryer, id string) (domain.AttemptEvidence, error) {
	row := query.QueryRowContext(ctx, `SELECT id, attempt_id, issue_id, key, result, summary, details, artifact_ids_json, version, created_at, updated_at
		FROM gate_evidence WHERE id = ?`, id)
	return scanAttemptEvidence(row)
}

func scanAttemptEvidence(scanner scanner) (domain.AttemptEvidence, error) {
	var id, attemptID, issueID, key, result, summary, createdAt, updatedAt string
	var details sql.NullString
	var artifactIDsJSON string
	var version int64
	if err := scanner.Scan(&id, &attemptID, &issueID, &key, &result, &summary, &details, &artifactIDsJSON, &version, &createdAt, &updatedAt); err != nil {
		return domain.AttemptEvidence{}, err
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return domain.AttemptEvidence{}, gateEvidenceCorruptField(err, "id", "INVALID_ULID")
	}
	evidenceResult := domain.EvidenceResult(result)
	if !evidenceResult.Valid() {
		return domain.AttemptEvidence{}, gateEvidenceCorruptField(nil, "result", "INVALID_ENUM")
	}
	if version < 1 {
		return domain.AttemptEvidence{}, gateEvidenceCorruptField(nil, "version", "INVALID_VALUE")
	}
	artifactIDs, err := unmarshalArtifactIDs([]byte(artifactIDsJSON))
	if err != nil {
		return domain.AttemptEvidence{}, gateEvidenceCorruptField(err, "artifact_ids_json", "INVALID_JSON")
	}
	created, err := parseIssueTimestamp("created_at", createdAt)
	if err != nil {
		return domain.AttemptEvidence{}, err
	}
	updated, err := parseIssueTimestamp("updated_at", updatedAt)
	if err != nil {
		return domain.AttemptEvidence{}, err
	}
	return domain.AttemptEvidence{
		ID: id, AttemptID: attemptID, IssueID: issueID, Key: key, Result: evidenceResult,
		Summary: summary, Details: details.String, ArtifactIDs: artifactIDs, Version: version,
		CreatedAt: created, UpdatedAt: updated,
	}, nil
}

// ListAttemptEvidence returns every current evidence record for one attempt,
// ordered by key, for atomic gate evaluation (ISSUE-172) and issue activity.
func (repository *AttemptRepository) ListAttemptEvidence(ctx context.Context, command ports.ListAttemptEvidenceCommand) ([]domain.AttemptEvidence, error) {
	var records []domain.AttemptEvidence
	err := repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		rows, err := query.QueryContext(ctx, `SELECT id, attempt_id, issue_id, key, result, summary, details, artifact_ids_json, version, created_at, updated_at
			FROM gate_evidence WHERE attempt_id = ? ORDER BY key`, command.AttemptID)
		if err != nil {
			return err
		}
		defer rows.Close()
		records = make([]domain.AttemptEvidence, 0)
		for rows.Next() {
			item, err := scanAttemptEvidence(rows)
			if err != nil {
				return err
			}
			records = append(records, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	cloned := make([]domain.AttemptEvidence, len(records))
	for index, item := range records {
		cloned[index] = domain.CloneAttemptEvidence(item)
	}
	return cloned, nil
}

type gateEvidenceEventPayload struct {
	EvidenceID  string   `json:"evidence_id"`
	AttemptID   string   `json:"attempt_id"`
	Key         string   `json:"key"`
	Result      string   `json:"result"`
	Version     int64    `json:"version"`
	ArtifactIDs []string `json:"artifact_ids"`
}

func appendGateEvidenceEvent(ctx context.Context, tx Executor, evidence domain.AttemptEvidence, eventType string, timestamp string) error {
	payload, err := json.Marshal(gateEvidenceEventPayload{
		EvidenceID: evidence.ID, AttemptID: evidence.AttemptID, Key: evidence.Key,
		Result: string(evidence.Result), Version: evidence.Version, ArtifactIDs: evidence.ArtifactIDs,
	})
	if err != nil {
		return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode gate evidence event", false)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO gate_evidence_events(
		evidence_id, attempt_id, issue_id, key, event_type, version, payload, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		evidence.ID, evidence.AttemptID, evidence.IssueID, evidence.Key, eventType, evidence.Version, string(payload), timestamp)
	return err
}

func nonEmptyPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func gateEvidenceCorruptField(cause error, field, code string) error {
	return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored gate evidence projection is invalid", false,
		domain.Detail{Field: field, Code: code})
}
