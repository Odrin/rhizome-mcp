package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
		document.Version = 2
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

		reviewTargets, latest, err := readLogicalReviewTargets(ctx, query)
		if err != nil {
			return err
		}
		document.ReviewTargets = reviewTargets
		if latest.After(exportedAt) {
			exportedAt = latest
		}

		reviewRequests, latest, err := readLogicalReviewRequests(ctx, query)
		if err != nil {
			return err
		}
		document.ReviewRequests = reviewRequests
		if latest.After(exportedAt) {
			exportedAt = latest
		}

		reviewOutcomes, latest, err := readLogicalReviewOutcomes(ctx, query)
		if err != nil {
			return err
		}
		document.ReviewOutcomes = reviewOutcomes
		if latest.After(exportedAt) {
			exportedAt = latest
		}

		reviewEvents, latest, err := readLogicalReviewEvents(ctx, query)
		if err != nil {
			return err
		}
		document.ReviewEvents = reviewEvents
		if latest.After(exportedAt) {
			exportedAt = latest
		}

		// Reservations ride in the version-2 extensions map rather than a
		// top-level array (ISSUE-215). The namespace is emitted only when
		// there is something to carry, so a project that never reserved a
		// resource exports exactly the document it exported before.
		// Reservation reserved/released events are not carried here: like
		// review events, they are already in document.Events, which
		// exports issue_events wholesale.
		reservations, latest, err := readLogicalReservations(ctx, query)
		if err != nil {
			return err
		}
		if len(reservations) > 0 {
			payload, err := json.Marshal(domain.LogicalReservationsExtension{
				Version: domain.LogicalReservationsExtensionVersion,
				Records: reservations,
			})
			if err != nil {
				return err
			}
			if document.Extensions == nil {
				document.Extensions = make(map[string]json.RawMessage, 1)
			}
			document.Extensions[domain.LogicalReservationsExtensionKey] = payload
		}
		if latest.After(exportedAt) {
			exportedAt = latest
		}

		// Workflow-gate state rides in its own extensions namespace
		// (ISSUE-175 AC3), following the reservations pattern exactly: the
		// namespace is emitted only when there is something to carry, so a
		// project that never configured a gate exports exactly the document
		// it exported before.
		gatesExtension, latest, err := readLogicalGates(ctx, query)
		if err != nil {
			return err
		}
		if !gatesExtension.IsEmpty() {
			payload, err := json.Marshal(gatesExtension)
			if err != nil {
				return err
			}
			if document.Extensions == nil {
				document.Extensions = make(map[string]json.RawMessage, 1)
			}
			document.Extensions[domain.LogicalGatesExtensionKey] = payload
		}
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

