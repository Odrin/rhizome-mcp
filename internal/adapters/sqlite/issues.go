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

// IssueRepository is the SQLite implementation of ports.IssueRepository.
type IssueRepository struct {
	db                                 *DB
	afterGetIssueProjectionReadForTest func()
}

const (
	createIssueOperation  = "create_issue"
	updateIssueOperation  = "update_issue"
	archiveIssueOperation = "archive_issue"
)

// NewIssueRepository returns an issue repository backed by database.
func NewIssueRepository(database *DB) (*IssueRepository, error) {
	if database == nil {
		return nil, domain.NewError(domain.CodeStorageConfiguration, "issue database is required", false)
	}
	return &IssueRepository{db: database}, nil
}

// LookupCreateIssue serves a replay before IDs are allocated. Create still
// repeats this check in its writer transaction to close the lookup/write race.
func (repository *IssueRepository) LookupCreateIssue(ctx context.Context, key string, hash []byte) (domain.Issue, bool, error) {
	var result domain.Issue
	var found bool
	err := repository.db.Read(ctx, func(ctx context.Context, query Queryer) error {
		var savedHash []byte
		var savedResponse string
		err := query.QueryRowContext(ctx, `SELECT request_hash, response_json FROM idempotency_records
			WHERE operation = ? AND idempotency_key = ?`, createIssueOperation, key).Scan(&savedHash, &savedResponse)
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

// CreateIssue atomically allocates a project-local sequence number, inserts the
// issue projection, and appends its creation event.
func (repository *IssueRepository) CreateIssue(ctx context.Context, command ports.CreateIssueCommand) (domain.Issue, error) {
	input, err := command.Input.Validate()
	if err != nil {
		return domain.Issue{}, err
	}
	if _, err := ids.ParseStrict(command.ID); err != nil {
		return domain.Issue{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate issue identifier", false)
	}

	now := command.CreatedAt.UTC()
	timestamp := now.Format(time.RFC3339Nano)
	var issue domain.Issue
	err = repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		if command.IdempotencyKey != "" {
			var savedHash []byte
			var savedResponse string
			err := tx.QueryRowContext(ctx, `SELECT request_hash, response_json FROM idempotency_records
				WHERE operation = ? AND idempotency_key = ?`, createIssueOperation, command.IdempotencyKey).Scan(&savedHash, &savedResponse)
			switch {
			case err == nil:
				if !bytes.Equal(savedHash, command.RequestHash) {
					return domain.NewError(domain.CodeIdempotencyConflict, "idempotency key was used with a different request", false,
						domain.Detail{Field: "idempotency_key", Code: domain.CodeIdempotencyConflict})
				}
				if err := json.Unmarshal([]byte(savedResponse), &issue); err != nil {
					return domain.WrapError(err, domain.CodeStorageCorrupt, "stored idempotency response is invalid", false)
				}
				return nil
			case err == sql.ErrNoRows:
			default:
				return err
			}
		}
		var sequenceNo int64
		err := tx.QueryRowContext(ctx, `
			UPDATE projects
			SET next_issue_number = next_issue_number + 1, updated_at = ?
			RETURNING next_issue_number - 1
		`, timestamp).Scan(&sequenceNo)
		if err != nil {
			if err == sql.ErrNoRows {
				return domain.NewError(domain.CodeProjectNotInitialized, "project database is not initialized", false)
			}
			return err
		}
		resolvedParentID, err := validateParent(ctx, tx, input.ParentID)
		if err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, `INSERT INTO issues(
			id, sequence_no, type, title, description, acceptance_criteria,
			status, priority, parent_id, blocked_reason, version,
			created_by_session_id, created_at, updated_at, closed_at,
			archived_at, archived_by_session_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, NULL, ?, ?, NULL, NULL, NULL)`,
			command.ID, sequenceNo, input.Type, input.Title, nullableString(input.Description),
			nullableString(input.AcceptanceCriteria), input.Status, input.Priority,
			nullableString(resolvedParentID), nullableString(input.BlockedReason), timestamp, timestamp,
		); err != nil {
			return err
		}
		labels, err := resolveIssueLabels(ctx, tx, input.Labels, input.CreateMissingLabels, command.LabelIDs, now)
		if err != nil {
			return err
		}
		if err := replaceIssueLabels(ctx, tx, command.ID, labels); err != nil {
			return err
		}

		payload, err := json.Marshal(issueCreatedPayload{
			SequenceNo: sequenceNo,
			Type:       input.Type,
			Status:     input.Status,
			Priority:   input.Priority,
			ParentID:   resolvedParentID,
			Labels:     labelNames(labels),
		})
		if err != nil {
			return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode issue creation event", false)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO issue_events(issue_id, event_type, session_id, attempt_id, payload, created_at)
			VALUES (?, 'issue_created', NULL, NULL, ?, ?)
		`, command.ID, string(payload), timestamp); err != nil {
			return err
		}

		issue = domain.Issue{
			ID:                 command.ID,
			DisplayID:          fmt.Sprintf("ISSUE-%d", sequenceNo),
			SequenceNo:         sequenceNo,
			Type:               input.Type,
			Title:              input.Title,
			Description:        input.Description,
			AcceptanceCriteria: input.AcceptanceCriteria,
			Status:             input.Status,
			Priority:           input.Priority,
			ParentID:           resolvedParentID,
			BlockedReason:      input.BlockedReason,
			Version:            1,
			CreatedAt:          now,
			UpdatedAt:          now,
			Labels:             labels,
		}
		if command.IdempotencyKey != "" {
			response, err := json.Marshal(issue)
			if err != nil {
				return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode issue create response", false)
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO idempotency_records(
				idempotency_key, operation, request_hash, response_json, created_at
			) VALUES (?, ?, ?, ?, ?)`, command.IdempotencyKey, createIssueOperation, command.RequestHash, string(response), timestamp)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domain.Issue{}, err
	}
	return issue, nil
}

// GetIssue reads one issue projection by internal ID or project-local sequence
// number. It performs no writes and does not hide archived issues.
func (repository *IssueRepository) GetIssue(ctx context.Context, identifier domain.IssueIdentifier) (domain.Issue, error) {
	var issue domain.Issue
	err := repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		var row *sql.Row
		switch identifier.Kind {
		case domain.IssueIdentifierInternalID:
			row = query.QueryRowContext(ctx, issueProjectionSelect+" WHERE id = ?", identifier.Value)
		case domain.IssueIdentifierDisplayID:
			row = query.QueryRowContext(ctx, issueProjectionSelect+" WHERE sequence_no = ?", identifier.SequenceNo)
		default:
			return domain.NewError(
				domain.CodeInvalidArgument,
				"issue identifier is invalid",
				false,
				domain.Detail{Field: "issue_id", Code: "INVALID_IDENTIFIER"},
			)
		}

		parsed, err := scanIssueProjection(row)
		if err != nil {
			if err == sql.ErrNoRows {
				return domain.NewError(domain.CodeIssueNotFound, "issue not found", false)
			}
			return err
		}
		if repository.afterGetIssueProjectionReadForTest != nil {
			repository.afterGetIssueProjectionReadForTest()
		}
		labels, err := loadIssueLabels(ctx, query, parsed.ID)
		if err != nil {
			return err
		}
		parsed.Labels = labels
		issue = parsed
		return nil
	})
	if err != nil {
		return domain.Issue{}, err
	}
	return issue, nil
}

