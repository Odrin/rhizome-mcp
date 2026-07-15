package sqlite

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

// WorkContextRepository reads the compact issue work-context projection.
type WorkContextRepository struct {
	db *DB
}

// NewWorkContextRepository returns a work-context repository backed by database.
func NewWorkContextRepository(database *DB) (*WorkContextRepository, error) {
	if database == nil {
		return nil, domain.NewError(domain.CodeStorageConfiguration, "work context database is required", false)
	}
	return &WorkContextRepository{db: database}, nil
}

// GetWorkContext reads a compact work context from a single SQLite snapshot.
func (repository *WorkContextRepository) GetWorkContext(ctx context.Context, command ports.GetWorkContextCommand) (domain.WorkContext, error) {
	input, err := command.Input.Validate()
	if err != nil {
		return domain.WorkContext{}, err
	}
	if command.Now.IsZero() {
		return domain.WorkContext{}, domain.NewError(domain.CodeInvalidArgument, "work context command timestamp is required", false)
	}
	now := command.Now.UTC()

	result := domain.NewEmptyWorkContext()
	err = repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		resolvedIssueID, err := resolveActivityIssueID(ctx, query, input.IssueID)
		if err != nil {
			return err
		}

		targetIssue, err := loadIssueForMutation(ctx, query, domain.IssueIdentifier{Kind: domain.IssueIdentifierInternalID, Value: resolvedIssueID})
		if err != nil {
			return err
		}
		result.Issue, err = buildWorkContextIssue(ctx, query, targetIssue, now)
		if err != nil {
			return err
		}

		if includesWorkContextSection(input.Include, domain.WorkContextIncludeParentEpic) && targetIssue.ParentID != nil {
			parsedParentID, err := ids.ParseStrict(*targetIssue.ParentID)
			if err != nil {
				return domain.NewError(domain.CodeStorageCorrupt, "stored issue projection is invalid", false)
			}
			parent, err := loadStoredIssueProjection(ctx, query, parsedParentID.String())
			if err != nil {
				return err
			}
			if parent.Type != domain.TypeEpic {
				return domain.NewError(domain.CodeStorageCorrupt, "stored issue projection is invalid", false)
			}
			parentIssue, err := buildWorkContextIssue(ctx, query, parent, now)
			if err != nil {
				return err
			}
			result.ParentEpic = &parentIssue
		}

		if includesWorkContextSection(input.Include, domain.WorkContextIncludeProjectInstructions) {
			project, err := readProjectRow(ctx, query)
			if err != nil {
				return err
			}
			if project.Instructions != nil {
				instructions := *project.Instructions
				result.ProjectInstructions = &instructions
			}
		}

		if includesWorkContextSection(input.Include, domain.WorkContextIncludeRelations) {
			result.Relations, err = loadWorkContextRelations(ctx, query, resolvedIssueID)
			if err != nil {
				return err
			}
		}

		if includesWorkContextSection(input.Include, domain.WorkContextIncludeRelatedIssueSummaries) {
			summaries, truncated, err := loadWorkContextRelatedIssueSummaries(ctx, query, resolvedIssueID, input.Limits[domain.WorkContextIncludeRelatedIssueSummaries], now)
			if err != nil {
				return err
			}
			result.RelatedIssueSummaries = summaries
			if truncated {
				result.Truncated = true
				result.TruncatedSections = appendWorkContextSection(result.TruncatedSections, domain.WorkContextIncludeRelatedIssueSummaries)
			}
		}

		result.Blockers, err = loadWorkContextBlockers(ctx, query, resolvedIssueID, now)
		if err != nil {
			return err
		}

		result.Decisions, err = loadWorkContextDecisions(ctx, query, resolvedIssueID)
		if err != nil {
			return err
		}

		result.PreviousAttempt, err = loadWorkContextPreviousAttempt(ctx, query, resolvedIssueID)
		if err != nil {
			return err
		}

		result.Checkpoint, err = loadWorkContextCheckpoint(ctx, query, resolvedIssueID)
		if err != nil {
			return err
		}

		result.Warnings, err = loadWorkContextWarnings(ctx, query, resolvedIssueID)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return domain.WorkContext{}, err
	}
	return domain.CloneWorkContext(result), nil
}