// ApplyLogicalProjectImport validates and atomically imports a logical
// project document into an empty destination. Every destination ID is
// pre-generated by the caller (application.ProjectService, using its own
// injected clock and ID generator) and carried on plan.DestinationIDs --
// see domain.NewLogicalProjectImportDestinationIDs -- rather than minted
// here: a generator constructed inside this write transaction would use a
// real clock regardless of what the caller injected, and would mint
// different IDs on every DB.Write retry.
func (repository *ProjectRepository) ApplyLogicalProjectImport(ctx context.Context, plan domain.LogicalProjectImportPlan) (domain.LogicalProjectImportApplyResult, error) {
	result := domain.LogicalProjectImportApplyResult{Counts: plan.DryRun.Counts}

	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
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
			formatStorageTime(projectCreatedAt), formatStorageTime(projectUpdatedAt)); err != nil {
			return err
		}

		var nextIssueNumber int64
		if err := tx.QueryRowContext(ctx, `SELECT next_issue_number FROM projects`).Scan(&nextIssueNumber); err != nil {
			return err
		}

		issueDestIDs := plan.DestinationIDs.IssueIDs
		labelDestIDs := plan.DestinationIDs.LabelIDs
		relationDestIDs := plan.DestinationIDs.RelationIDs
		commentDestIDs := plan.DestinationIDs.CommentIDs
		decisionDestIDs := plan.DestinationIDs.DecisionIDs
		attemptDestIDs := plan.DestinationIDs.AttemptIDs
		attemptNoteDestIDs := plan.DestinationIDs.AttemptNoteIDs
		artifactDestIDs := plan.DestinationIDs.ArtifactIDs
		for _, issue := range plan.Document.Issues {
			if _, ok := issueDestIDs[issue.ID]; !ok {
				return domain.NewError(domain.CodeStorageCorrupt, "import plan is missing a destination issue identifier", false)
			}
		}
		for _, label := range plan.Document.Labels {
			if _, ok := labelDestIDs[label.ID]; !ok {
				return domain.NewError(domain.CodeStorageCorrupt, "import plan is missing a destination label identifier", false)
			}
		}
		for _, relation := range plan.Document.Relations {
			if _, ok := relationDestIDs[relation.ID]; !ok {
				return domain.NewError(domain.CodeStorageCorrupt, "import plan is missing a destination relation identifier", false)
			}
		}
		for _, comment := range plan.Document.Comments {
			if _, ok := commentDestIDs[comment.ID]; !ok {
				return domain.NewError(domain.CodeStorageCorrupt, "import plan is missing a destination comment identifier", false)
			}
		}
		for _, decision := range plan.Document.Decisions {
			if _, ok := decisionDestIDs[decision.ID]; !ok {
				return domain.NewError(domain.CodeStorageCorrupt, "import plan is missing a destination decision identifier", false)
			}
		}
		for _, attempt := range plan.Document.Attempts {
			if _, ok := attemptDestIDs[attempt.ID]; !ok {
				return domain.NewError(domain.CodeStorageCorrupt, "import plan is missing a destination attempt identifier", false)
			}
		}
		for _, note := range plan.Document.AttemptNotes {
			if _, ok := attemptNoteDestIDs[note.ID]; !ok {
				return domain.NewError(domain.CodeStorageCorrupt, "import plan is missing a destination attempt note identifier", false)
			}
		}
		for _, artifact := range plan.Document.Artifacts {
			if _, ok := artifactDestIDs[artifact.ID]; !ok {
				return domain.NewError(domain.CodeStorageCorrupt, "import plan is missing a destination artifact identifier", false)
			}
		}

		for _, label := range plan.Document.Labels {
			createdAt, err := parseLogicalProjectTimestamp("labels.created_at", label.CreatedAt)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO labels(id, name, description, created_at) VALUES (?, ?, ?, ?)`,
				labelDestIDs[label.ID], label.Name, nullableString(label.Description), formatStorageTime(createdAt)); err != nil {
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
			// apply_import is exempt from gate evaluation (docs/02 §17.1,
			// ISSUE-201): it restores historical terminal state, not a live
			// transition. It is no longer exempt from validation, though --
			// every imported issue runs the same field/enum/limit checks
			// create_issue runs.
			if _, err := (domain.CreateIssueInput{
				Type: domain.Type(issue.Type), Title: issue.Title, Description: issue.Description,
				AcceptanceCriteria: issue.AcceptanceCriteria, Status: domain.Status(issue.Status), Priority: domain.Priority(issue.Priority),
				ParentID: parentID, BlockedReason: issue.BlockedReason,
			}).Validate(); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO issues(
					id, sequence_no, type, title, description, acceptance_criteria,
					status, priority, parent_id, blocked_reason, version,
					created_by_session_id, created_at, updated_at, closed_at,
					archived_at, archived_by_session_id
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, NULL, NULL, NULL)
			`, issueDestIDs[issue.ID], nextIssueNumber+int64(index), issue.Type, issue.Title,
				nullableString(issue.Description), nullableString(issue.AcceptanceCriteria), issue.Status,
				issue.Priority, nullableString(parentID), nullableString(issue.BlockedReason),
				domain.LogicalIssueVersionForImport(issue.Version),
				formatStorageTime(createdAt), formatStorageTime(updatedAt)); err != nil {
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
				relationDestIDs[relation.ID], sourceID, targetID, relation.Type, formatStorageTime(createdAt)); err != nil {
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
				text := formatStorageTime(parsedEditedAt)
				editedAt = &text
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, issue_id, content, created_by_session_id, author_label, created_at, edited_at) VALUES (?, ?, ?, NULL, NULL, ?, ?)`,
				commentDestIDs[comment.ID], issueDestIDs[comment.IssueID], comment.Content, formatStorageTime(createdAt), nullableString(editedAt)); err != nil {
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
				decisionDestIDs[decision.ID], nullableString(issueID), decision.Title, decision.Summary, decision.Content, decision.Status, nullableString(supersedesID), formatStorageTime(createdAt)); err != nil {
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
				text := formatStorageTime(parsedFinishedAt)
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
				attempt.IssueVersionAtStart, attempt.ContextEventIDAtStart, []byte("logical-import-lease"), formatStorageTime(leaseExpiresAt),
				formatStorageTime(createdAt), formatStorageTime(lastHeartbeatAt), nullableString(finishedAt),
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
				attemptNoteDestIDs[note.ID], attemptDestIDs[note.AttemptID], note.Kind, note.Content, nullableString(nextStepsJSON), boolToInt(note.Important), formatStorageTime(createdAt)); err != nil {
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
				artifactDestIDs[artifact.ID], issueDestIDs[artifact.IssueID], nullableString(attemptID), artifact.Type, artifact.URI, nullableString(artifact.Title), nullableStringFromRawMessage(artifact.Metadata), formatStorageTime(createdAt)); err != nil {
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
			// A version 1 document carries no source (it predates the
			// unified log), so its events restore as ordinary issue
			// events. A version 2 document round-trips the tag verbatim:
			// dropping it here silently reclassified every review event as
			// issue activity, which changes what review staleness sees
			// after a restore (ISSUE-215).
			source := event.Source
			if source == "" {
				source = domain.LogicalEventSourceIssue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_type, session_id, attempt_id, payload, created_at, source) VALUES (?, ?, NULL, ?, ?, ?, ?)`,
				nullableString(issueID), event.EventType, nullableString(attemptID), string(event.Payload), formatStorageTime(createdAt), source); err != nil {
				return err
			}
		}

		reviewTargetDestIDs := plan.DestinationIDs.ReviewTargetIDs
		reviewRequestDestIDs := plan.DestinationIDs.ReviewRequestIDs
		reviewOutcomeDestIDs := plan.DestinationIDs.ReviewOutcomeIDs
		for _, target := range plan.Document.ReviewTargets {
			if _, ok := reviewTargetDestIDs[target.ID]; !ok {
				return domain.NewError(domain.CodeStorageCorrupt, "import plan is missing a destination review target identifier", false)
			}
		}
		for _, request := range plan.Document.ReviewRequests {
			if _, ok := reviewRequestDestIDs[request.ID]; !ok {
				return domain.NewError(domain.CodeStorageCorrupt, "import plan is missing a destination review request identifier", false)
			}
		}
		for _, outcome := range plan.Document.ReviewOutcomes {
			if _, ok := reviewOutcomeDestIDs[outcome.ID]; !ok {
				return domain.NewError(domain.CodeStorageCorrupt, "import plan is missing a destination review outcome identifier", false)
			}
		}

		for _, target := range plan.Document.ReviewTargets {
			createdAt, err := parseLogicalProjectTimestamp("review_targets.created_at", target.CreatedAt)
			if err != nil {
				return err
			}
			artifactIDsJSON, err := json.Marshal(target.ArtifactIDs)
			if err != nil {
				return err
			}
			purposesJSON, err := logicalPurposesJSONForImport(target.Purposes)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO review_targets(id, issue_id, issue_version, latest_event_id, artifact_ids_json, purposes_json, version, created_at) VALUES (?, ?, ?, ?, ?, ?, 1, ?)`,
				reviewTargetDestIDs[target.ID], issueDestIDs[target.IssueID], target.IssueVersion, target.LatestEventID, string(artifactIDsJSON), purposesJSON, formatStorageTime(createdAt)); err != nil {
				return err
			}
		}

		for _, request := range plan.Document.ReviewRequests {
			createdAt, err := parseLogicalProjectTimestamp("review_requests.created_at", request.CreatedAt)
			if err != nil {
				return err
			}
			var resolvedAt *string
			if request.ResolvedAt != nil {
				parsedTime, err := parseLogicalProjectTimestamp("review_requests.resolved_at", *request.ResolvedAt)
				if err != nil {
					return err
				}
				formattedTime := formatStorageTime(parsedTime)
				resolvedAt = &formattedTime
			}
			var supersedesID *string
			if request.SupersedesID != nil {
				mappedID := reviewRequestDestIDs[*request.SupersedesID]
				supersedesID = &mappedID
			}
			artifactIDsJSON, err := json.Marshal(request.ArtifactIDs)
			if err != nil {
				return err
			}
			purposesJSON, err := logicalPurposesJSONForImport(request.Purposes)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO review_requests(id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, purposes_json, status, supersedes_id, active_attempt_id, version, created_at, resolved_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, 1, ?, ?)`,
				reviewRequestDestIDs[request.ID], reviewTargetDestIDs[request.TargetID], issueDestIDs[request.IssueID], request.TargetIssueVersion, request.TargetEventID, string(artifactIDsJSON), purposesJSON, request.Status, nullableString(supersedesID), formatStorageTime(createdAt), nullableString(resolvedAt)); err != nil {
				return err
			}
		}

		for _, outcome := range plan.Document.ReviewOutcomes {
			createdAt, err := parseLogicalProjectTimestamp("review_outcomes.created_at", outcome.CreatedAt)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO review_outcomes(id, request_id, attempt_id, outcome, reason, version, created_at) VALUES (?, ?, ?, ?, ?, 1, ?)`,
				reviewOutcomeDestIDs[outcome.ID], reviewRequestDestIDs[outcome.RequestID], attemptDestIDs[outcome.AttemptID], outcome.Outcome, nullableString(outcome.Reason), formatStorageTime(createdAt)); err != nil {
				return err
			}
		}

		// Reservations arrive in the extensions namespace, not a top-level
		// array, so they are decoded rather than ranged over. Parsing has
		// already validated the namespace (released-only, references
		// resolvable and consistent with the owning attempt's issue), so
		// this is a plain insert.
		reservations, err := plan.Document.DecodeReservationsExtension()
		if err != nil {
			return err
		}
		reservationDestIDs := plan.DestinationIDs.ReservationIDs
		for _, reservation := range reservations {
			if _, ok := reservationDestIDs[reservation.ID]; !ok {
				return domain.NewError(domain.CodeStorageCorrupt, "import plan is missing a destination reservation identifier", false)
			}
		}
		for _, reservation := range reservations {
			createdAt, err := parseLogicalProjectTimestamp("reservations.created_at", reservation.CreatedAt)
			if err != nil {
				return err
			}
			releasedAt, err := parseLogicalProjectTimestamp("reservations.released_at", reservation.ReleasedAt)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO resource_reservations(id, issue_id, attempt_id, kind, display_value, comparison_value, normalized_json, status, version, created_at, released_at, release_reason) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`,
				reservationDestIDs[reservation.ID], issueDestIDs[reservation.IssueID], attemptDestIDs[reservation.AttemptID],
				reservation.Kind, reservation.DisplayValue, reservation.ComparisonValue, string(reservation.NormalizedJSON),
				reservation.Status, formatStorageTime(createdAt), formatStorageTime(releasedAt), reservation.ReleaseReason); err != nil {
				return err
			}
		}

		// Workflow-gate state arrives in its own extensions namespace
		// (ISSUE-175 AC3). Parsing has already validated every record and
		// its references, so this is a plain insert with ID remapping on
		// the reference columns. Snapshot and audit payload blobs are
		// inserted verbatim: their embedded identities are frozen audit
		// facts protected by the snapshot fingerprint (see
		// domain.LogicalAttemptGateSnapshot).
		gates, err := plan.Document.DecodeGatesExtension()
		if err != nil {
			return err
		}
		workflowPolicyDestIDs := plan.DestinationIDs.WorkflowPolicyIDs
		gateEvidenceDestIDs := plan.DestinationIDs.GateEvidenceIDs
		reviewApprovalDestIDs := plan.DestinationIDs.ReviewApprovalIDs
		for _, policy := range gates.Policies {
			if _, ok := workflowPolicyDestIDs[policy.ID]; !ok {
				return domain.NewError(domain.CodeStorageCorrupt, "import plan is missing a destination workflow policy identifier", false)
			}
		}
		for _, evidence := range gates.Evidence {
			if _, ok := gateEvidenceDestIDs[evidence.ID]; !ok {
				return domain.NewError(domain.CodeStorageCorrupt, "import plan is missing a destination gate evidence identifier", false)
			}
		}
		for _, approval := range gates.ReviewApprovals {
			if _, ok := reviewApprovalDestIDs[approval.ID]; !ok {
				return domain.NewError(domain.CodeStorageCorrupt, "import plan is missing a destination review approval identifier", false)
			}
		}

		for _, policy := range gates.Policies {
			createdAt, err := parseLogicalProjectTimestamp("gates.policies.created_at", policy.CreatedAt)
			if err != nil {
				return err
			}
			updatedAt, err := parseLogicalProjectTimestamp("gates.policies.updated_at", policy.UpdatedAt)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_policies(id, selector_json, requirements_json, status, version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				workflowPolicyDestIDs[policy.ID], string(policy.SelectorJSON), string(policy.RequirementsJSON),
				policy.Status, policy.Version, formatStorageTime(createdAt), formatStorageTime(updatedAt)); err != nil {
				return err
			}
		}
		for _, event := range gates.PolicyEvents {
			createdAt, err := parseLogicalProjectTimestamp("gates.policy_events.created_at", event.CreatedAt)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_policy_events(policy_id, event_type, session_id, prior_version, new_version, payload, created_at) VALUES (?, ?, NULL, ?, ?, ?, ?)`,
				workflowPolicyDestIDs[event.PolicyID], event.EventType, nullableInt64Pointer(event.PriorVersion),
				event.NewVersion, string(event.Payload), formatStorageTime(createdAt)); err != nil {
				return err
			}
		}
		for _, snapshot := range gates.AttemptSnapshots {
			createdAt, err := parseLogicalProjectTimestamp("gates.attempt_snapshots.created_at", snapshot.CreatedAt)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO attempt_gate_snapshots(attempt_id, requirements_json, source_policies_json, fingerprint, issue_version, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
				attemptDestIDs[snapshot.AttemptID], string(snapshot.RequirementsJSON), string(snapshot.SourcePoliciesJSON),
				snapshot.Fingerprint, snapshot.IssueVersion, formatStorageTime(createdAt)); err != nil {
				return err
			}
		}

		// Every imported review target gets a snapshot row, restoring the
		// invariant migration 012 established (every target has one, so
		// reading a target snapshot stays an unconditional read). Targets
		// the document carries a snapshot for get that snapshot; the rest
		// -- v1 documents and pre-gates v2 documents -- get the same empty
		// sentinel backfill the migration wrote, which evaluates exactly
		// like the missing row would (zero requirements), so their
		// behaviour is unchanged.
		targetSnapshots := make(map[string]domain.LogicalReviewTargetGateSnapshot, len(gates.ReviewTargetSnapshots))
		for _, snapshot := range gates.ReviewTargetSnapshots {
			targetSnapshots[snapshot.TargetID] = snapshot
		}
		for _, target := range plan.Document.ReviewTargets {
			snapshot, carried := targetSnapshots[target.ID]
			if !carried {
				snapshot = domain.LogicalReviewTargetGateSnapshot{
					TargetID:           target.ID,
					RequirementsJSON:   json.RawMessage("[]"),
					SourcePoliciesJSON: json.RawMessage("[]"),
					Fingerprint:        "0000000000000000000000000000000000000000000000000000000000000000",
					IssueVersion:       target.IssueVersion,
					CreatedAt:          target.CreatedAt,
				}
			}
			createdAt, err := parseLogicalProjectTimestamp("gates.review_target_snapshots.created_at", snapshot.CreatedAt)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO review_target_gate_snapshots(target_id, requirements_json, source_policies_json, fingerprint, issue_version, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
				reviewTargetDestIDs[target.ID], string(snapshot.RequirementsJSON), string(snapshot.SourcePoliciesJSON),
				snapshot.Fingerprint, snapshot.IssueVersion, formatStorageTime(createdAt)); err != nil {
				return err
			}
		}

		for _, evidence := range gates.Evidence {
			createdAt, err := parseLogicalProjectTimestamp("gates.evidence.created_at", evidence.CreatedAt)
			if err != nil {
				return err
			}
			updatedAt, err := parseLogicalProjectTimestamp("gates.evidence.updated_at", evidence.UpdatedAt)
			if err != nil {
				return err
			}
			artifactIDsJSON, err := json.Marshal(evidence.ArtifactIDs)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO gate_evidence(id, attempt_id, issue_id, key, result, summary, details, artifact_ids_json, version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				gateEvidenceDestIDs[evidence.ID], attemptDestIDs[evidence.AttemptID], issueDestIDs[evidence.IssueID],
				evidence.Key, evidence.Result, evidence.Summary, nullableString(evidence.Details), string(artifactIDsJSON),
				evidence.Version, formatStorageTime(createdAt), formatStorageTime(updatedAt)); err != nil {
				return err
			}
		}
		for _, event := range gates.EvidenceEvents {
			createdAt, err := parseLogicalProjectTimestamp("gates.evidence_events.created_at", event.CreatedAt)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO gate_evidence_events(evidence_id, attempt_id, issue_id, key, event_type, version, payload, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				gateEvidenceDestIDs[event.EvidenceID], attemptDestIDs[event.AttemptID], issueDestIDs[event.IssueID],
				event.Key, event.EventType, event.Version, string(event.Payload), formatStorageTime(createdAt)); err != nil {
				return err
			}
		}
		for _, approval := range gates.ReviewApprovals {
			createdAt, err := parseLogicalProjectTimestamp("gates.review_approvals.created_at", approval.CreatedAt)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO review_approvals(id, issue_id, target_id, request_id, attempt_id, purpose, target_issue_version, target_event_id, version, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				reviewApprovalDestIDs[approval.ID], issueDestIDs[approval.IssueID], reviewTargetDestIDs[approval.TargetID],
				reviewRequestDestIDs[approval.RequestID], attemptDestIDs[approval.AttemptID], approval.Purpose,
				approval.TargetIssueVersion, approval.TargetEventID, approval.Version, formatStorageTime(createdAt)); err != nil {
				return err
			}
		}

		// plan.Document.ReviewEvents is deliberately NOT re-inserted here:
		// every review-sourced event it contains is already present in
		// plan.Document.Events too (readLogicalEvents exports all of
		// issue_events, review-sourced rows included -- see
		// TestProjectRepositoryExportIncludesReviewSourcedEventsAndImportPreservesThem,
		// which pins this on purpose), and that array's own insertion loop
		// above already recreates every one of those rows. Re-inserting
		// them again from ReviewEvents would duplicate them. ReviewEvents
		// exists as an export-time convenience projection (typed
		// request_id/target_id instead of buried in an opaque payload,
		// scoped to the review workflow) for tools and humans reading the
		// interchange document directly, not as a second source of truth
		// to replay; its referential integrity is still checked at parse
		// time regardless (see validateLogicalProjectDocumentSemantics).

		if len(plan.Document.Issues) > 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE projects SET next_issue_number = ?, updated_at = ?`, nextIssueNumber+int64(len(plan.Document.Issues)), formatStorageTime(projectUpdatedAt)); err != nil {
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
		SELECT id, type, title, description, acceptance_criteria, status, priority, parent_id, blocked_reason, created_at, updated_at, closed_at, version
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
			version                                                              int64
		)
		if err := rows.Scan(&id, &issueType, &title, &description, &acceptanceCriteria, &status, &priority, &parentID, &blockedReason, &createdAtText, &updatedAtText, &closedAt, &version); err != nil {
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
			Version:            domain.LogicalIssueVersionForExport(version),
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

// readLogicalReservations exports released reservations whose owning
// attempt itself crosses the interchange boundary. Two filters matter:
// active reservations are excluded because nothing in the destination
// database would hold their lease, and a released reservation whose
// attempt is still active is excluded too, because readLogicalAttempts
// drops active attempts and the row would import as a dangling reference.
func readLogicalReservations(ctx context.Context, query Queryer) ([]domain.LogicalReservation, time.Time, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT r.id, r.issue_id, r.attempt_id, r.kind, r.display_value, r.comparison_value, r.normalized_json, r.status, r.created_at, r.released_at, r.release_reason
		FROM resource_reservations r
		JOIN issues i ON r.issue_id = i.id
		JOIN work_attempts a ON r.attempt_id = a.id
		WHERE i.archived_at IS NULL AND a.status <> 'active' AND r.status = 'released'
		ORDER BY r.created_at ASC, r.id ASC`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()

	reservations := make([]domain.LogicalReservation, 0)
	var latest time.Time
	for rows.Next() {
		var (
			id, issueID, attemptID, kind, displayValue string
			comparisonValue, normalizedJSON, status    string
			createdAtText                              string
			releasedAtText, releaseReason              sql.NullString
		)
		if err := rows.Scan(&id, &issueID, &attemptID, &kind, &displayValue, &comparisonValue, &normalizedJSON, &status, &createdAtText, &releasedAtText, &releaseReason); err != nil {
			return nil, time.Time{}, corruptLogicalProjectValue(err, "reservations")
		}
		// The storage CHECK pairs status = 'released' with both release
		// columns, so a row reaching here without them is corruption, not
		// a shape this exporter should paper over.
		if !releasedAtText.Valid || !releaseReason.Valid {
			return nil, time.Time{}, corruptLogicalProjectField(nil, "reservations", "INVALID_VALUE")
		}
		if !json.Valid([]byte(normalizedJSON)) {
			return nil, time.Time{}, corruptLogicalProjectField(nil, "normalized_json", "INVALID_JSON")
		}
		createdAt, err := parseLogicalProjectTimestamp("created_at", createdAtText)
		if err != nil {
			return nil, time.Time{}, err
		}
		releasedAt, err := parseLogicalProjectTimestamp("released_at", releasedAtText.String)
		if err != nil {
			return nil, time.Time{}, err
		}
		if createdAt.After(latest) {
			latest = createdAt
		}
		if releasedAt.After(latest) {
			latest = releasedAt
		}
		reservations = append(reservations, domain.LogicalReservation{
			ID:              id,
			IssueID:         issueID,
			AttemptID:       attemptID,
			Kind:            kind,
			DisplayValue:    displayValue,
			ComparisonValue: comparisonValue,
			NormalizedJSON:  json.RawMessage(normalizedJSON),
			Status:          status,
			CreatedAt:       formatLogicalProjectTimestamp(createdAt),
			ReleasedAt:      formatLogicalProjectTimestamp(releasedAt),
			ReleaseReason:   releaseReason.String,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return reservations, latest, nil
}

func readLogicalEvents(ctx context.Context, query Queryer) ([]domain.LogicalEvent, time.Time, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT id, issue_id, event_type, attempt_id, payload, created_at, source
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
			issueID, attemptID                            sql.NullString
			id                                            int64
			eventType, payloadText, createdAtText, source string
		)
		if err := rows.Scan(&id, &issueID, &eventType, &attemptID, &payloadText, &createdAtText, &source); err != nil {
			return nil, time.Time{}, corruptLogicalProjectValue(err, "events")
		}
		if source != domain.LogicalEventSourceIssue && source != domain.LogicalEventSourceReview {
			return nil, time.Time{}, corruptLogicalProjectField(nil, "events", "INVALID_VALUE")
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
		events = append(events, domain.LogicalEvent{SourceID: id, IssueID: nullableLogicalString(issueID), EventType: eventType, SessionID: nil, AttemptID: nullableLogicalString(attemptID), Payload: payload, CreatedAt: formatLogicalProjectTimestamp(createdAt), Source: source})
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return events, latest, nil
}

func readLogicalReviewTargets(ctx context.Context, query Queryer) ([]domain.LogicalReviewTarget, time.Time, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT id, issue_id, issue_version, latest_event_id, artifact_ids_json, purposes_json, created_at
		FROM review_targets
		WHERE issue_id IN (SELECT id FROM issues WHERE archived_at IS NULL)
		ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()

	targets := make([]domain.LogicalReviewTarget, 0)
	var latest time.Time
	for rows.Next() {
		var (
			id, issueID, artifactIDsJSON, purposesJSON, createdAtText string
			issueVersion, latestEventID                               int64
		)
		if err := rows.Scan(&id, &issueID, &issueVersion, &latestEventID, &artifactIDsJSON, &purposesJSON, &createdAtText); err != nil {
			return nil, time.Time{}, corruptLogicalProjectValue(err, "review_targets")
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
		artifactIDs, err := parseLogicalStringArray("artifact_ids", artifactIDsJSON)
		if err != nil {
			return nil, time.Time{}, err
		}
		purposes, err := parseLogicalStringArray("purposes", purposesJSON)
		if err != nil {
			return nil, time.Time{}, err
		}
		targets = append(targets, domain.LogicalReviewTarget{
			ID:            id,
			IssueID:       issueID,
			IssueVersion:  issueVersion,
			LatestEventID: latestEventID,
			ArtifactIDs:   artifactIDs,
			Purposes:      logicalPurposesForExport(purposes),
			CreatedAt:     formatLogicalProjectTimestamp(createdAt),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return targets, latest, nil
}

func readLogicalReviewRequests(ctx context.Context, query Queryer) ([]domain.LogicalReviewRequest, time.Time, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, purposes_json, status, supersedes_id, created_at, resolved_at
		FROM review_requests
		WHERE issue_id IN (SELECT id FROM issues WHERE archived_at IS NULL)
			AND status <> 'claimed'
		ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()

	requests := make([]domain.LogicalReviewRequest, 0)
	var latest time.Time
	for rows.Next() {
		var (
			id, targetID, issueID, status, artifactIDsJSON, purposesJSON, createdAtText string
			targetIssueVersion, targetEventID                                           int64
			supersedesID, resolvedAtText                                                sql.NullString
		)
		if err := rows.Scan(&id, &targetID, &issueID, &targetIssueVersion, &targetEventID, &artifactIDsJSON, &purposesJSON, &status, &supersedesID, &createdAtText, &resolvedAtText); err != nil {
			return nil, time.Time{}, corruptLogicalProjectValue(err, "review_requests")
		}
		if _, err := ids.ParseStrict(id); err != nil {
			return nil, time.Time{}, corruptLogicalProjectField(err, "id", "INVALID_ULID")
		}
		if _, err := ids.ParseStrict(targetID); err != nil {
			return nil, time.Time{}, corruptLogicalProjectField(err, "target_id", "INVALID_ULID")
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
		artifactIDs, err := parseLogicalStringArray("artifact_ids", artifactIDsJSON)
		if err != nil {
			return nil, time.Time{}, err
		}
		purposes, err := parseLogicalStringArray("purposes", purposesJSON)
		if err != nil {
			return nil, time.Time{}, err
		}
		request := domain.LogicalReviewRequest{
			ID:                 id,
			TargetID:           targetID,
			IssueID:            issueID,
			TargetIssueVersion: targetIssueVersion,
			TargetEventID:      targetEventID,
			ArtifactIDs:        artifactIDs,
			Purposes:           logicalPurposesForExport(purposes),
			Status:             status,
			SupersedesID:       nullableLogicalString(supersedesID),
			CreatedAt:          formatLogicalProjectTimestamp(createdAt),
		}
		if resolvedAtText.Valid {
			resolvedAt, err := parseLogicalProjectTimestamp("resolved_at", resolvedAtText.String)
			if err != nil {
				return nil, time.Time{}, err
			}
			if resolvedAt.After(latest) {
				latest = resolvedAt
			}
			request.ResolvedAt = ptrLogicalString(formatLogicalProjectTimestamp(resolvedAt))
		}
		requests = append(requests, request)
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return requests, latest, nil
}

func readLogicalReviewOutcomes(ctx context.Context, query Queryer) ([]domain.LogicalReviewOutcome, time.Time, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT ro.id, ro.request_id, ro.attempt_id, ro.outcome, ro.reason, ro.created_at
		FROM review_outcomes ro
		JOIN review_requests rr ON ro.request_id = rr.id
		JOIN issues i ON rr.issue_id = i.id
		WHERE i.archived_at IS NULL
		ORDER BY ro.created_at ASC, ro.id ASC`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()

	outcomes := make([]domain.LogicalReviewOutcome, 0)
	var latest time.Time
	for rows.Next() {
		var (
			id, requestID, attemptID, outcome, createdAtText string
			reason                                           sql.NullString
		)
		if err := rows.Scan(&id, &requestID, &attemptID, &outcome, &reason, &createdAtText); err != nil {
			return nil, time.Time{}, corruptLogicalProjectValue(err, "review_outcomes")
		}
		if _, err := ids.ParseStrict(id); err != nil {
			return nil, time.Time{}, corruptLogicalProjectField(err, "id", "INVALID_ULID")
		}
		if _, err := ids.ParseStrict(requestID); err != nil {
			return nil, time.Time{}, corruptLogicalProjectField(err, "request_id", "INVALID_ULID")
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
		outcomes = append(outcomes, domain.LogicalReviewOutcome{
			ID:        id,
			RequestID: requestID,
			AttemptID: attemptID,
			Outcome:   outcome,
			Reason:    nullableLogicalString(reason),
			CreatedAt: formatLogicalProjectTimestamp(createdAt),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return outcomes, latest, nil
}

func readLogicalReviewEvents(ctx context.Context, query Queryer) ([]domain.LogicalReviewEvent, time.Time, error) {
	// review_events was folded into issue_events (source='review') by
	// migration 008; request_id/target_id are no longer their own columns
	// there, so pull them back out of the payload every review event
	// already carries them in (see payloadForReviewEvent in reviews.go --
	// every review_* event type sets both unconditionally). Applies the
	// same archived-issue and active-attempt exclusions readLogicalEvents
	// uses for the unified log's issue-sourced rows.
	rows, err := query.QueryContext(ctx, `
		SELECT e.id, json_extract(e.payload, '$.request_id'), json_extract(e.payload, '$.target_id'),
			e.attempt_id, e.event_type, e.payload, e.created_at
		FROM issue_events e
		WHERE e.source = 'review'
			AND (e.issue_id IS NULL OR e.issue_id IN (SELECT id FROM issues WHERE archived_at IS NULL))
			AND (e.attempt_id IS NULL OR e.attempt_id NOT IN (SELECT id FROM work_attempts WHERE status = 'active'))
		ORDER BY e.created_at ASC, e.id ASC`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()

	events := make([]domain.LogicalReviewEvent, 0)
	var latest time.Time
	for rows.Next() {
		var (
			sourceID                       int64
			requestID, targetID, eventType string
			payloadText, createdAtText     string
			attemptIDNull                  sql.NullString
		)
		if err := rows.Scan(&sourceID, &requestID, &targetID, &attemptIDNull, &eventType, &payloadText, &createdAtText); err != nil {
			return nil, time.Time{}, corruptLogicalProjectValue(err, "review_events")
		}
		if _, err := ids.ParseStrict(requestID); err != nil {
			return nil, time.Time{}, corruptLogicalProjectField(err, "request_id", "INVALID_ULID")
		}
		if _, err := ids.ParseStrict(targetID); err != nil {
			return nil, time.Time{}, corruptLogicalProjectField(err, "target_id", "INVALID_ULID")
		}
		if attemptIDNull.Valid {
			if _, err := ids.ParseStrict(attemptIDNull.String); err != nil {
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
		events = append(events, domain.LogicalReviewEvent{
			SourceID:  sourceID,
			RequestID: requestID,
			TargetID:  targetID,
			AttemptID: nullableLogicalString(attemptIDNull),
			EventType: eventType,
			Payload:   payload,
			CreatedAt: formatLogicalProjectTimestamp(createdAt),
		})
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

// logicalPurposesJSONForImport renders the purposes column value for an
// imported review target or request: the document's normalized list when it
// carries one, otherwise the [implementation] compatibility default --
// exactly what the column DEFAULT would have written (docs/02 §17.5).
func logicalPurposesJSONForImport(purposes []string) (string, error) {
	if purposes == nil {
		purposes = domain.DefaultReviewPurposes()
	} else {
		normalized, err := domain.ValidateReviewPurposes(purposes)
		if err != nil {
			return "", err
		}
		purposes = normalized
	}
	payload, err := json.Marshal(purposes)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

// logicalPurposesForExport returns nil when purposes equals the
// [implementation] compatibility default, so the field is omitted and a
// project that never named a purpose exports exactly the document it
// exported before ISSUE-175 (see LogicalReviewTarget.Purposes).
func logicalPurposesForExport(purposes []string) []string {
	defaults := domain.DefaultReviewPurposes()
	if len(purposes) == len(defaults) {
		match := true
		for index := range purposes {
			if purposes[index] != defaults[index] {
				match = false
				break
			}
		}
		if match {
			return nil
		}
	}
	return purposes
}

// readLogicalGates exports the durable workflow-gate state (ISSUE-175 AC3).
// The attempt-owned entities (attempt snapshots, evidence, evidence events)
// follow readLogicalReservations' rule: only rows whose owning attempt
// itself crosses the boundary (non-active, unarchived issue) are exported.
// Review-target snapshots and approvals follow the review entities'
// unarchived-issue filter; approvals additionally require their approving
// attempt to be exported.
func readLogicalGates(ctx context.Context, query Queryer) (domain.LogicalGatesExtension, time.Time, error) {
	extension := domain.LogicalGatesExtension{
		Version:               domain.LogicalGatesExtensionVersion,
		Policies:              []domain.LogicalWorkflowPolicy{},
		PolicyEvents:          []domain.LogicalWorkflowPolicyEvent{},
		AttemptSnapshots:      []domain.LogicalAttemptGateSnapshot{},
		ReviewTargetSnapshots: []domain.LogicalReviewTargetGateSnapshot{},
		Evidence:              []domain.LogicalGateEvidence{},
		EvidenceEvents:        []domain.LogicalGateEvidenceEvent{},
		ReviewApprovals:       []domain.LogicalReviewApproval{},
	}
	var latest time.Time
	observe := func(moments ...time.Time) {
		for _, moment := range moments {
			if moment.After(latest) {
				latest = moment
			}
		}
	}

	rows, err := query.QueryContext(ctx, `
		SELECT id, selector_json, requirements_json, status, version, created_at, updated_at
		FROM workflow_policies
		ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return extension, time.Time{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, selectorJSON, requirementsJSON, status string
			version                                    int64
			createdAtText, updatedAtText               string
		)
		if err := rows.Scan(&id, &selectorJSON, &requirementsJSON, &status, &version, &createdAtText, &updatedAtText); err != nil {
			return extension, time.Time{}, corruptLogicalProjectValue(err, "workflow_policies")
		}
		createdAt, err := parseLogicalProjectTimestamp("created_at", createdAtText)
		if err != nil {
			return extension, time.Time{}, err
		}
		updatedAt, err := parseLogicalProjectTimestamp("updated_at", updatedAtText)
		if err != nil {
			return extension, time.Time{}, err
		}
		observe(createdAt, updatedAt)
		extension.Policies = append(extension.Policies, domain.LogicalWorkflowPolicy{
			ID: id, SelectorJSON: json.RawMessage(selectorJSON), RequirementsJSON: json.RawMessage(requirementsJSON),
			Status: status, Version: version,
			CreatedAt: formatLogicalProjectTimestamp(createdAt), UpdatedAt: formatLogicalProjectTimestamp(updatedAt),
		})
	}
	if err := rows.Err(); err != nil {
		return extension, time.Time{}, err
	}

	eventRows, err := query.QueryContext(ctx, `
		SELECT id, policy_id, event_type, session_id, prior_version, new_version, payload, created_at
		FROM workflow_policy_events
		ORDER BY id ASC`)
	if err != nil {
		return extension, time.Time{}, err
	}
	defer eventRows.Close()
	for eventRows.Next() {
		var (
			sourceID, newVersion int64
			policyID, eventType  string
			sessionID            sql.NullString
			priorVersion         sql.NullInt64
			payload              string
			createdAtText        string
		)
		if err := eventRows.Scan(&sourceID, &policyID, &eventType, &sessionID, &priorVersion, &newVersion, &payload, &createdAtText); err != nil {
			return extension, time.Time{}, corruptLogicalProjectValue(err, "workflow_policy_events")
		}
		createdAt, err := parseLogicalProjectTimestamp("created_at", createdAtText)
		if err != nil {
			return extension, time.Time{}, err
		}
		observe(createdAt)
		event := domain.LogicalWorkflowPolicyEvent{
			SourceID: sourceID, PolicyID: policyID, EventType: eventType,
			SessionID: nullableLogicalString(sessionID), NewVersion: newVersion,
			Payload: json.RawMessage(payload), CreatedAt: formatLogicalProjectTimestamp(createdAt),
		}
		if priorVersion.Valid {
			value := priorVersion.Int64
			event.PriorVersion = &value
		}
		extension.PolicyEvents = append(extension.PolicyEvents, event)
	}
	if err := eventRows.Err(); err != nil {
		return extension, time.Time{}, err
	}

	attemptSnapshotRows, err := query.QueryContext(ctx, `
		SELECT s.attempt_id, s.requirements_json, s.source_policies_json, s.fingerprint, s.issue_version, s.created_at
		FROM attempt_gate_snapshots s
		JOIN work_attempts a ON s.attempt_id = a.id
		JOIN issues i ON a.issue_id = i.id
		WHERE i.archived_at IS NULL AND a.status <> 'active'
		ORDER BY s.created_at ASC, s.attempt_id ASC`)
	if err != nil {
		return extension, time.Time{}, err
	}
	defer attemptSnapshotRows.Close()
	for attemptSnapshotRows.Next() {
		snapshot, createdAt, err := scanLogicalGateSnapshot(attemptSnapshotRows, "attempt_gate_snapshots")
		if err != nil {
			return extension, time.Time{}, err
		}
		observe(createdAt)
		extension.AttemptSnapshots = append(extension.AttemptSnapshots, domain.LogicalAttemptGateSnapshot{
			AttemptID: snapshot.ownerID, RequirementsJSON: snapshot.requirements, SourcePoliciesJSON: snapshot.sourcePolicies,
			Fingerprint: snapshot.fingerprint, IssueVersion: snapshot.issueVersion, CreatedAt: snapshot.createdAt,
		})
	}
	if err := attemptSnapshotRows.Err(); err != nil {
		return extension, time.Time{}, err
	}

	targetSnapshotRows, err := query.QueryContext(ctx, `
		SELECT s.target_id, s.requirements_json, s.source_policies_json, s.fingerprint, s.issue_version, s.created_at
		FROM review_target_gate_snapshots s
		JOIN review_targets t ON s.target_id = t.id
		WHERE t.issue_id IN (SELECT id FROM issues WHERE archived_at IS NULL)
		ORDER BY s.created_at ASC, s.target_id ASC`)
	if err != nil {
		return extension, time.Time{}, err
	}
	defer targetSnapshotRows.Close()
	for targetSnapshotRows.Next() {
		snapshot, createdAt, err := scanLogicalGateSnapshot(targetSnapshotRows, "review_target_gate_snapshots")
		if err != nil {
			return extension, time.Time{}, err
		}
		observe(createdAt)
		extension.ReviewTargetSnapshots = append(extension.ReviewTargetSnapshots, domain.LogicalReviewTargetGateSnapshot{
			TargetID: snapshot.ownerID, RequirementsJSON: snapshot.requirements, SourcePoliciesJSON: snapshot.sourcePolicies,
			Fingerprint: snapshot.fingerprint, IssueVersion: snapshot.issueVersion, CreatedAt: snapshot.createdAt,
		})
	}
	if err := targetSnapshotRows.Err(); err != nil {
		return extension, time.Time{}, err
	}

	evidenceRows, err := query.QueryContext(ctx, `
		SELECT e.id, e.attempt_id, e.issue_id, e.key, e.result, e.summary, e.details, e.artifact_ids_json, e.version, e.created_at, e.updated_at
		FROM gate_evidence e
		JOIN work_attempts a ON e.attempt_id = a.id
		JOIN issues i ON e.issue_id = i.id
		WHERE i.archived_at IS NULL AND a.status <> 'active'
		ORDER BY e.created_at ASC, e.id ASC`)
	if err != nil {
		return extension, time.Time{}, err
	}
	defer evidenceRows.Close()
	for evidenceRows.Next() {
		var (
			id, attemptID, issueID, key, result, summary string
			details                                      sql.NullString
			artifactIDsJSON                              string
			version                                      int64
			createdAtText, updatedAtText                 string
		)
		if err := evidenceRows.Scan(&id, &attemptID, &issueID, &key, &result, &summary, &details, &artifactIDsJSON, &version, &createdAtText, &updatedAtText); err != nil {
			return extension, time.Time{}, corruptLogicalProjectValue(err, "gate_evidence")
		}
		createdAt, err := parseLogicalProjectTimestamp("created_at", createdAtText)
		if err != nil {
			return extension, time.Time{}, err
		}
		updatedAt, err := parseLogicalProjectTimestamp("updated_at", updatedAtText)
		if err != nil {
			return extension, time.Time{}, err
		}
		artifactIDs, err := parseLogicalStringArray("artifact_ids", artifactIDsJSON)
		if err != nil {
			return extension, time.Time{}, err
		}
		observe(createdAt, updatedAt)
		extension.Evidence = append(extension.Evidence, domain.LogicalGateEvidence{
			ID: id, AttemptID: attemptID, IssueID: issueID, Key: key, Result: result, Summary: summary,
			Details: nullableLogicalString(details), ArtifactIDs: artifactIDs, Version: version,
			CreatedAt: formatLogicalProjectTimestamp(createdAt), UpdatedAt: formatLogicalProjectTimestamp(updatedAt),
		})
	}
	if err := evidenceRows.Err(); err != nil {
		return extension, time.Time{}, err
	}

	evidenceEventRows, err := query.QueryContext(ctx, `
		SELECT ev.id, ev.evidence_id, ev.attempt_id, ev.issue_id, ev.key, ev.event_type, ev.version, ev.payload, ev.created_at
		FROM gate_evidence_events ev
		JOIN work_attempts a ON ev.attempt_id = a.id
		JOIN issues i ON ev.issue_id = i.id
		WHERE i.archived_at IS NULL AND a.status <> 'active'
		ORDER BY ev.id ASC`)
	if err != nil {
		return extension, time.Time{}, err
	}
	defer evidenceEventRows.Close()
	for evidenceEventRows.Next() {
		var (
			sourceID, version                              int64
			evidenceID, attemptID, issueID, key, eventType string
			payload, createdAtText                         string
		)
		if err := evidenceEventRows.Scan(&sourceID, &evidenceID, &attemptID, &issueID, &key, &eventType, &version, &payload, &createdAtText); err != nil {
			return extension, time.Time{}, corruptLogicalProjectValue(err, "gate_evidence_events")
		}
		createdAt, err := parseLogicalProjectTimestamp("created_at", createdAtText)
		if err != nil {
			return extension, time.Time{}, err
		}
		observe(createdAt)
		extension.EvidenceEvents = append(extension.EvidenceEvents, domain.LogicalGateEvidenceEvent{
			SourceID: sourceID, EvidenceID: evidenceID, AttemptID: attemptID, IssueID: issueID, Key: key,
			EventType: eventType, Version: version, Payload: json.RawMessage(payload),
			CreatedAt: formatLogicalProjectTimestamp(createdAt),
		})
	}
	if err := evidenceEventRows.Err(); err != nil {
		return extension, time.Time{}, err
	}

	approvalRows, err := query.QueryContext(ctx, `
		SELECT ap.id, ap.issue_id, ap.target_id, ap.request_id, ap.attempt_id, ap.purpose, ap.target_issue_version, ap.target_event_id, ap.version, ap.created_at
		FROM review_approvals ap
		JOIN work_attempts a ON ap.attempt_id = a.id
		JOIN issues i ON ap.issue_id = i.id
		WHERE i.archived_at IS NULL AND a.status <> 'active'
		ORDER BY ap.created_at ASC, ap.id ASC`)
	if err != nil {
		return extension, time.Time{}, err
	}
	defer approvalRows.Close()
	for approvalRows.Next() {
		var (
			id, issueID, targetID, requestID, attemptID, purpose string
			targetIssueVersion, targetEventID, version           int64
			createdAtText                                        string
		)
		if err := approvalRows.Scan(&id, &issueID, &targetID, &requestID, &attemptID, &purpose, &targetIssueVersion, &targetEventID, &version, &createdAtText); err != nil {
			return extension, time.Time{}, corruptLogicalProjectValue(err, "review_approvals")
		}
		createdAt, err := parseLogicalProjectTimestamp("created_at", createdAtText)
		if err != nil {
			return extension, time.Time{}, err
		}
		observe(createdAt)
		extension.ReviewApprovals = append(extension.ReviewApprovals, domain.LogicalReviewApproval{
			ID: id, IssueID: issueID, TargetID: targetID, RequestID: requestID, AttemptID: attemptID,
			Purpose: purpose, TargetIssueVersion: targetIssueVersion, TargetEventID: targetEventID,
			Version: version, CreatedAt: formatLogicalProjectTimestamp(createdAt),
		})
	}
	if err := approvalRows.Err(); err != nil {
		return extension, time.Time{}, err
	}

	return extension, latest, nil
}

// scannedGateSnapshot carries one scanned snapshot row shared by the two
// snapshot tables.
type scannedGateSnapshot struct {
	ownerID        string
	requirements   json.RawMessage
	sourcePolicies json.RawMessage
	fingerprint    string
	issueVersion   int64
	createdAt      string
}

func scanLogicalGateSnapshot(rows *sql.Rows, table string) (scannedGateSnapshot, time.Time, error) {
	var (
		ownerID, requirementsJSON, sourcePoliciesJSON, fingerprint string
		issueVersion                                               int64
		createdAtText                                              string
	)
	if err := rows.Scan(&ownerID, &requirementsJSON, &sourcePoliciesJSON, &fingerprint, &issueVersion, &createdAtText); err != nil {
		return scannedGateSnapshot{}, time.Time{}, corruptLogicalProjectValue(err, table)
	}
	createdAt, err := parseLogicalProjectTimestamp("created_at", createdAtText)
	if err != nil {
		return scannedGateSnapshot{}, time.Time{}, err
	}
	return scannedGateSnapshot{
		ownerID:        ownerID,
		requirements:   json.RawMessage(requirementsJSON),
		sourcePolicies: json.RawMessage(sourcePoliciesJSON),
		fingerprint:    fingerprint,
		issueVersion:   issueVersion,
		createdAt:      formatLogicalProjectTimestamp(createdAt),
	}, createdAt, nil
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
	parsed, err := parseStorageTime(value)
	if err != nil {
		return time.Time{}, corruptLogicalProjectField(err, field, "INVALID_TIMESTAMP")
	}
	return parsed, nil
}

// formatLogicalProjectTimestamp renders a timestamp for the logical
// interchange document (export JSON), not a SQLite storage column -- it
// deliberately keeps the trimmed time.RFC3339Nano form rather than
// formatStorageTime's fixed width. The document isn't compared via SQL
// memcmp, and widening it would be an interchange format-version change
// (docs/07), not a fix for the SQL comparison bug this file's storage
// writes need.
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
	parsed, err := parseStorageTime(value)
	if err != nil {
		return time.Time{}, corruptProjectTimestamp(err, field)
	}
	return parsed, nil
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