// LookupUpdateIssue serves a replay before label IDs are allocated. Update
// still repeats this check in its writer transaction to close the lookup/write race.
func (repository *IssueRepository) LookupUpdateIssue(ctx context.Context, key string, hash []byte) (ports.UpdateIssueResult, bool, error) {
	var result ports.UpdateIssueResult
	var found bool
	err := repository.db.Read(ctx, func(ctx context.Context, query Queryer) error {
		var savedHash []byte
		var savedResponse string
		err := query.QueryRowContext(ctx, `SELECT request_hash, response_json FROM idempotency_records
			WHERE operation = ? AND idempotency_key = ?`, updateIssueOperation, key).Scan(&savedHash, &savedResponse)
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

// UpdateIssue atomically validates the current projection, conditionally
// persists an optimistic patch, and appends its one corresponding event.
func (repository *IssueRepository) UpdateIssue(ctx context.Context, command ports.UpdateIssueCommand) (ports.UpdateIssueResult, error) {
	var result ports.UpdateIssueResult
	now := command.UpdatedAt.UTC()
	timestamp := now.Format(time.RFC3339Nano)
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		if command.IdempotencyKey != "" {
			var savedHash []byte
			var savedResponse string
			err := tx.QueryRowContext(ctx, `SELECT request_hash, response_json FROM idempotency_records
				WHERE operation = ? AND idempotency_key = ?`, updateIssueOperation, command.IdempotencyKey).Scan(&savedHash, &savedResponse)
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
		current, err := loadIssueForMutation(ctx, tx, command.Identifier)
		if err != nil {
			return err
		}
		if current.ArchivedAt != nil {
			return domain.NewError(domain.CodeIssueArchived, "issue is archived", false)
		}
		if current.Version != command.ExpectedVersion {
			return domain.NewError(domain.CodeVersionConflict, "issue version conflict", true)
		}
		next, changedFields, err := domain.ApplyIssuePatch(current, command.Changes)
		if err != nil {
			return err
		}
		if command.Changes.ParentID.Set && next.ParentID != nil {
			resolved, err := validateParent(ctx, tx, next.ParentID)
			if err != nil {
				return err
			}
			if *resolved == current.ID {
				return invalidParentError()
			}
			next.ParentID = resolved
		}
		if next.Type == domain.TypeEpic && next.ParentID != nil {
			return invalidParentError()
		}
		if command.Changes.Labels.Set {
			labels, err := resolveIssueLabels(ctx, tx, command.Changes.Labels.Value, command.CreateMissingLabels, command.LabelIDs, now)
			if err != nil {
				return err
			}
			if err := replaceIssueLabels(ctx, tx, current.ID, labels); err != nil {
				return err
			}
			next.Labels = labels
		}
		next.UpdatedAt = now
		next.Version = current.Version + 1
		if command.Changes.Status.Set {
			switch {
			case next.Status.Terminal() && !current.Status.Terminal():
				next.ClosedAt = &now
			case !next.Status.Terminal() && current.Status.Terminal():
				next.ClosedAt = nil
			}
		}

		res, err := tx.ExecContext(ctx, `UPDATE issues SET
			type = ?, title = ?, description = ?, acceptance_criteria = ?,
			status = ?, priority = ?, parent_id = ?, blocked_reason = ?,
			version = ?, updated_at = ?, closed_at = ?
			WHERE id = ? AND version = ? AND archived_at IS NULL`,
			next.Type, next.Title, nullableString(next.Description), nullableString(next.AcceptanceCriteria),
			next.Status, next.Priority, nullableString(next.ParentID), nullableString(next.BlockedReason),
			next.Version, timestamp, nullableTime(next.ClosedAt), current.ID, command.ExpectedVersion,
		)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return classifyConditionalUpdateFailure(ctx, tx, current.ID)
		}
		payload, err := json.Marshal(newIssueUpdatedPayload(next, changedFields))
		if err != nil {
			return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode issue update event", false)
		}
		eventType := "issue_updated"
		if command.Changes.Status.Set {
			eventType = "status_changed"
		} else if command.Changes.Labels.Set {
			eventType = "labels_changed"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO issue_events(issue_id, event_type, session_id, attempt_id, payload, created_at)
			VALUES (?, ?, NULL, NULL, ?, ?)`, current.ID, eventType, string(payload), timestamp); err != nil {
			return err
		}
		result = ports.UpdateIssueResult{Issue: next, ChangedFields: append([]string(nil), changedFields...)}
		if command.IdempotencyKey != "" {
			response, err := json.Marshal(result)
			if err != nil {
				return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode issue update response", false)
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO idempotency_records(
				idempotency_key, operation, request_hash, response_json, created_at
			) VALUES (?, ?, ?, ?, ?)`, command.IdempotencyKey, updateIssueOperation, command.RequestHash, string(response), timestamp)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ports.UpdateIssueResult{}, err
	}
	return result, nil
}

// LookupArchiveIssue serves a replay before the writer transaction begins.
// Archive still repeats this check in its writer transaction to close the
// lookup/write race.
func (repository *IssueRepository) LookupArchiveIssue(ctx context.Context, key string, hash []byte) (ports.ArchiveIssueResult, bool, error) {
	var result ports.ArchiveIssueResult
	var found bool
	err := repository.db.Read(ctx, func(ctx context.Context, query Queryer) error {
		var savedHash []byte
		var savedResponse string
		err := query.QueryRowContext(ctx, `SELECT request_hash, response_json FROM idempotency_records
			WHERE operation = ? AND idempotency_key = ?`, archiveIssueOperation, key).Scan(&savedHash, &savedResponse)
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

// ArchiveIssue atomically protects against active attempts, conditionally
// archives an issue, and appends its one corresponding event.
func (repository *IssueRepository) ArchiveIssue(ctx context.Context, command ports.ArchiveIssueCommand) (ports.ArchiveIssueResult, error) {
	var result ports.ArchiveIssueResult
	now := command.ArchivedAt.UTC()
	timestamp := now.Format(time.RFC3339Nano)
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		if command.IdempotencyKey != "" {
			var savedHash []byte
			var savedResponse string
			err := tx.QueryRowContext(ctx, `SELECT request_hash, response_json FROM idempotency_records
				WHERE operation = ? AND idempotency_key = ?`, archiveIssueOperation, command.IdempotencyKey).Scan(&savedHash, &savedResponse)
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
		current, err := loadIssueForMutation(ctx, tx, command.Identifier)
		if err != nil {
			return err
		}
		if current.ArchivedAt != nil {
			return domain.NewError(domain.CodeIssueArchived, "issue is archived", false)
		}
		if current.Version != command.ExpectedVersion {
			return domain.NewError(domain.CodeVersionConflict, "issue version conflict", true)
		}
		if err := expireAttemptsForIssue(ctx, tx, current.ID, now); err != nil {
			return err
		}
		var hasActiveAttempt bool
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM work_attempts
				WHERE issue_id = ? AND status = 'active'
			)`, current.ID).Scan(&hasActiveAttempt); err != nil {
			return err
		}
		if hasActiveAttempt {
			return domain.NewError(domain.CodeActiveAttemptExists, "issue has an active work attempt", false)
		}

		res, err := tx.ExecContext(ctx, `
			UPDATE issues
			SET archived_at = ?, archived_by_session_id = NULL,
				version = version + 1, updated_at = ?
			WHERE id = ? AND version = ? AND archived_at IS NULL`,
			timestamp, timestamp, current.ID, command.ExpectedVersion,
		)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return classifyConditionalUpdateFailure(ctx, tx, current.ID)
		}

		payload, err := json.Marshal(issueArchivedPayload{
			Version:    command.ExpectedVersion + 1,
			ArchivedAt: timestamp,
		})
		if err != nil {
			return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode issue archive event", false)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO issue_events(issue_id, event_type, session_id, attempt_id, payload, created_at)
			VALUES (?, 'issue_archived', NULL, NULL, ?, ?)`,
			current.ID, string(payload), timestamp); err != nil {
			return err
		}

		result.Issue, err = loadIssueForMutation(ctx, tx, domain.IssueIdentifier{
			Kind:  domain.IssueIdentifierInternalID,
			Value: current.ID,
		})
		if err != nil {
			return err
		}
		if command.IdempotencyKey != "" {
			response, err := json.Marshal(result)
			if err != nil {
				return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode issue archive response", false)
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO idempotency_records(
				idempotency_key, operation, request_hash, response_json, created_at
			) VALUES (?, ?, ?, ?, ?)`, command.IdempotencyKey, archiveIssueOperation, command.RequestHash, string(response), timestamp)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ports.ArchiveIssueResult{}, err
	}
	return result, nil
}

func loadIssueForMutation(ctx context.Context, tx Queryer, identifier domain.IssueIdentifier) (domain.Issue, error) {
	var row *sql.Row
	switch identifier.Kind {
	case domain.IssueIdentifierInternalID:
		row = tx.QueryRowContext(ctx, issueProjectionSelect+" WHERE id = ?", identifier.Value)
	case domain.IssueIdentifierDisplayID:
		row = tx.QueryRowContext(ctx, issueProjectionSelect+" WHERE sequence_no = ?", identifier.SequenceNo)
	default:
		return domain.Issue{}, domain.NewError(domain.CodeInvalidArgument, "issue identifier is invalid", false,
			domain.Detail{Field: "issue_id", Code: "INVALID_IDENTIFIER"})
	}
	issue, err := scanIssueProjection(row)
	if err == sql.ErrNoRows {
		return domain.Issue{}, domain.NewError(domain.CodeIssueNotFound, "issue not found", false)
	}
	if err != nil {
		return issue, err
	}
	labels, err := loadIssueLabels(ctx, tx, issue.ID)
	if err != nil {
		return domain.Issue{}, err
	}
	issue.Labels = labels
	return issue, nil
}

func classifyConditionalUpdateFailure(ctx context.Context, tx Executor, id string) error {
	var archivedAt sql.NullString
	var version int64
	err := tx.QueryRowContext(ctx, "SELECT archived_at, version FROM issues WHERE id = ?", id).Scan(&archivedAt, &version)
	if err == sql.ErrNoRows {
		return domain.NewError(domain.CodeIssueNotFound, "issue not found", false)
	}
	if err != nil {
		return err
	}
	if archivedAt.Valid {
		return domain.NewError(domain.CodeIssueArchived, "issue is archived", false)
	}
	return domain.NewError(domain.CodeVersionConflict, "issue version conflict", true)
}

const issueProjectionSelect = `SELECT id, sequence_no, type, title, description, acceptance_criteria,
	status, priority, parent_id, blocked_reason, version,
	created_by_session_id, created_at, updated_at, closed_at,
	archived_at, archived_by_session_id FROM issues`

func scanIssueProjection(row *sql.Row) (domain.Issue, error) {
	var (
		id, issueType, title, status, priority, createdAt, updatedAt  string
		description, acceptanceCriteria, parentID, blockedReason      sql.NullString
		createdBySessionID, closedAt, archivedAt, archivedBySessionID sql.NullString
		sequenceNo, version                                           int64
	)
	if err := scanIssueProjectionColumns(row, &id, &sequenceNo, &issueType, &title, &description,
		&acceptanceCriteria, &parentID, &blockedReason, &version,
		&createdBySessionID, &closedAt, &archivedAt, &archivedBySessionID,
		&status, &priority, &createdAt, &updatedAt,
	); err != nil {
		if err != sql.ErrNoRows {
			return domain.Issue{}, domain.WrapError(err, domain.CodeStorageCorrupt, "stored issue projection is invalid", false)
		}
		return domain.Issue{}, err
	}
	return parseIssueProjectionColumns(id, sequenceNo, issueType, title, description, acceptanceCriteria,
		parentID, blockedReason, status, priority, version, createdBySessionID, createdAt, updatedAt,
		closedAt, archivedAt, archivedBySessionID)
}

func scanIssueProjectionColumns(scanner labelScanner, id *string, sequenceNo *int64, issueType, title *string,
	description, acceptanceCriteria, parentID, blockedReason *sql.NullString, version *int64,
	createdBySessionID, closedAt, archivedAt, archivedBySessionID *sql.NullString,
	status, priority, createdAt, updatedAt *string,
) error {
	return scanner.Scan(
		id, sequenceNo, issueType, title, description, acceptanceCriteria,
		status, priority, parentID, blockedReason, version,
		createdBySessionID, createdAt, updatedAt, closedAt, archivedAt, archivedBySessionID,
	)
}

func parseIssueProjectionColumns(id string, sequenceNo int64, issueType, title string,
	description, acceptanceCriteria, parentID, blockedReason sql.NullString,
	status, priority string, version int64, createdBySessionID sql.NullString, createdAt, updatedAt string,
	closedAt, archivedAt, archivedBySessionID sql.NullString,
) (domain.Issue, error) {
	parsedType, err := domain.ParseType(issueType)
	if err != nil {
		return domain.Issue{}, corruptIssueProjection(err)
	}
	parsedStatus, err := domain.ParseStatus(status)
	if err != nil {
		return domain.Issue{}, corruptIssueProjection(err)
	}
	parsedPriority, err := domain.ParsePriority(priority)
	if err != nil {
		return domain.Issue{}, corruptIssueProjection(err)
	}
	created, err := parseIssueTimestamp("created_at", createdAt)
	if err != nil {
		return domain.Issue{}, err
	}
	updated, err := parseIssueTimestamp("updated_at", updatedAt)
	if err != nil {
		return domain.Issue{}, err
	}
	closed, err := parseNullableIssueTimestamp("closed_at", closedAt)
	if err != nil {
		return domain.Issue{}, err
	}
	archived, err := parseNullableIssueTimestamp("archived_at", archivedAt)
	if err != nil {
		return domain.Issue{}, err
	}
	return domain.Issue{
		ID:                  id,
		DisplayID:           fmt.Sprintf("ISSUE-%d", sequenceNo),
		SequenceNo:          sequenceNo,
		Type:                parsedType,
		Title:               title,
		Description:         nullableStringPointer(description),
		AcceptanceCriteria:  nullableStringPointer(acceptanceCriteria),
		Status:              parsedStatus,
		Priority:            parsedPriority,
		ParentID:            nullableStringPointer(parentID),
		BlockedReason:       nullableStringPointer(blockedReason),
		Version:             version,
		CreatedBySessionID:  nullableStringPointer(createdBySessionID),
		CreatedAt:           created,
		UpdatedAt:           updated,
		ClosedAt:            closed,
		ArchivedAt:          archived,
		ArchivedBySessionID: nullableStringPointer(archivedBySessionID),
		Labels:              []domain.Label{},
	}, nil
}

type issueUpdatedPayload struct {
	ChangedFields         []string         `json:"changed_fields"`
	Title                 *string          `json:"title,omitempty"`
	Type                  *domain.Type     `json:"type,omitempty"`
	Priority              *domain.Priority `json:"priority,omitempty"`
	Status                *domain.Status   `json:"status,omitempty"`
	ParentID              *string          `json:"parent_id,omitempty"`
	DescriptionSet        *bool            `json:"description_set,omitempty"`
	AcceptanceCriteriaSet *bool            `json:"acceptance_criteria_set,omitempty"`
	BlockedReasonSet      *bool            `json:"blocked_reason_set,omitempty"`
	Labels                []string         `json:"labels,omitempty"`
}

func newIssueUpdatedPayload(next domain.Issue, changedFields []string) issueUpdatedPayload {
	payload := issueUpdatedPayload{ChangedFields: append([]string(nil), changedFields...)}
	for _, field := range changedFields {
		switch field {
		case "title":
			value := next.Title
			payload.Title = &value
		case "type":
			value := next.Type
			payload.Type = &value
		case "priority":
			value := next.Priority
			payload.Priority = &value
		case "status":
			value := next.Status
			payload.Status = &value
		case "parent_id":
			payload.ParentID = copyOptionalString(next.ParentID)
		case "description":
			value := next.Description != nil
			payload.DescriptionSet = &value
		case "acceptance_criteria":
			value := next.AcceptanceCriteria != nil
			payload.AcceptanceCriteriaSet = &value
		case "blocked_reason":
			value := next.BlockedReason != nil
			payload.BlockedReasonSet = &value
		case "labels":
			payload.Labels = labelNames(next.Labels)
		}
	}
	return payload
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func copyOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func nullableStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func parseIssueTimestamp(field, value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, domain.WrapError(err, domain.CodeStorageCorrupt, "stored issue projection is invalid", false,
			domain.Detail{Field: field, Code: "INVALID_TIMESTAMP"})
	}
	if _, offset := parsed.Zone(); offset != 0 {
		return time.Time{}, domain.NewError(domain.CodeStorageCorrupt, "stored issue projection is invalid", false,
			domain.Detail{Field: field, Code: "INVALID_TIMESTAMP"})
	}
	return parsed.UTC(), nil
}

func parseNullableIssueTimestamp(field string, value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := parseIssueTimestamp(field, value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func corruptIssueProjection(cause error) error {
	return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored issue projection is invalid", false)
}

type issueCreatedPayload struct {
	SequenceNo int64           `json:"sequence_no"`
	Type       domain.Type     `json:"type"`
	Status     domain.Status   `json:"status"`
	Priority   domain.Priority `json:"priority"`
	ParentID   *string         `json:"parent_id,omitempty"`
	Labels     []string        `json:"labels,omitempty"`
}

type issueArchivedPayload struct {
	Version    int64  `json:"version"`
	ArchivedAt string `json:"archived_at"`
}

func validateParent(ctx context.Context, tx Executor, parentID *string) (*string, error) {
	if parentID == nil {
		return nil, nil
	}

	identifier, err := domain.ParseIssueIdentifier(*parentID)
	if err != nil {
		return nil, err
	}
	var (
		resolvedID string
		issueType  domain.Type
		archivedAt sql.NullString
	)
	var row *sql.Row
	switch identifier.Kind {
	case domain.IssueIdentifierInternalID:
		row = tx.QueryRowContext(ctx, "SELECT id, type, archived_at FROM issues WHERE id = ?", identifier.Value)
	case domain.IssueIdentifierDisplayID:
		row = tx.QueryRowContext(ctx, "SELECT id, type, archived_at FROM issues WHERE sequence_no = ?", identifier.SequenceNo)
	default:
		return nil, domain.NewError(
			domain.CodeInvalidArgument,
			"parent identifier is invalid",
			false,
			domain.Detail{Field: "parent_id", Code: "INVALID_IDENTIFIER"},
		)
	}
	err = row.Scan(&resolvedID, &issueType, &archivedAt)
	if err == sql.ErrNoRows {
		return nil, invalidParentError()
	}
	if err != nil {
		return nil, err
	}
	if issueType != domain.TypeEpic || archivedAt.Valid {
		return nil, invalidParentError()
	}
	return &resolvedID, nil
}

func invalidParentError() error {
	return domain.NewError(
		domain.CodeInvalidEpicParent,
		"parent_id must reference a non-archived epic",
		false,
		domain.Detail{Field: "parent_id", Code: domain.CodeInvalidEpicParent},
	)
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

var _ ports.IssueRepository = (*IssueRepository)(nil)
