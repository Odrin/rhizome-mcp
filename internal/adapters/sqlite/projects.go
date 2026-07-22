package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

// ProjectRepository is the SQLite implementation of ports.ProjectRepository.
type ProjectRepository struct {
	db *DB
}

// NewProjectRepository returns a project metadata repository backed by database.
func NewProjectRepository(database *DB) (*ProjectRepository, error) {
	if database == nil {
		return nil, domain.NewError(domain.CodeStorageConfiguration, "project database is required", false)
	}
	return &ProjectRepository{db: database}, nil
}

// GetProject reads the project row, applied migration version, and latest event
// from one SQLite snapshot. It performs no writes.
func (repository *ProjectRepository) GetProject(ctx context.Context) (domain.Project, error) {
	var project domain.Project
	err := repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		row, err := readProjectRow(ctx, query)
		if err != nil {
			return err
		}

		var schemaVersion int64
		if err := query.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
		).Scan(&schemaVersion); err != nil {
			return corruptProjectProjection(err)
		}
		if schemaVersion < 0 || int64(int(schemaVersion)) != schemaVersion {
			return corruptProjectProjection(fmt.Errorf("invalid schema version %d", schemaVersion))
		}

		var latestEventID int64
		if err := query.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(id), 0) FROM issue_events",
		).Scan(&latestEventID); err != nil {
			return corruptProjectProjection(err)
		}
		if latestEventID < 0 {
			return corruptProjectProjection(fmt.Errorf("invalid latest event ID %d", latestEventID))
		}

		project = row
		project.SchemaVersion = int(schemaVersion)
		project.LatestEventID = latestEventID
		return nil
	})
	if err != nil {
		return domain.Project{}, err
	}
	return project, nil
}

// ExportLogicalProject reads one SQLite snapshot and renders the logical project document.
func (repository *ProjectRepository) ExportLogicalProject(ctx context.Context) (domain.LogicalProjectDocument, error) {
	var document domain.LogicalProjectDocument
	err := repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		project, err := readProjectRow(ctx, query)
		if err != nil {
			return err
		}

		document.Format = "rhizome-logical-project"
		document.Version = 1
		document.Project = domain.LogicalProjectProject{
			ID:           project.ID,
			Name:         project.Name,
			Instructions: project.Instructions,
			CreatedAt:    formatLogicalProjectTimestamp(project.CreatedAt),
			UpdatedAt:    formatLogicalProjectTimestamp(project.UpdatedAt),
		}
		exportedAt := project.UpdatedAt

		issues, latest, err := readLogicalIssues(ctx, query)
		if err != nil {
			return err
		}
		document.Issues = issues
		if latest.After(exportedAt) {
			exportedAt = latest
		}

		labels, latest, err := readLogicalLabels(ctx, query)
		if err != nil {
			return err
		}
		document.Labels = labels
		if latest.After(exportedAt) {
			exportedAt = latest
		}

		issueLabels, latest, err := readLogicalIssueLabels(ctx, query)
		if err != nil {
			return err
		}
		document.IssueLabels = issueLabels
		if latest.After(exportedAt) {
			exportedAt = latest
		}

		relations, latest, err := readLogicalRelations(ctx, query)
		if err != nil {
			return err
		}
		document.Relations = relations
		if latest.After(exportedAt) {
			exportedAt = latest
		}

		comments, latest, err := readLogicalComments(ctx, query)
		if err != nil {
			return err
		}
		document.Comments = comments
		if latest.After(exportedAt) {
			exportedAt = latest
		}

		decisions, latest, err := readLogicalDecisions(ctx, query)
		if err != nil {
			return err
		}
		document.Decisions = decisions
		if latest.After(exportedAt) {
			exportedAt = latest
		}

		attempts, latest, err := readLogicalAttempts(ctx, query)
		if err != nil {
			return err
		}
		document.Attempts = attempts
		if latest.After(exportedAt) {
			exportedAt = latest
		}

		attemptNotes, latest, err := readLogicalAttemptNotes(ctx, query)
		if err != nil {
			return err
		}
		document.AttemptNotes = attemptNotes
		if latest.After(exportedAt) {
			exportedAt = latest
		}

		artifacts, latest, err := readLogicalArtifacts(ctx, query)
		if err != nil {
			return err
		}
		document.Artifacts = artifacts
		if latest.After(exportedAt) {
			exportedAt = latest
		}

		events, latest, err := readLogicalEvents(ctx, query)
		if err != nil {
			return err
		}
		document.Events = events
		if latest.After(exportedAt) {
			exportedAt = latest
		}

		document.ExportedAt = formatLogicalProjectTimestamp(exportedAt)
		return nil
	})
	if err != nil {
		return domain.LogicalProjectDocument{}, err
	}
	return document, nil
}