func includesWorkContextSection(values []domain.WorkContextInclude, wanted domain.WorkContextInclude) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func appendWorkContextSection(values []domain.WorkContextInclude, wanted domain.WorkContextInclude) []domain.WorkContextInclude {
	if includesWorkContextSection(values, wanted) {
		return values
	}
	if values == nil {
		values = []domain.WorkContextInclude{}
	}
	return append(values, wanted)
}

func loadWorkContextRelations(ctx context.Context, query Queryer, issueID string) ([]domain.IssueRelation, error) {
	rows, err := query.QueryContext(ctx, `SELECT relation.id, relation.source_issue_id, relation.target_issue_id, relation.type, relation.created_at, source.sequence_no AS source_sequence_no, target.sequence_no AS target_sequence_no
	FROM issue_relations AS relation
	LEFT JOIN issues AS source ON source.id = relation.source_issue_id
	LEFT JOIN issues AS target ON target.id = relation.target_issue_id
	WHERE relation.source_issue_id = ? OR relation.target_issue_id = ?
	ORDER BY relation.type ASC, source.sequence_no ASC, relation.source_issue_id ASC, target.sequence_no ASC, relation.target_issue_id ASC, relation.id ASC`, issueID, issueID)
	if err != nil {
		return nil, workContextCorrupt(err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.IssueRelation, 0)
	for rows.Next() {
		var relationID, sourceIssueID, targetIssueID, relationType, createdAt string
		var sourceSequenceNo, targetSequenceNo sql.NullInt64
		if err := rows.Scan(&relationID, &sourceIssueID, &targetIssueID, &relationType, &createdAt, &sourceSequenceNo, &targetSequenceNo); err != nil {
			return nil, workContextCorrupt(err)
		}
		if !sourceSequenceNo.Valid || !targetSequenceNo.Valid {
			return nil, domain.NewError(domain.CodeStorageCorrupt, "stored relation endpoint is invalid", false)
		}
		if _, err := ids.ParseStrict(relationID); err != nil {
			return nil, domain.NewError(domain.CodeStorageCorrupt, "stored relation projection is invalid", false, domain.Detail{Field: "id", Code: "INVALID_ULID"})
		}
		if _, err := ids.ParseStrict(sourceIssueID); err != nil {
			return nil, domain.NewError(domain.CodeStorageCorrupt, "stored relation endpoint is invalid", false, domain.Detail{Field: "source_issue_id", Code: "INVALID_ULID"})
		}
		if _, err := ids.ParseStrict(targetIssueID); err != nil {
			return nil, domain.NewError(domain.CodeStorageCorrupt, "stored relation endpoint is invalid", false, domain.Detail{Field: "target_issue_id", Code: "INVALID_ULID"})
		}
		parsedType, err := domain.ParseRelationType(relationType)
		if err != nil {
			return nil, domain.WrapError(err, domain.CodeStorageCorrupt, "stored relation projection is invalid", false)
		}
		createdAtTime, err := parseIssueTimestamp("created_at", createdAt)
		if err != nil {
			return nil, domain.WrapError(err, domain.CodeStorageCorrupt, "stored relation projection is invalid", false)
		}
		result = append(result, domain.IssueRelation{ID: relationID, SourceIssueID: sourceIssueID, TargetIssueID: targetIssueID, Type: parsedType, CreatedAt: createdAtTime})
	}
	if err := rows.Err(); err != nil {
		return nil, workContextCorrupt(err)
	}
	return result, nil
}

func loadWorkContextRelatedIssueSummaries(ctx context.Context, query Queryer, issueID string, limit int, now time.Time) ([]domain.WorkContextIssue, bool, error) {
	rows, err := query.QueryContext(ctx, `SELECT endpoint_ids.issue_id, related.id
	FROM (
		SELECT relation.source_issue_id AS issue_id
		FROM issue_relations AS relation
		WHERE relation.source_issue_id = ? OR relation.target_issue_id = ?
		UNION
		SELECT relation.target_issue_id AS issue_id
		FROM issue_relations AS relation
		WHERE relation.source_issue_id = ? OR relation.target_issue_id = ?
	) AS endpoint_ids
	LEFT JOIN issues AS related ON related.id = endpoint_ids.issue_id
	WHERE endpoint_ids.issue_id != ?
	ORDER BY related.sequence_no ASC, related.id ASC
	LIMIT ?`, issueID, issueID, issueID, issueID, issueID, limit+1)
	if err != nil {
		return nil, false, workContextCorrupt(err)
	}
	defer func() { _ = rows.Close() }()

	relatedIssueIDs := make([]string, 0, limit+1)
	for rows.Next() {
		var endpointIssueID string
		var relatedIssueID sql.NullString
		if err := rows.Scan(&endpointIssueID, &relatedIssueID); err != nil {
			return nil, false, workContextCorrupt(err)
		}
		_ = endpointIssueID
		if !relatedIssueID.Valid {
			return nil, false, domain.NewError(domain.CodeStorageCorrupt, "stored relation endpoint is invalid", false)
		}
		relatedIssueIDs = append(relatedIssueIDs, relatedIssueID.String)
	}
	if err := rows.Err(); err != nil {
		return nil, false, workContextCorrupt(err)
	}

	truncated := false
	if len(relatedIssueIDs) > limit {
		relatedIssueIDs = relatedIssueIDs[:limit]
		truncated = true
	}

	result := make([]domain.WorkContextIssue, 0, len(relatedIssueIDs))
	for _, relatedIssueID := range relatedIssueIDs {
		issue, err := loadStoredIssueProjection(ctx, query, relatedIssueID)
		if err != nil {
			return nil, false, err
		}
		issueSummary, err := buildWorkContextIssue(ctx, query, issue, now)
		if err != nil {
			return nil, false, err
		}
		result = append(result, issueSummary)
	}
	return result, truncated, nil
}

func buildWorkContextIssue(ctx context.Context, query Queryer, issue domain.Issue, now time.Time) (domain.WorkContextIssue, error) {
	active, err := hasActiveAttempt(ctx, query, issue.ID, now)
	if err != nil {
		return domain.WorkContextIssue{}, err
	}
	status, err := domain.EffectiveStatusFor(issue.Status, active)
	if err != nil {
		return domain.WorkContextIssue{}, err
	}
	count, blocked, err := loadIssueBlockerState(ctx, query, issue.ID)
	if err != nil {
		return domain.WorkContextIssue{}, err
	}
	return domain.WorkContextIssue{
		ID:                     issue.ID,
		DisplayID:              issue.DisplayID,
		Title:                  issue.Title,
		Description:            copyOptionalString(issue.Description),
		AcceptanceCriteria:     copyOptionalString(issue.AcceptanceCriteria),
		EffectiveStatus:        status,
		UnresolvedBlockerCount: count,
		IsBlocked:              blocked,
	}, nil
}

func loadIssueBlockerState(ctx context.Context, query Queryer, issueID string) (int64, bool, error) {
	var unresolvedBlockerCount int64
	err := query.QueryRowContext(ctx, `SELECT `+issueUnresolvedBlockerCountSQL+` FROM issues WHERE id = ?`, issueID).Scan(&unresolvedBlockerCount)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, false, domain.NewError(domain.CodeStorageCorrupt, "stored issue projection is invalid", false)
		}
		return 0, false, workContextCorrupt(err)
	}
	var isBlocked int64
	err = query.QueryRowContext(ctx, `SELECT `+issueBlockedSQL+` FROM issues WHERE id = ?`, issueID).Scan(&isBlocked)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, false, domain.NewError(domain.CodeStorageCorrupt, "stored issue projection is invalid", false)
		}
		return 0, false, workContextCorrupt(err)
	}
	return unresolvedBlockerCount, isBlocked == 1, nil
}

