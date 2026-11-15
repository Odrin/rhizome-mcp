package sqlite

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

// BoardSnapshotRepository loads full BoardService and IssueDetailService
// aggregates inside single SQLite read snapshots so sections never tear across
// concurrent writers and active-attempt gates never pay O(N) read transactions.
type BoardSnapshotRepository struct {
	db                    *DB
	afterFirstReadForTest func()
}

// NewBoardSnapshotRepository returns a single-snapshot board and issue-detail
// loader backed by database.
func NewBoardSnapshotRepository(database *DB) (*BoardSnapshotRepository, error) {
	if database == nil {
		return nil, domain.NewError(domain.CodeStorageConfiguration, "board snapshot database is required", false)
	}
	return &BoardSnapshotRepository{db: database}, nil
}

// LoadBoardSnapshot loads every bounded section of domain.BoardResult inside a
// single SQLite read snapshot anchored at now.
func (repository *BoardSnapshotRepository) LoadBoardSnapshot(ctx context.Context, now time.Time) (domain.BoardResult, error) {
	if repository == nil || repository.db == nil {
		return domain.BoardResult{}, domain.NewError(domain.CodeStorageConfiguration, "board snapshot database is required", false)
	}
	now = now.UTC()

	var result domain.BoardResult
	err := repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		statusCounts, err := countIssuesByEffectiveStatus(ctx, query, now)
		if err != nil {
			return err
		}
		if repository.afterFirstReadForTest != nil {
			repository.afterFirstReadForTest()
		}

		blockedIssues, hasMoreBlocked, err := loadBoardBlockedIssues(ctx, query, now)
		if err != nil {
			return err
		}

		activeAttempts, err := listActiveAttemptsSnapshot(ctx, query, now, domain.MaxBoardCollectionLimit)
		if err != nil {
			return err
		}

		reservationRows, hasMoreReservations, err := loadReservationsSnapshot(ctx, query, "status = 'active'", nil, domain.MaxBoardCollectionLimit)
		if err != nil {
			return err
		}
		activeReservations := domain.FilterReservationsByActiveAttempts(reservationRows, activeAttempts.Items)

		attemptGates, err := loadAttemptGatesSnapshot(ctx, query, activeAttempts.Items, now)
		if err != nil {
			return err
		}

		reviewRequests, hasMoreReviews, err := loadOpenReviewRequestsSnapshot(ctx, query, domain.MaxBoardCollectionLimit)
		if err != nil {
			return err
		}

		planningGraph, err := loadBoardPlanningGraphSnapshot(ctx, query, now)
		if err != nil {
			return err
		}

		result = domain.BoardResult{
			GeneratedAt:        now,
			StatusCounts:       statusCounts,
			ActiveAttempts:     activeAttempts.Items,
			AttemptGates:       attemptGates,
			ActiveReservations: activeReservations,
			BlockedIssues:      blockedIssues,
			ReviewRequests:     reviewRequests,
			PlanningGraph:      planningGraph,
			Truncation: domain.BoardTruncation{
				BlockedIssues:      hasMoreBlocked,
				ActiveAttempts:     activeAttempts.HasMore,
				ActiveReservations: hasMoreReservations,
				ReviewRequests:     hasMoreReviews,
			},
		}
		return nil
	})
	if err != nil {
		return domain.BoardResult{}, err
	}
	return result, nil
}

// LoadIssueDetailSnapshot loads every bounded section of domain.IssueDetail
// inside a single SQLite read snapshot anchored at command.Now.
func (repository *BoardSnapshotRepository) LoadIssueDetailSnapshot(ctx context.Context, command ports.LoadIssueDetailSnapshotCommand) (domain.IssueDetail, error) {
	if repository == nil || repository.db == nil {
		return domain.IssueDetail{}, domain.NewError(domain.CodeStorageConfiguration, "board snapshot database is required", false)
	}
	if command.Identifier.Kind != domain.IssueIdentifierInternalID && command.Identifier.Kind != domain.IssueIdentifierDisplayID {
		return domain.IssueDetail{}, domain.NewError(
			domain.CodeInvalidArgument,
			"issue identifier is invalid",
			false,
			domain.Detail{Field: "issue_id", Code: "INVALID_IDENTIFIER"},
		)
	}
	now := command.Now.UTC()

	var detail domain.IssueDetail
	err := repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		issue, err := loadIssueByIdentifier(ctx, query, command.Identifier, repository.afterFirstReadForTest)
		if err != nil {
			return err
		}

		graph, err := loadIssueDetailGraphSnapshot(ctx, query, issue, now)
		if err != nil {
			return err
		}

		activityInput, err := validateActivityInput(domain.GetIssueActivityInput{
			IssueID: issue.ID,
			Limit:   20,
			Order:   domain.ActivityOrderNewestFirst,
		})
		if err != nil {
			return err
		}
		activity, err := loadIssueActivityFromQuery(ctx, query, issue.ID, activityInput, nil)
		if err != nil {
			return err
		}

		reservationRows, hasMoreReservations, err := loadReservationsSnapshot(ctx, query, "issue_id = ?", []any{issue.ID}, domain.DefaultReservationHistoryLimit)
		if err != nil {
			return err
		}

		gates, err := buildWorkContextGateSummary(ctx, query, issue.ID, issue, now)
		if err != nil {
			return err
		}

		assembled, err := domain.AssembleIssueDetail(issue, graph, activity, reservationRows, hasMoreReservations, gates)
		if err != nil {
			return err
		}
		detail = assembled
		return nil
	})
	if err != nil {
		return domain.IssueDetail{}, err
	}
	return detail, nil
}