// HasLogicalProjectImportDestinationContent reports whether any durable project data exists.
func (repository *ProjectRepository) HasLogicalProjectImportDestinationContent(ctx context.Context) (bool, error) {
	var hasContent bool
	err := repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		row := query.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM issues
				UNION ALL
				SELECT 1 FROM labels
				UNION ALL
				SELECT 1 FROM issue_labels
				UNION ALL
				SELECT 1 FROM issue_relations
				UNION ALL
				SELECT 1 FROM comments
				UNION ALL
				SELECT 1 FROM decisions
				UNION ALL
				SELECT 1 FROM work_attempts
				UNION ALL
				SELECT 1 FROM attempt_notes
				UNION ALL
				SELECT 1 FROM artifacts
				UNION ALL
				SELECT 1 FROM issue_events
			)`)
		return row.Scan(&hasContent)
	})
	if err != nil {
		return false, err
	}
	return hasContent, nil
}

// ApplyLogicalProjectImport validates and atomically imports a logical project document into an empty destination.
func (repository *ProjectRepository) ApplyLogicalProjectImport(ctx context.Context, plan domain.LogicalProjectImportPlan) (domain.LogicalProjectImportApplyResult, error) {
	result := domain.LogicalProjectImportApplyResult{Counts: plan.DryRun.Counts}
	generator, err := ids.NewGenerator(clock.RealClock{}, rand.Reader)
	if err != nil {
		return result, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate import identifiers", false)
	}

	err = repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
			return err
		}
		hasContent, err := hasLogicalProjectImportDestinationContentInTransaction(ctx, tx)
		if err != nil {
			return err
		}
		if hasContent {
			latestEventID, err := latestIssueEventIDInTransaction(ctx, tx)
			if err != nil {
				return err
			}
			result.Conflicts = []domain.LogicalProjectImportConflict{{
				Code:    "empty_destination_required",
				Message: "destination project must be empty for this import",
				Field:   "$.destination",
			}}
			result.LatestEventID = latestEventID
			return nil
		}

		projectCreatedAt, err := parseLogicalProjectTimestamp("project.created_at", plan.Document.Project.CreatedAt)
		if err != nil {
			return err
		}
		projectUpdatedAt, err := parseLogicalProjectTimestamp("project.updated_at", plan.Document.Project.UpdatedAt)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE projects
			SET name = ?, instructions = ?, created_at = ?, updated_at = ?
		`, nullableString(plan.Document.Project.Name), nullableString(plan.Document.Project.Instructions),
			projectCreatedAt.UTC().Format(time.RFC3339Nano), projectUpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}

		var nextIssueNumber int64
		if err := tx.QueryRowContext(ctx, `SELECT next_issue_number FROM projects`).Scan(&nextIssueNumber); err != nil {
			return err
		}

		issueDestIDs := make(map[string]string, len(plan.Document.Issues))
		labelDestIDs := make(map[string]string, len(plan.Document.Labels))
		relationDestIDs := make(map[string]string, len(plan.Document.Relations))
		commentDestIDs := make(map[string]string, len(plan.Document.Comments))
		decisionDestIDs := make(map[string]string, len(plan.Document.Decisions))
		attemptDestIDs := make(map[string]string, len(plan.Document.Attempts))
		attemptNoteDestIDs := make(map[string]string, len(plan.Document.AttemptNotes))
		artifactDestIDs := make(map[string]string, len(plan.Document.Artifacts))

		for _, issue := range plan.Document.Issues {
			destID, err := generator.New()
			if err != nil {
				return domain.WrapError(err, domain.CodeIDGeneration, "cannot generate issue identifier", false)
			}
			issueDestIDs[issue.ID] = destID
		}
		for _, label := range plan.Document.Labels {
			destID, err := generator.New()
			if err != nil {
				return domain.WrapError(err, domain.CodeIDGeneration, "cannot generate label identifier", false)
			}
			labelDestIDs[label.ID] = destID
		}
		for _, relation := range plan.Document.Relations {
			destID, err := generator.New()
			if err != nil {
				return domain.WrapError(err, domain.CodeIDGeneration, "cannot generate relation identifier", false)
			}
			relationDestIDs[relation.ID] = destID
		}
		for _, comment := range plan.Document.Comments {
			destID, err := generator.New()
			if err != nil {
				return domain.WrapError(err, domain.CodeIDGeneration, "cannot generate comment identifier", false)
			}
			commentDestIDs[comment.ID] = destID
		}
		for _, decision := range plan.Document.Decisions {
			destID, err := generator.New()
			if err != nil {
				return domain.WrapError(err, domain.CodeIDGeneration, "cannot generate decision identifier", false)
			}
			decisionDestIDs[decision.ID] = destID
		}
		for _, attempt := range plan.Document.Attempts {
			destID, err := generator.New()
			if err != nil {
				return domain.WrapError(err, domain.CodeIDGeneration, "cannot generate attempt identifier", false)
			}
			attemptDestIDs[attempt.ID] = destID
		}
		for _, note := range plan.Document.AttemptNotes {
			destID, err := generator.New()
			if err != nil {
				return domain.WrapError(err, domain.CodeIDGeneration, "cannot generate attempt note identifier", false)
			}
			attemptNoteDestIDs[note.ID] = destID
		}
		for _, artifact := range plan.Document.Artifacts {
			destID, err := generator.New()
			if err != nil {
				return domain.WrapError(err, domain.CodeIDGeneration, "cannot generate artifact identifier", false)
			}
			artifactDestIDs[artifact.ID] = destID
		}

		for _, label := range plan.Document.Labels {
			createdAt, err := parseLogicalProjectTimestamp("labels.created_at", label.CreatedAt)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO labels(id, name, description, created_at) VALUES (?, ?, ?, ?)`,
				labelDestIDs[label.ID], label.Name, nullableString(label.Description), createdAt.UTC().Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}

		for index, issue := range plan.Document.Issues {
			createdAt, err := parseLogicalProjectTimestamp("issues.created_at", issue.CreatedAt)
			if err != nil {
				return err
			}
			updatedAt, err := parseLogicalProjectTimestamp("issues.updated_at", issue.UpdatedAt)
			if err != nil {
				return err
			}
			var parentID *string
			if issue.ParentID != nil {
				mappedParentID, ok := issueDestIDs[*issue.ParentID]
				if ok {
					parentID = &mappedParentID
				}
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO issues(
					id, sequence_no, type, title, description, acceptance_criteria,
					status, priority, parent_id, blocked_reason, version,
					created_by_session_id, created_at, updated_at, closed_at,
					archived_at, archived_by_session_id
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, NULL, ?, ?, NULL, NULL, NULL)
			`, issueDestIDs[issue.ID], nextIssueNumber+int64(index), issue.Type, issue.Title,
				nullableString(issue.Description), nullableString(issue.AcceptanceCriteria), issue.Status,
				issue.Priority, nullableString(parentID), nullableString(issue.BlockedReason),
				createdAt.UTC().Format(time.RFC3339Nano), updatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}

		for _, link := range plan.Document.IssueLabels {
			if _, err := tx.ExecContext(ctx, `INSERT INTO issue_labels(issue_id, label_id) VALUES (?, ?)`, issueDestIDs[link.IssueID], labelDestIDs[link.LabelID]); err != nil {
				return err
			}
		}

		for index, relation := range plan.Document.Relations {
			createdAt, err := parseLogicalProjectTimestamp("relations.created_at", relation.CreatedAt)
			if err != nil {
				return err
			}
			sourceID, targetID := domain.CanonicalRelationEndpoints(
				domain.RelationType(relation.Type), issueDestIDs[relation.SourceIssueID], issueDestIDs[relation.TargetIssueID],
			)
			if _, err := tx.ExecContext(ctx, `INSERT INTO issue_relations(id, source_issue_id, target_issue_id, type, created_at) VALUES (?, ?, ?, ?, ?)`,
				relationDestIDs[relation.ID], sourceID, targetID, relation.Type, createdAt.UTC().Format(time.RFC3339Nano)); err != nil {
				return logicalImportRelationWriteError(err, index)
			}
		}

		for _, comment := range plan.Document.Comments {
			createdAt, err := parseLogicalProjectTimestamp("comments.created_at", comment.CreatedAt)
			if err != nil {
				return err
			}
			var editedAt *string
			if comment.EditedAt != nil {
				parsedEditedAt, err := parseLogicalProjectTimestamp("comments.edited_at", *comment.EditedAt)
				if err != nil {
					return err
				}
				text := parsedEditedAt.UTC().Format(time.RFC3339Nano)
				editedAt = &text
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, issue_id, content, created_by_session_id, author_label, created_at, edited_at) VALUES (?, ?, ?, NULL, NULL, ?, ?)`,
				commentDestIDs[comment.ID], issueDestIDs[comment.IssueID], comment.Content, createdAt.UTC().Format(time.RFC3339Nano), nullableString(editedAt)); err != nil {
				return err
			}
		}

		for _, decision := range plan.Document.Decisions {
			createdAt, err := parseLogicalProjectTimestamp("decisions.created_at", decision.CreatedAt)
			if err != nil {
				return err
			}
			var issueID *string
			if decision.IssueID != nil {
				mappedID := issueDestIDs[*decision.IssueID]
				issueID = &mappedID
			}
			var supersedesID *string
			if decision.SupersedesID != nil {
				mappedID := decisionDestIDs[*decision.SupersedesID]
				supersedesID = &mappedID
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO decisions(id, issue_id, title, summary, content, status, supersedes_id, created_by_session_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?)`,
				decisionDestIDs[decision.ID], nullableString(issueID), decision.Title, decision.Summary, decision.Content, decision.Status, nullableString(supersedesID), createdAt.UTC().Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}

		for _, attempt := range plan.Document.Attempts {
			createdAt, err := parseLogicalProjectTimestamp("attempts.started_at", attempt.StartedAt)
			if err != nil {
				return err
			}
			leaseExpiresAt, err := parseLogicalProjectTimestamp("attempts.lease_expires_at", attempt.LeaseExpiresAt)
			if err != nil {
				return err
			}
			lastHeartbeatAt, err := parseLogicalProjectTimestamp("attempts.last_heartbeat_at", attempt.LastHeartbeatAt)
			if err != nil {
				return err
			}
			var finishedAt *string
			if attempt.FinishedAt != nil {
				parsedFinishedAt, err := parseLogicalProjectTimestamp("attempts.finished_at", *attempt.FinishedAt)
				if err != nil {
					return err
				}
				text := parsedFinishedAt.UTC().Format(time.RFC3339Nano)
				finishedAt = &text
			}
			var resultSummary *string
			if attempt.ResultSummary != nil {
				resultSummary = attempt.ResultSummary
			}
			var nextStepsJSON *string
			if len(attempt.NextSteps) > 0 {
				payload, err := json.Marshal(attempt.NextSteps)
				if err != nil {
					return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode attempt next steps", false)
				}
				text := string(payload)
				nextStepsJSON = &text
			}
			var verificationJSON *string
			if len(attempt.Verification) > 0 {
				payload, err := json.Marshal(attempt.Verification)
				if err != nil {
					return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode attempt verification", false)
				}
				text := string(payload)
				verificationJSON = &text
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(
				id, issue_id, session_id, agent_label, kind, status, issue_version_at_start,
				context_event_id_at_start, lease_token_hash, lease_expires_at, started_at,
				last_heartbeat_at, finished_at, result_summary, next_steps_json, verification_json,
				failure_reason_code, interruption_reason_code, reason_details
			) VALUES (?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				attemptDestIDs[attempt.ID], issueDestIDs[attempt.IssueID], nullableString(attempt.AgentLabel), attempt.Kind, attempt.Status,
				attempt.IssueVersionAtStart, attempt.ContextEventIDAtStart, []byte("logical-import-lease"), leaseExpiresAt.UTC().Format(time.RFC3339Nano),
				createdAt.UTC().Format(time.RFC3339Nano), lastHeartbeatAt.UTC().Format(time.RFC3339Nano), nullableString(finishedAt),
				nullableString(resultSummary), nullableString(nextStepsJSON), nullableString(verificationJSON), nullableString(attempt.FailureReasonCode),
				nullableString(attempt.InterruptionReasonCode), nullableString(attempt.ReasonDetails)); err != nil {
				return err
			}
		}

		for _, note := range plan.Document.AttemptNotes {
			createdAt, err := parseLogicalProjectTimestamp("attempt_notes.created_at", note.CreatedAt)
			if err != nil {
				return err
			}
			var nextStepsJSON *string
			if len(note.NextSteps) > 0 {
				payload, err := json.Marshal(note.NextSteps)
				if err != nil {
					return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode attempt note next steps", false)
				}
				text := string(payload)
				nextStepsJSON = &text
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO attempt_notes(id, attempt_id, kind, content, next_steps_json, important, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				attemptNoteDestIDs[note.ID], attemptDestIDs[note.AttemptID], note.Kind, note.Content, nullableString(nextStepsJSON), boolToInt(note.Important), createdAt.UTC().Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}

		for _, artifact := range plan.Document.Artifacts {
			createdAt, err := parseLogicalProjectTimestamp("artifacts.created_at", artifact.CreatedAt)
			if err != nil {
				return err
			}
			var attemptID *string
			if artifact.AttemptID != nil {
				mappedID := attemptDestIDs[*artifact.AttemptID]
				attemptID = &mappedID
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts(id, issue_id, attempt_id, type, uri, title, metadata, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				artifactDestIDs[artifact.ID], issueDestIDs[artifact.IssueID], nullableString(attemptID), artifact.Type, artifact.URI, nullableString(artifact.Title), nullableStringFromRawMessage(artifact.Metadata), createdAt.UTC().Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}

		for _, event := range plan.Document.Events {
			createdAt, err := parseLogicalProjectTimestamp("events.created_at", event.CreatedAt)
			if err != nil {
				return err
			}
			var issueID *string
			if event.IssueID != nil {
				mappedID := issueDestIDs[*event.IssueID]
				issueID = &mappedID
			}
			var attemptID *string
			if event.AttemptID != nil {
				mappedID := attemptDestIDs[*event.AttemptID]
				attemptID = &mappedID
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_type, session_id, attempt_id, payload, created_at) VALUES (?, ?, NULL, ?, ?, ?)`,
				nullableString(issueID), event.EventType, nullableString(attemptID), string(event.Payload), createdAt.UTC().Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}

		if len(plan.Document.Issues) > 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE projects SET next_issue_number = ?, updated_at = ?`, nextIssueNumber+int64(len(plan.Document.Issues)), projectUpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}

		result.LatestEventID, err = latestIssueEventIDInTransaction(ctx, tx)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	return result, nil
}

func logicalImportRelationWriteError(err error, index int) error {
	translated := TranslateError(err)
	var domainErr *domain.Error
	if !errors.As(translated, &domainErr) || domainErr.Code != domain.CodeStorageConstraint {
		return err
	}
	details := append([]domain.Detail(nil), domainErr.Details...)
	details = append(details, domain.Detail{
		EntityIndex: &index,
		Field:       fmt.Sprintf("$.relations[%d]", index),
		Code:        "IMPORT_STORAGE_CONSTRAINT",
		Message:     "relation violates a storage constraint",
	})
	return domain.WrapError(err, domainErr.Code, domainErr.Message, domainErr.Retryable, details...)
}

func hasLogicalProjectImportDestinationContentInTransaction(ctx context.Context, tx Executor) (bool, error) {
	var hasContent bool
	row := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM issues
			UNION ALL
			SELECT 1 FROM labels
			UNION ALL
			SELECT 1 FROM issue_labels
			UNION ALL
			SELECT 1 FROM issue_relations
			UNION ALL
			SELECT 1 FROM comments
			UNION ALL
			SELECT 1 FROM decisions
			UNION ALL
			SELECT 1 FROM work_attempts
			UNION ALL
			SELECT 1 FROM attempt_notes
			UNION ALL
			SELECT 1 FROM artifacts
			UNION ALL
			SELECT 1 FROM issue_events
		)`)
	if err := row.Scan(&hasContent); err != nil {
		return false, err
	}
	return hasContent, nil
}

func latestIssueEventIDInTransaction(ctx context.Context, tx Executor) (int64, error) {
	var latestEventID int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM issue_events`).Scan(&latestEventID); err != nil {
		return 0, err
	}
	return latestEventID, nil
}

func nullableStringFromRawMessage(value json.RawMessage) any {
	if value == nil {
		return nil
	}
	if len(value) == 0 {
		return nil
	}
	return string(value)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func readLogicalIssues(ctx context.Context, query Queryer) ([]domain.LogicalIssue, time.Time, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT id, type, title, description, acceptance_criteria, status, priority, parent_id, blocked_reason, created_at, updated_at, closed_at
		FROM issues
		WHERE archived_at IS NULL
		ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()

	issues := make([]domain.LogicalIssue, 0)
	var latest time.Time
	for rows.Next() {
		var (
			description, acceptanceCriteria, parentID, blockedReason, closedAt   sql.NullString
			id, issueType, title, status, priority, createdAtText, updatedAtText string
		)
		if err := rows.Scan(&id, &issueType, &title, &description, &acceptanceCriteria, &status, &priority, &parentID, &blockedReason, &createdAtText, &updatedAtText, &closedAt); err != nil {
			return nil, time.Time{}, corruptLogicalProjectValue(err, "issues")
		}
		if _, err := ids.ParseStrict(id); err != nil {
			return nil, time.Time{}, corruptLogicalProjectField(err, "id", "INVALID_ULID")
		}
		createdAt, err := parseLogicalProjectTimestamp("created_at", createdAtText)
		if err != nil {
			return nil, time.Time{}, err
		}
		updatedAt, err := parseLogicalProjectTimestamp("updated_at", updatedAtText)
		if err != nil {
			return nil, time.Time{}, err
		}
		if createdAt.After(latest) {
			latest = createdAt
		}
		if updatedAt.After(latest) {
			latest = updatedAt
		}
		issue := domain.LogicalIssue{
			ID:                 id,
			Type:               issueType,
			Title:              title,
			Description:        nullableLogicalString(description),
			AcceptanceCriteria: nullableLogicalString(acceptanceCriteria),
			Status:             status,
			Priority:           priority,
			ParentID:           nullableLogicalString(parentID),
			BlockedReason:      nullableLogicalString(blockedReason),
			CreatedBySessionID: nil,
			CreatedAt:          formatLogicalProjectTimestamp(createdAt),
			UpdatedAt:          formatLogicalProjectTimestamp(updatedAt),
			ClosedAt:           nullableLogicalString(closedAt),
		}
		if parentID.Valid {
			if _, err := ids.ParseStrict(parentID.String); err != nil {
				return nil, time.Time{}, corruptLogicalProjectField(err, "parent_id", "INVALID_ULID")
			}
		}
		issues = append(issues, issue)
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return issues, latest, nil
}

func readLogicalLabels(ctx context.Context, query Queryer) ([]domain.LogicalLabel, time.Time, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT id, name, description, created_at
		FROM labels
		ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()

	labels := make([]domain.LogicalLabel, 0)
	var latest time.Time
	for rows.Next() {
		var (
			description             sql.NullString
			id, name, createdAtText string
		)
		if err := rows.Scan(&id, &name, &description, &createdAtText); err != nil {
			return nil, time.Time{}, corruptLogicalProjectValue(err, "labels")
		}
		if _, err := ids.ParseStrict(id); err != nil {
			return nil, time.Time{}, corruptLogicalProjectField(err, "id", "INVALID_ULID")
		}
		createdAt, err := parseLogicalProjectTimestamp("created_at", createdAtText)
		if err != nil {
			return nil, time.Time{}, err
		}
		if createdAt.After(latest) {
			latest = createdAt
		}
		labels = append(labels, domain.LogicalLabel{ID: id, Name: name, Description: nullableLogicalString(description), CreatedAt: formatLogicalProjectTimestamp(createdAt)})
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return labels, latest, nil
}

func readLogicalIssueLabels(ctx context.Context, query Queryer) ([]domain.LogicalIssueLabel, time.Time, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT il.issue_id, il.label_id
		FROM issue_labels il
		JOIN issues i ON il.issue_id = i.id
		WHERE i.archived_at IS NULL
		ORDER BY il.issue_id ASC, il.label_id ASC`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()

	issueLabels := make([]domain.LogicalIssueLabel, 0)
	for rows.Next() {
		var issueID, labelID string
		if err := rows.Scan(&issueID, &labelID); err != nil {
			return nil, time.Time{}, corruptLogicalProjectValue(err, "issue_labels")
		}
		if _, err := ids.ParseStrict(issueID); err != nil {
			return nil, time.Time{}, corruptLogicalProjectField(err, "issue_id", "INVALID_ULID")
		}
		if _, err := ids.ParseStrict(labelID); err != nil {
			return nil, time.Time{}, corruptLogicalProjectField(err, "label_id", "INVALID_ULID")
		}
		issueLabels = append(issueLabels, domain.LogicalIssueLabel{IssueID: issueID, LabelID: labelID})
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return issueLabels, time.Time{}, nil
}

func readLogicalRelations(ctx context.Context, query Queryer) ([]domain.LogicalRelation, time.Time, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT r.id, r.source_issue_id, r.target_issue_id, r.type, r.created_at
		FROM issue_relations r
		JOIN issues source ON r.source_issue_id = source.id
		JOIN issues target ON r.target_issue_id = target.id
		WHERE source.archived_at IS NULL AND target.archived_at IS NULL
		ORDER BY r.source_issue_id ASC, r.target_issue_id ASC, r.type ASC`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()

	relations := make([]domain.LogicalRelation, 0)
	var latest time.Time
	for rows.Next() {
		var id, sourceIssueID, targetIssueID, relationType, createdAtText string
		if err := rows.Scan(&id, &sourceIssueID, &targetIssueID, &relationType, &createdAtText); err != nil {
			return nil, time.Time{}, corruptLogicalProjectValue(err, "relations")
		}
		for _, value := range []struct{ field, value string }{{"id", id}, {"source_issue_id", sourceIssueID}, {"target_issue_id", targetIssueID}} {
			if _, err := ids.ParseStrict(value.value); err != nil {
				return nil, time.Time{}, corruptLogicalProjectField(err, value.field, "INVALID_ULID")
			}
		}
		createdAt, err := parseLogicalProjectTimestamp("created_at", createdAtText)
		if err != nil {
			return nil, time.Time{}, err
		}
		if createdAt.After(latest) {
			latest = createdAt
		}
		relations = append(relations, domain.LogicalRelation{ID: id, SourceIssueID: sourceIssueID, TargetIssueID: targetIssueID, Type: relationType, CreatedBySessionID: nil, CreatedAt: formatLogicalProjectTimestamp(createdAt)})
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return relations, latest, nil
}

func readLogicalComments(ctx context.Context, query Queryer) ([]domain.LogicalComment, time.Time, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT c.id, c.issue_id, c.content, c.author_label, c.created_at, c.edited_at
		FROM comments c
		JOIN issues i ON c.issue_id = i.id
		WHERE i.archived_at IS NULL
		ORDER BY c.created_at ASC, c.id ASC`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()

	comments := make([]domain.LogicalComment, 0)
	var latest time.Time
	for rows.Next() {
		var (
			authorLabel, editedAt               sql.NullString
			id, issueID, content, createdAtText string
		)
		if err := rows.Scan(&id, &issueID, &content, &authorLabel, &createdAtText, &editedAt); err != nil {
			return nil, time.Time{}, corruptLogicalProjectValue(err, "comments")
		}
		if _, err := ids.ParseStrict(id); err != nil {
			return nil, time.Time{}, corruptLogicalProjectField(err, "id", "INVALID_ULID")
		}
		if _, err := ids.ParseStrict(issueID); err != nil {
			return nil, time.Time{}, corruptLogicalProjectField(err, "issue_id", "INVALID_ULID")
		}
		createdAt, err := parseLogicalProjectTimestamp("created_at", createdAtText)
		if err != nil {
			return nil, time.Time{}, err
		}
		if createdAt.After(latest) {
			latest = createdAt
		}
		comment := domain.LogicalComment{ID: id, IssueID: issueID, Content: content, CreatedBySessionID: nil, AuthorLabel: nullableLogicalString(authorLabel), CreatedAt: formatLogicalProjectTimestamp(createdAt), EditedAt: nullableLogicalString(editedAt)}
		if editedAt.Valid {
			editedTime, err := parseLogicalProjectTimestamp("edited_at", editedAt.String)
			if err != nil {
				return nil, time.Time{}, err
			}
			if editedTime.After(latest) {
				latest = editedTime
			}
			comment.EditedAt = ptrLogicalString(formatLogicalProjectTimestamp(editedTime))
		}
		comments = append(comments, comment)
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return comments, latest, nil
}

func readLogicalDecisions(ctx context.Context, query Queryer) ([]domain.LogicalDecision, time.Time, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT d.id, d.issue_id, d.title, d.summary, d.content, d.status, d.supersedes_id, d.created_at
		FROM decisions d
		LEFT JOIN issues i ON d.issue_id = i.id
		WHERE d.issue_id IS NULL OR i.archived_at IS NULL
		ORDER BY d.created_at ASC, d.id ASC`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()

	decisions := make([]domain.LogicalDecision, 0)
	var latest time.Time
	for rows.Next() {
		var (
			issueID, supersedesID                              sql.NullString
			id, title, summary, content, status, createdAtText string
		)
		if err := rows.Scan(&id, &issueID, &title, &summary, &content, &status, &supersedesID, &createdAtText); err != nil {
			return nil, time.Time{}, corruptLogicalProjectValue(err, "decisions")
		}
		if _, err := ids.ParseStrict(id); err != nil {
			return nil, time.Time{}, corruptLogicalProjectField(err, "id", "INVALID_ULID")
		}
		createdAt, err := parseLogicalProjectTimestamp("created_at", createdAtText)
		if err != nil {
			return nil, time.Time{}, err
		}
		if createdAt.After(latest) {
			latest = createdAt
		}
		decision := domain.LogicalDecision{ID: id, IssueID: nullableLogicalString(issueID), Title: title, Summary: summary, Content: content, Status: status, SupersedesID: nullableLogicalString(supersedesID), CreatedBySessionID: nil, CreatedAt: formatLogicalProjectTimestamp(createdAt)}
		if supersedesID.Valid {
			if _, err := ids.ParseStrict(supersedesID.String); err != nil {
				return nil, time.Time{}, corruptLogicalProjectField(err, "supersedes_id", "INVALID_ULID")
			}
		}
		if issueID.Valid {
			if _, err := ids.ParseStrict(issueID.String); err != nil {
				return nil, time.Time{}, corruptLogicalProjectField(err, "issue_id", "INVALID_ULID")
			}
		}
		decisions = append(decisions, decision)
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return decisions, latest, nil
}

func readLogicalAttempts(ctx context.Context, query Queryer) ([]domain.LogicalAttempt, time.Time, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT a.id, a.issue_id, a.agent_label, a.kind, a.status, a.issue_version_at_start, a.context_event_id_at_start, a.lease_expires_at, a.started_at, a.last_heartbeat_at, a.finished_at, a.result_summary, a.next_steps_json, a.verification_json, a.failure_reason_code, a.interruption_reason_code, a.reason_details
		FROM work_attempts a
		JOIN issues i ON a.issue_id = i.id
		WHERE i.archived_at IS NULL AND a.status <> 'active'
		ORDER BY a.started_at ASC, a.id ASC`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()

	attempts := make([]domain.LogicalAttempt, 0)
	var latest time.Time
	for rows.Next() {
		var (
			agentLabel, finishedAt, resultSummary, failureReasonCode, interruptionReasonCode, reasonDetails sql.NullString
			nextStepsJSON, verificationJSON                                                                 sql.NullString
			id, issueID, kind, status, leaseExpiresAtText, startedAtText, lastHeartbeatAtText               string
			issueVersionAtStart, contextEventIDAtStart                                                      int64
		)
		if err := rows.Scan(&id, &issueID, &agentLabel, &kind, &status, &issueVersionAtStart, &contextEventIDAtStart, &leaseExpiresAtText, &startedAtText, &lastHeartbeatAtText, &finishedAt, &resultSummary, &nextStepsJSON, &verificationJSON, &failureReasonCode, &interruptionReasonCode, &reasonDetails); err != nil {
			return nil, time.Time{}, corruptLogicalProjectValue(err, "attempts")
		}
		if _, err := ids.ParseStrict(id); err != nil {
			return nil, time.Time{}, corruptLogicalProjectField(err, "id", "INVALID_ULID")
		}
		if _, err := ids.ParseStrict(issueID); err != nil {
			return nil, time.Time{}, corruptLogicalProjectField(err, "issue_id", "INVALID_ULID")
		}
		leaseExpiresAt, err := parseLogicalProjectTimestamp("lease_expires_at", leaseExpiresAtText)
		if err != nil {
			return nil, time.Time{}, err
		}
		startedAt, err := parseLogicalProjectTimestamp("started_at", startedAtText)
		if err != nil {
			return nil, time.Time{}, err
		}
		lastHeartbeatAt, err := parseLogicalProjectTimestamp("last_heartbeat_at", lastHeartbeatAtText)
		if err != nil {
			return nil, time.Time{}, err
		}
		if leaseExpiresAt.After(latest) {
			latest = leaseExpiresAt
		}
		if startedAt.After(latest) {
			latest = startedAt
		}
		if lastHeartbeatAt.After(latest) {
			latest = lastHeartbeatAt
		}
		var nextSteps []string
		if nextStepsJSON.Valid {
			nextSteps, err = parseLogicalStringArray("next_steps", nextStepsJSON.String)
			if err != nil {
				return nil, time.Time{}, err
			}
		} else {
			nextSteps = []string{}
		}
		var verification []string
		if verificationJSON.Valid {
			verification, err = parseLogicalStringArray("verification", verificationJSON.String)
			if err != nil {
				return nil, time.Time{}, err
			}
		} else {
			verification = []string{}
		}
		attempt := domain.LogicalAttempt{
			ID:                     id,
			IssueID:                issueID,
			SessionID:              nil,
			AgentLabel:             nullableLogicalString(agentLabel),
			Kind:                   kind,
			Status:                 status,
			IssueVersionAtStart:    issueVersionAtStart,
			ContextEventIDAtStart:  contextEventIDAtStart,
			LeaseExpiresAt:         formatLogicalProjectTimestamp(leaseExpiresAt),
			StartedAt:              formatLogicalProjectTimestamp(startedAt),
			LastHeartbeatAt:        formatLogicalProjectTimestamp(lastHeartbeatAt),
			FinishedAt:             nullableLogicalString(finishedAt),
			ResultSummary:          nullableLogicalString(resultSummary),
			NextSteps:              nextSteps,
			Verification:           verification,
			FailureReasonCode:      nullableLogicalString(failureReasonCode),
			InterruptionReasonCode: nullableLogicalString(interruptionReasonCode),
			ReasonDetails:          nullableLogicalString(reasonDetails),
		}
		if finishedAt.Valid {
			finishedTime, err := parseLogicalProjectTimestamp("finished_at", finishedAt.String)
			if err != nil {
				return nil, time.Time{}, err
			}
			if finishedTime.After(latest) {
				latest = finishedTime
			}
			attempt.FinishedAt = ptrLogicalString(formatLogicalProjectTimestamp(finishedTime))
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return attempts, latest, nil
}

func readLogicalAttemptNotes(ctx context.Context, query Queryer) ([]domain.LogicalAttemptNote, time.Time, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT an.id, an.attempt_id, an.kind, an.content, an.next_steps_json, an.important, an.created_at
		FROM attempt_notes an
		JOIN work_attempts a ON an.attempt_id = a.id
		JOIN issues i ON a.issue_id = i.id
		WHERE i.archived_at IS NULL AND a.status <> 'active'
		ORDER BY an.created_at ASC, an.id ASC`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()

	attemptNotes := make([]domain.LogicalAttemptNote, 0)
	var latest time.Time
	for rows.Next() {
		var (
			nextStepsJSON                               sql.NullString
			id, attemptID, kind, content, createdAtText string
			important                                   int
		)
		if err := rows.Scan(&id, &attemptID, &kind, &content, &nextStepsJSON, &important, &createdAtText); err != nil {
			return nil, time.Time{}, corruptLogicalProjectValue(err, "attempt_notes")
		}
		if _, err := ids.ParseStrict(id); err != nil {
			return nil, time.Time{}, corruptLogicalProjectField(err, "id", "INVALID_ULID")
		}
		if _, err := ids.ParseStrict(attemptID); err != nil {
			return nil, time.Time{}, corruptLogicalProjectField(err, "attempt_id", "INVALID_ULID")
		}
		createdAt, err := parseLogicalProjectTimestamp("created_at", createdAtText)
		if err != nil {
			return nil, time.Time{}, err
		}
		if createdAt.After(latest) {
			latest = createdAt
		}
		var nextSteps []string
		if nextStepsJSON.Valid {
			nextSteps, err = parseLogicalStringArray("next_steps", nextStepsJSON.String)
			if err != nil {
				return nil, time.Time{}, err
			}
		} else {
			nextSteps = []string{}
		}
		attemptNotes = append(attemptNotes, domain.LogicalAttemptNote{ID: id, AttemptID: attemptID, Kind: kind, Content: content, NextSteps: nextSteps, Important: important == 1, CreatedAt: formatLogicalProjectTimestamp(createdAt)})
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return attemptNotes, latest, nil
}

func readLogicalArtifacts(ctx context.Context, query Queryer) ([]domain.LogicalArtifact, time.Time, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT a.id, a.issue_id, a.attempt_id, a.type, a.uri, a.title, a.metadata, a.created_at
		FROM artifacts a
		JOIN issues i ON a.issue_id = i.id
		WHERE i.archived_at IS NULL
			AND (a.attempt_id IS NULL OR a.attempt_id NOT IN (SELECT id FROM work_attempts WHERE status = 'active'))
		ORDER BY a.created_at ASC, a.id ASC`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()

	artifacts := make([]domain.LogicalArtifact, 0)
	var latest time.Time
	for rows.Next() {
		var (
			attemptID, title                              sql.NullString
			metadata                                      sql.NullString
			id, issueID, artifactType, uri, createdAtText string
		)
		if err := rows.Scan(&id, &issueID, &attemptID, &artifactType, &uri, &title, &metadata, &createdAtText); err != nil {
			return nil, time.Time{}, corruptLogicalProjectValue(err, "artifacts")
		}
		if _, err := ids.ParseStrict(id); err != nil {
			return nil, time.Time{}, corruptLogicalProjectField(err, "id", "INVALID_ULID")
		}
		if _, err := ids.ParseStrict(issueID); err != nil {
			return nil, time.Time{}, corruptLogicalProjectField(err, "issue_id", "INVALID_ULID")
		}
		createdAt, err := parseLogicalProjectTimestamp("created_at", createdAtText)
		if err != nil {
			return nil, time.Time{}, err
		}
		if createdAt.After(latest) {
			latest = createdAt
		}
		var rawMetadata json.RawMessage
		if metadata.Valid {
			rawMetadata, err = parseLogicalJSONBytes("metadata", metadata.String)
			if err != nil {
				return nil, time.Time{}, err
			}
		}
		artifacts = append(artifacts, domain.LogicalArtifact{ID: id, IssueID: issueID, AttemptID: nullableLogicalString(attemptID), Type: artifactType, URI: uri, Title: nullableLogicalString(title), Metadata: rawMetadata, CreatedAt: formatLogicalProjectTimestamp(createdAt)})
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return artifacts, latest, nil
}

func readLogicalEvents(ctx context.Context, query Queryer) ([]domain.LogicalEvent, time.Time, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT id, issue_id, event_type, attempt_id, payload, created_at
		FROM issue_events
		WHERE (issue_id IS NULL OR issue_id IN (SELECT id FROM issues WHERE archived_at IS NULL))
			AND (attempt_id IS NULL OR attempt_id NOT IN (SELECT id FROM work_attempts WHERE status = 'active'))
		ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()

	events := make([]domain.LogicalEvent, 0)
	var latest time.Time
	for rows.Next() {
		var (
			issueID, attemptID                    sql.NullString
			id                                    int64
			eventType, payloadText, createdAtText string
		)
		if err := rows.Scan(&id, &issueID, &eventType, &attemptID, &payloadText, &createdAtText); err != nil {
			return nil, time.Time{}, corruptLogicalProjectValue(err, "events")
		}
		if issueID.Valid {
			if _, err := ids.ParseStrict(issueID.String); err != nil {
				return nil, time.Time{}, corruptLogicalProjectField(err, "issue_id", "INVALID_ULID")
			}
		}
		if attemptID.Valid {
			if _, err := ids.ParseStrict(attemptID.String); err != nil {
				return nil, time.Time{}, corruptLogicalProjectField(err, "attempt_id", "INVALID_ULID")
			}
		}
		createdAt, err := parseLogicalProjectTimestamp("created_at", createdAtText)
		if err != nil {
			return nil, time.Time{}, err
		}
		if createdAt.After(latest) {
			latest = createdAt
		}
		payload, err := parseLogicalJSONBytes("payload", payloadText)
		if err != nil {
			return nil, time.Time{}, err
		}
		events = append(events, domain.LogicalEvent{SourceID: id, IssueID: nullableLogicalString(issueID), EventType: eventType, SessionID: nil, AttemptID: nullableLogicalString(attemptID), Payload: payload, CreatedAt: formatLogicalProjectTimestamp(createdAt)})
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return events, latest, nil
}

func nullableLogicalString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func ptrLogicalString(value string) *string {
	return &value
}

func parseLogicalJSONBytes(field, value string) (json.RawMessage, error) {
	if !json.Valid([]byte(value)) {
		return nil, corruptLogicalProjectField(fmt.Errorf("invalid JSON for %s", field), field, "INVALID_JSON")
	}
	return json.RawMessage(value), nil
}

func parseLogicalStringArray(field, value string) ([]string, error) {
	if !json.Valid([]byte(value)) {
		return nil, corruptLogicalProjectField(fmt.Errorf("invalid JSON for %s", field), field, "INVALID_JSON")
	}
	var result []string
	if err := json.Unmarshal([]byte(value), &result); err != nil {
		return nil, corruptLogicalProjectField(err, field, "INVALID_JSON_TYPE")
	}
	if result == nil {
		return []string{}, nil
	}
	return result, nil
}

func parseLogicalProjectTimestamp(field, value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, corruptLogicalProjectField(err, field, "INVALID_TIMESTAMP")
	}
	if _, offset := parsed.Zone(); offset != 0 {
		return time.Time{}, corruptLogicalProjectField(nil, field, "INVALID_TIMESTAMP")
	}
	return parsed.UTC(), nil
}

func formatLogicalProjectTimestamp(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func corruptLogicalProjectField(cause error, field, code string) error {
	detail := domain.Detail{Field: field, Code: code}
	return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored logical project export is invalid", false, detail)
}

func corruptLogicalProjectValue(cause error, field string) error {
	return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored logical project export is invalid", false, domain.Detail{Field: field, Code: "INVALID_VALUE"})
}

func readProjectRow(ctx context.Context, query Queryer) (domain.Project, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT id, name, instructions, next_issue_number, created_at, updated_at
		FROM projects
		ORDER BY id ASC
		LIMIT 2`)
	if err != nil {
		return domain.Project{}, err
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return domain.Project{}, err
		}
		return domain.Project{}, domain.NewError(
			domain.CodeProjectNotInitialized,
			"project database is not initialized",
			false,
		)
	}

	var (
		name, instructions       sql.NullString
		nextIssueNumber          int64
		createdAt, updatedAt, id string
	)
	if err := rows.Scan(&id, &name, &instructions, &nextIssueNumber, &createdAt, &updatedAt); err != nil {
		return domain.Project{}, corruptProjectProjection(err)
	}
	if rows.Next() {
		return domain.Project{}, domain.NewError(
			domain.CodeStorageCorrupt,
			"stored project projection is invalid",
			false,
		)
	}
	if err := rows.Err(); err != nil {
		return domain.Project{}, err
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return domain.Project{}, corruptProjectProjection(err)
	}
	if nextIssueNumber < 1 {
		return domain.Project{}, corruptProjectProjection(fmt.Errorf("invalid project values"))
	}

	created, err := parseProjectTimestamp("created_at", createdAt)
	if err != nil {
		return domain.Project{}, err
	}
	updated, err := parseProjectTimestamp("updated_at", updatedAt)
	if err != nil {
		return domain.Project{}, err
	}
	return domain.Project{
		ID:              id,
		Name:            nullableProjectString(name),
		Instructions:    nullableProjectString(instructions),
		NextIssueNumber: nextIssueNumber,
		CreatedAt:       created,
		UpdatedAt:       updated,
	}, nil
}

func nullableProjectString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func parseProjectTimestamp(field, value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, corruptProjectTimestamp(err, field)
	}
	if _, offset := parsed.Zone(); offset != 0 {
		return time.Time{}, corruptProjectTimestamp(nil, field)
	}
	return parsed.UTC(), nil
}

func corruptProjectTimestamp(cause error, field string) error {
	detail := domain.Detail{Field: field, Code: "INVALID_TIMESTAMP"}
	if cause != nil {
		return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored project projection is invalid", false, detail)
	}
	return domain.NewError(domain.CodeStorageCorrupt, "stored project projection is invalid", false, detail)
}

func corruptProjectProjection(cause error) error {
	return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored project projection is invalid", false)
}

var _ ports.ProjectRepository = (*ProjectRepository)(nil)