func hasActiveAttempt(ctx context.Context, query Queryer, issueID string, now time.Time) (bool, error) {
	var attemptID string
	err := query.QueryRowContext(ctx, `SELECT id FROM work_attempts WHERE issue_id = ? AND status = 'active' AND lease_expires_at > ? LIMIT 1`, issueID, now.UTC().Format(time.RFC3339Nano)).Scan(&attemptID)
	if err == nil {
		return true, nil
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	return false, workContextCorrupt(err)
}

func loadWorkContextBlockers(ctx context.Context, query Queryer, issueID string, now time.Time) ([]domain.WorkContextIssue, error) {
	rows, err := query.QueryContext(ctx, `SELECT source_issue_id FROM issue_relations WHERE target_issue_id = ? AND type = 'blocks' ORDER BY source_issue_id ASC`, issueID)
	if err != nil {
		return nil, workContextCorrupt(err)
	}
	defer func() { _ = rows.Close() }()

	type blockerIssue struct {
		issue      domain.WorkContextIssue
		sequenceNo int64
		id         string
	}
	blockers := make([]blockerIssue, 0)
	for rows.Next() {
		var sourceIssueID string
		if err := rows.Scan(&sourceIssueID); err != nil {
			return nil, workContextCorrupt(err)
		}
		if _, err := ids.ParseStrict(sourceIssueID); err != nil {
			return nil, domain.NewError(domain.CodeStorageCorrupt, "stored relation endpoint is invalid", false,
				domain.Detail{Field: "source_issue_id", Code: "INVALID_ULID"})
		}
		issue, sequenceNo, err := loadWorkContextBlockerSource(ctx, query, sourceIssueID, now)
		if err != nil {
			return nil, err
		}
		if issue == nil {
			continue
		}
		blockers = append(blockers, blockerIssue{issue: *issue, sequenceNo: sequenceNo, id: sourceIssueID})
	}
	if err := rows.Err(); err != nil {
		return nil, workContextCorrupt(err)
	}

	sort.Slice(blockers, func(i, j int) bool {
		if blockers[i].sequenceNo != blockers[j].sequenceNo {
			return blockers[i].sequenceNo < blockers[j].sequenceNo
		}
		return blockers[i].id < blockers[j].id
	})

	result := make([]domain.WorkContextIssue, 0, len(blockers))
	for _, blocker := range blockers {
		result = append(result, blocker.issue)
	}
	return result, nil
}

func loadWorkContextBlockerSource(ctx context.Context, query Queryer, issueID string, now time.Time) (*domain.WorkContextIssue, int64, error) {
	issue, err := loadStoredIssueProjection(ctx, query, issueID)
	if err != nil {
		return nil, 0, err
	}
	if issue.ArchivedAt != nil || issue.Status == domain.StatusDone || issue.Status == domain.StatusCancelled {
		return nil, 0, nil
	}
	projection, err := buildWorkContextIssue(ctx, query, issue, now)
	if err != nil {
		return nil, 0, err
	}
	return &projection, issue.SequenceNo, nil
}

func loadStoredIssueProjection(ctx context.Context, query Queryer, issueID string) (domain.Issue, error) {
	row := query.QueryRowContext(ctx, issueProjectionSelect+" WHERE id = ?", issueID)
	issue, err := scanIssueProjection(row)
	if err == sql.ErrNoRows {
		return domain.Issue{}, domain.NewError(domain.CodeStorageCorrupt, "stored relation endpoint is invalid", false)
	}
	if err != nil {
		return domain.Issue{}, workContextCorrupt(err)
	}
	return issue, nil
}

func loadWorkContextDecisions(ctx context.Context, query Queryer, issueID string) ([]domain.WorkContextDecisionSummary, error) {
	rows, err := query.QueryContext(ctx, `SELECT id, issue_id, title, summary, content, status, supersedes_id, created_by_session_id, created_at FROM decisions WHERE issue_id = ? AND status = 'active' ORDER BY created_at DESC, id ASC`, issueID)
	if err != nil {
		return nil, workContextCorrupt(err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]domain.WorkContextDecisionSummary, 0)
	for rows.Next() {
		decision, err := scanActivityDecision(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, domain.WorkContextDecisionSummary{
			ID:        decision.ID,
			Title:     decision.Title,
			Summary:   decision.Summary,
			Status:    decision.Status,
			CreatedAt: decision.CreatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, workContextCorrupt(err)
	}
	return result, nil
}

func loadWorkContextPreviousAttempt(ctx context.Context, query Queryer, issueID string) (*domain.WorkContextAttemptSummary, error) {
	row := query.QueryRowContext(ctx, `SELECT id, issue_id, session_id, agent_label, kind, status, issue_version_at_start, context_event_id_at_start, lease_expires_at, started_at, last_heartbeat_at, finished_at, result_summary, next_steps_json, verification_json, failure_reason_code, interruption_reason_code, reason_details FROM work_attempts WHERE issue_id = ? AND status IN ('completed', 'failed', 'interrupted', 'expired') ORDER BY finished_at DESC, id ASC LIMIT 1`, issueID)
	attempt, err := scanActivityAttempt(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &domain.WorkContextAttemptSummary{
		ID:            attempt.ID,
		Kind:          attempt.Kind,
		Status:        attempt.Status,
		FinishedAt:    attempt.FinishedAt,
		ResultSummary: attempt.ResultSummary,
		NextSteps:     attempt.NextSteps,
	}, nil
}

func loadWorkContextCheckpoint(ctx context.Context, query Queryer, issueID string) (*domain.AttemptNote, error) {
	row := query.QueryRowContext(ctx, `SELECT attempt_notes.id, attempt_notes.attempt_id, attempt_notes.kind, attempt_notes.content, attempt_notes.next_steps_json, attempt_notes.important, attempt_notes.created_at FROM attempt_notes JOIN work_attempts ON work_attempts.id = attempt_notes.attempt_id WHERE work_attempts.issue_id = ? AND attempt_notes.kind = 'checkpoint' ORDER BY attempt_notes.created_at DESC, attempt_notes.id ASC LIMIT 1`, issueID)
	note, err := scanActivityAttemptNote(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &domain.AttemptNote{ID: note.ID, AttemptID: note.AttemptID, Kind: note.Kind, Content: note.Content, NextSteps: note.NextSteps, Important: note.Important, CreatedAt: note.CreatedAt}, nil
}

func loadWorkContextWarnings(ctx context.Context, query Queryer, issueID string) ([]string, error) {
	rows, err := query.QueryContext(ctx, `SELECT id, issue_id, session_id, agent_label, kind, status, issue_version_at_start, context_event_id_at_start, lease_expires_at, started_at, last_heartbeat_at, finished_at, result_summary, next_steps_json, verification_json, failure_reason_code, interruption_reason_code, reason_details FROM work_attempts WHERE issue_id = ? AND status IN ('completed', 'failed', 'interrupted', 'expired', 'cancelled') ORDER BY finished_at DESC, id ASC LIMIT 3`, issueID)
	if err != nil {
		return nil, workContextCorrupt(err)
	}
	defer func() { _ = rows.Close() }()

	attempts := make([]domain.WorkAttempt, 0, 3)
	for rows.Next() {
		attempt, err := scanActivityAttempt(rows)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, workContextCorrupt(err)
	}

	if len(attempts) < 3 {
		return []string{}, nil
	}
	for _, attempt := range attempts {
		if attempt.Status != domain.AttemptStatusFailed && attempt.Status != domain.AttemptStatusExpired {
			return []string{}, nil
		}
	}
	return []string{"REPEATED_ATTEMPT_FAILURES"}, nil
}

func workContextCorrupt(cause error) error {
	return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored work context projection is invalid", false)
}

var _ ports.WorkContextRepository = (*WorkContextRepository)(nil)