func loadBoardBlockedIssues(ctx context.Context, query Queryer, now time.Time) ([]domain.IssueProjection, bool, error) {
	statement := issueListProjectionSelect(now) +
		` WHERE archived_at IS NULL AND (` + issueBlockedSQL + `) = 1
		ORDER BY priority_rank DESC, is_claimable DESC, sequence_no ASC
		LIMIT ?`
	rows, err := query.QueryContext(ctx, statement, domain.MaxBoardCollectionLimit+1)
	if err != nil {
		return nil, false, err
	}
	items := make([]domain.IssueProjection, 0, domain.MaxBoardCollectionLimit)
	for rows.Next() {
		item, err := scanIssueListProjection(rows)
		if err != nil {
			rows.Close()
			return nil, false, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, false, err
	}
	// Release the cursor before loadIssueListLabels reuses this single snapshot conn.
	if err := rows.Close(); err != nil {
		return nil, false, err
	}

	hasMore := len(items) > domain.MaxBoardCollectionLimit
	if hasMore {
		items = items[:domain.MaxBoardCollectionLimit]
	}
	if err := loadIssueListLabels(ctx, query, items); err != nil {
		return nil, false, err
	}
	return items, hasMore, nil
}

func loadReservationsSnapshot(ctx context.Context, query Queryer, where string, args []any, limit int) ([]domain.Reservation, bool, error) {
	statement := `SELECT ` + reservationColumns + ` FROM resource_reservations WHERE ` + where +
		` ORDER BY created_at DESC, id DESC LIMIT ?`
	queryArgs := append(slices.Clone(args), limit+1)
	rows, err := query.QueryContext(ctx, statement, queryArgs...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	items := make([]domain.Reservation, 0, limit)
	for rows.Next() {
		row, err := scanReservationRow(rows)
		if err != nil {
			return nil, false, err
		}
		items = append(items, row.toDomain())
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

func loadAttemptGatesSnapshot(ctx context.Context, query Queryer, activeAttempts []domain.ActiveAttemptSummary, now time.Time) ([]domain.AttemptGateProgress, error) {
	if len(activeAttempts) == 0 {
		return []domain.AttemptGateProgress{}, nil
	}

	uniqueIssueIDs := make([]string, 0, len(activeAttempts))
	seen := make(map[string]struct{}, len(activeAttempts))
	for _, attempt := range activeAttempts {
		if _, exists := seen[attempt.IssueID]; !exists {
			seen[attempt.IssueID] = struct{}{}
			uniqueIssueIDs = append(uniqueIssueIDs, attempt.IssueID)
		}
	}

	args := make([]any, len(uniqueIssueIDs))
	for index, issueID := range uniqueIssueIDs {
		args[index] = issueID
	}
	rows, err := query.QueryContext(
		ctx,
		`SELECT id, type, acceptance_criteria FROM issues WHERE id IN (`+graphPlaceholders(len(uniqueIssueIDs))+`)`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	issuesByID := make(map[string]domain.Issue, len(uniqueIssueIDs))
	for rows.Next() {
		var (
			id, issueType      string
			acceptanceCriteria sql.NullString
		)
		if err := rows.Scan(&id, &issueType, &acceptanceCriteria); err != nil {
			rows.Close()
			return nil, corruptIssueProjection(err)
		}
		parsedType, err := domain.ParseType(issueType)
		if err != nil {
			rows.Close()
			return nil, corruptIssueProjection(err)
		}
		issuesByID[id] = domain.Issue{
			ID:                 id,
			Type:               parsedType,
			AcceptanceCriteria: nullableStringScan(acceptanceCriteria),
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	// Close rows before buildWorkContextGateSummary executes gate queries on query.
	if err := rows.Close(); err != nil {
		return nil, err
	}

	summariesByIssueID := make(map[string]domain.WorkContextGateSummary, len(uniqueIssueIDs))
	for _, issueID := range uniqueIssueIDs {
		issue, ok := issuesByID[issueID]
		if !ok {
			return nil, domain.NewError(domain.CodeIssueNotFound, "issue not found", false)
		}
		summary, err := buildWorkContextGateSummary(ctx, query, issueID, issue, now)
		if err != nil {
			return nil, err
		}
		summariesByIssueID[issueID] = summary
	}

	attemptGates := make([]domain.AttemptGateProgress, 0, len(activeAttempts))
	for _, attempt := range activeAttempts {
		attemptGates = append(attemptGates, domain.AttemptGateProgress{
			AttemptID:      attempt.AttemptID,
			IssueID:        attempt.IssueID,
			IssueDisplayID: attempt.IssueDisplayID,
			Gates:          cloneWorkContextGateSummary(summariesByIssueID[attempt.IssueID]),
		})
	}
	return attemptGates, nil
}

func cloneWorkContextGateSummary(summary domain.WorkContextGateSummary) domain.WorkContextGateSummary {
	cloned := summary
	if summary.SnapshotFingerprint != nil {
		fingerprint := *summary.SnapshotFingerprint
		cloned.SnapshotFingerprint = &fingerprint
	}
	if summary.Unmet != nil {
		cloned.Unmet = slices.Clone(summary.Unmet)
	}
	if summary.NextActions != nil {
		cloned.NextActions = slices.Clone(summary.NextActions)
	}
	return cloned
}

func loadOpenReviewRequestsSnapshot(ctx context.Context, query Queryer, limit int) ([]domain.ReviewRequest, bool, error) {
	rows, err := query.QueryContext(ctx, `SELECT `+reviewRequestColumnsQualified+`
		FROM review_requests
		WHERE review_requests.status = 'open'
		ORDER BY review_requests.created_at DESC, review_requests.id DESC
		LIMIT ?`, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	items := make([]domain.ReviewRequest, 0, limit)
	for rows.Next() {
		request, err := scanReviewRequestRow(rows)
		if err != nil {
			return nil, false, err
		}
		items = append(items, request)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

func loadBoardPlanningGraphSnapshot(ctx context.Context, query Queryer, now time.Time) (domain.GraphResult, error) {
	includeTerminal := false
	normalized, err := (domain.GetPlanningGraphInput{IncludeTerminal: &includeTerminal}).Validate()
	if err != nil {
		return domain.GraphResult{}, err
	}
	traversal := domain.GraphTraversal{
		Depth:             *normalized.Depth,
		MaxNodes:          *normalized.MaxNodes,
		Direction:         domain.GraphDirectionBoth,
		RelationTypes:     []domain.RelationType{domain.RelationTypeBlocks},
		IncludeHierarchy:  true,
		IncludeTerminal:   *normalized.IncludeTerminal,
		ExcludeReview:     !*normalized.IncludeReview,
		PreferNonTerminal: true,
	}
	snapshot := domain.GraphSnapshot{
		Nodes:            []domain.IssueProjection{},
		Edges:            []domain.GraphEdge{},
		TopLevelIssueIDs: []string{},
	}
	if err := loadSelectedGraph(ctx, query, ports.LoadGraphCommand{
		Now:       now,
		Traversal: &traversal,
	}, graphProjectionSQL(now), &snapshot); err != nil {
		return domain.GraphResult{}, err
	}
	if snapshot.SelectedResult != nil {
		return *snapshot.SelectedResult, nil
	}
	traversal.RootIssueIDs = snapshot.TopLevelIssueIDs
	return domain.BuildGraph(snapshot, traversal), nil
}

func loadIssueDetailGraphSnapshot(ctx context.Context, query Queryer, issue domain.Issue, now time.Time) (domain.GraphResult, error) {
	if issue.ArchivedAt != nil {
		// Archived issues cannot root a graph traversal (GetIssueGraph returns
		// CodeIssueArchived), so skip graph queries and preserve ISSUE-341.
		return domain.GraphResult{
			RootIssueID: &issue.ID,
			Nodes:       []domain.IssueProjection{},
			Edges:       []domain.GraphEdge{},
		}, nil
	}

	normalized, err := (domain.GetIssueGraphInput{
		RootIssueID: issue.ID,
		Direction:   domain.GraphDirectionBoth,
		View:        "compact",
	}).Validate()
	if err != nil {
		return domain.GraphResult{}, err
	}
	traversal := domain.GraphTraversal{
		Depth:            *normalized.Depth,
		MaxNodes:         *normalized.MaxNodes,
		Direction:        normalized.Direction,
		RelationTypes:    normalized.RelationTypes,
		IncludeHierarchy: *normalized.IncludeHierarchy,
		IncludeTerminal:  *normalized.IncludeTerminal,
	}
	rootID := issue.ID
	snapshot := domain.GraphSnapshot{
		RootIssueID:      &rootID,
		Nodes:            []domain.IssueProjection{},
		Edges:            []domain.GraphEdge{},
		TopLevelIssueIDs: []string{},
	}
	if err := loadSelectedGraph(ctx, query, ports.LoadGraphCommand{
		Now:       now,
		Traversal: &traversal,
	}, graphProjectionSQL(now), &snapshot); err != nil {
		return domain.GraphResult{}, err
	}
	if snapshot.SelectedResult != nil {
		return *snapshot.SelectedResult, nil
	}
	traversal.RootIssueIDs = []string{strings.TrimSpace(issue.ID)}
	traversal.ExplicitRootID = issue.ID
	return domain.BuildGraph(snapshot, traversal), nil
}
