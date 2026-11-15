package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

// BoardSnapshotRepository loads full BoardService and IssueDetailService
// aggregates inside single SQLite read snapshots so sections never tear across
// concurrent writers and active-attempt gates never pay O(N) read transactions.
type BoardSnapshotRepository struct {
	db                    *DB
	clock                 clock.Clock
	afterFirstReadForTest func()

	watermarkMu     sync.Mutex
	watermarkByConn map[string]boardWatermarkConnState
	externalEpoch   uint64
}

type boardWatermarkConnState struct {
	dataVersion int64
	stateDigest string
}

// NewBoardSnapshotRepository returns a single-snapshot board and issue-detail
// loader backed by database.
func NewBoardSnapshotRepository(database *DB) (*BoardSnapshotRepository, error) {
	if database == nil {
		return nil, domain.NewError(domain.CodeStorageConfiguration, "board snapshot database is required", false)
	}
	return &BoardSnapshotRepository{db: database, clock: clock.RealClock{}}, nil
}

// WithClock configures the clock used when checking time-sensitive board
// state such as active-attempt lease expiration.
func (repository *BoardSnapshotRepository) WithClock(source clock.Clock) *BoardSnapshotRepository {
	if repository != nil && source != nil {
		repository.clock = source
	}
	return repository
}

// EnsureClock adopts source when the repository still uses the default real clock
// (or has nil clock), allowing composed services with injected clocks to share
// one time source with pre-constructed repositories.
func (repository *BoardSnapshotRepository) EnsureClock(source clock.Clock) {
	if repository == nil || source == nil {
		return
	}
	if repository.clock == nil {
		repository.clock = source
		return
	}
	if _, isDefault := repository.clock.(clock.RealClock); isDefault {
		repository.clock = source
	}
}

const loadBoardWatermarkQuery = `
SELECT
	(SELECT COALESCE(MAX(id), 0) FROM issue_events),
	(SELECT COUNT(*) FROM issue_events),
	(SELECT COALESCE(MAX(id), 0) FROM gate_evidence_events),
	(SELECT COUNT(*) FROM gate_evidence_events),
	(SELECT COALESCE(MAX(id), 0) FROM workflow_policy_events),
	(SELECT COUNT(*) FROM workflow_policy_events),
	(SELECT COUNT(*) FROM issues),
	(SELECT COALESCE(SUM(CASE WHEN status = 'blocked' THEN 1 ELSE 0 END), 0) FROM issues),
	(SELECT COALESCE(MAX(version), 0) FROM issues),
	(SELECT COALESCE(MAX(updated_at), '') FROM issues),
	(SELECT COALESCE(MAX(archived_at), '') FROM issues),
	(SELECT COUNT(*) FROM work_attempts),
	(SELECT COALESCE(SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END), 0) FROM work_attempts),
	(SELECT COALESCE(MAX(started_at), '') FROM work_attempts),
	(SELECT COALESCE(MAX(last_heartbeat_at), '') FROM work_attempts),
	(SELECT COALESCE(MAX(finished_at), '') FROM work_attempts),
	(SELECT COALESCE(MAX(lease_expires_at), '') FROM work_attempts),
	(SELECT COUNT(*) FROM resource_reservations),
	(SELECT COALESCE(SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END), 0) FROM resource_reservations),
	(SELECT COALESCE(MAX(version), 0) FROM resource_reservations),
	(SELECT COALESCE(MAX(created_at), '') FROM resource_reservations),
	(SELECT COALESCE(MAX(released_at), '') FROM resource_reservations),
	(SELECT COUNT(*) FROM review_requests),
	(SELECT COALESCE(SUM(CASE WHEN status = 'open' THEN 1 ELSE 0 END), 0) FROM review_requests),
	(SELECT COALESCE(MAX(version), 0) FROM review_requests),
	(SELECT COALESCE(MAX(created_at), '') FROM review_requests),
	(SELECT COALESCE(MAX(resolved_at), '') FROM review_requests),
	(SELECT COUNT(*) FROM issue_relations),
	(SELECT COALESCE(MAX(created_at), '') FROM issue_relations),
	(SELECT COUNT(*) FROM issue_labels),
	(SELECT COUNT(*) FROM gate_evidence),
	(SELECT COALESCE(MAX(version), 0) FROM gate_evidence),
	(SELECT COALESCE(MAX(updated_at), '') FROM gate_evidence),
	(SELECT COUNT(*) FROM workflow_policies),
	(SELECT COALESCE(MAX(version), 0) FROM workflow_policies),
	(SELECT COALESCE(MAX(updated_at), '') FROM workflow_policies),
	(SELECT COUNT(*) FROM review_approvals),
	(SELECT COUNT(*) FROM attempt_gate_snapshots),
	(SELECT COUNT(*) FROM work_attempts WHERE status = 'active' AND lease_expires_at > ?),
	(SELECT COALESCE(MAX(id), '') FROM work_attempts WHERE status = 'active' AND lease_expires_at > ?),
	(SELECT COALESCE(MIN(lease_expires_at), '') FROM work_attempts WHERE status = 'active' AND lease_expires_at > ?)
`

// LoadBoardWatermark computes a board state watermark within a single SQLite
// read snapshot combining board-visible table watermarks and PRAGMA data_version
// so external-process writes are detected without reading the full snapshot.
func (repository *BoardSnapshotRepository) LoadBoardWatermark(ctx context.Context) (string, error) {
	if repository == nil || repository.db == nil {
		return "", domain.NewError(domain.CodeStorageConfiguration, "board snapshot database is required", false)
	}
	clockSource := repository.clock
	if clockSource == nil {
		clockSource = clock.RealClock{}
	}
	nowText := formatStorageTime(clockSource.Now().UTC())
	var (
		issueEventsMaxID, issueEventsCount               int64
		gateEventsMaxID, gateEventsCount                 int64
		policyEventsMaxID, policyEventsCount             int64
		issuesCount, issuesBlocked, issuesMaxVersion     int64
		issuesMaxUpdatedAt, issuesMaxArchivedAt          string
		attemptsCount, attemptsActive                    int64
		attemptsMaxStartedAt, attemptsMaxHeartbeatAt     string
		attemptsMaxFinishedAt, attemptsMaxLeaseExpiresAt string
		reservationsCount, reservationsActive            int64
		reservationsMaxVersion                           int64
		reservationsMaxCreatedAt, reservationsMaxRelAt   string
		reviewsCount, reviewsOpen, reviewsMaxVersion     int64
		reviewsMaxCreatedAt, reviewsMaxResolvedAt        string
		relationsCount                                   int64
		relationsMaxCreatedAt                            string
		labelsCount                                      int64
		gateEvidenceCount, gateEvidenceMaxVersion        int64
		gateEvidenceMaxUpdatedAt                         string
		policiesCount, policiesMaxVersion                int64
		policiesMaxUpdatedAt                             string
		reviewApprovalsCount                             int64
		attemptGateSnapshotsCount                        int64
		attemptsLiveLeaseCount                           int64
		attemptsLiveLeaseMaxID                           string
		attemptsLiveLeaseMinExpiresAt                    string
		dataVersion                                      int64
		connKey                                          string
	)
	if err := repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		// Query table state first so SQLite opens the WAL read snapshot before
		// inspecting connection-local PRAGMA data_version within the same snapshot.
		row := query.QueryRowContext(ctx, loadBoardWatermarkQuery, nowText, nowText, nowText)
		if err := row.Scan(
			&issueEventsMaxID, &issueEventsCount,
			&gateEventsMaxID, &gateEventsCount,
			&policyEventsMaxID, &policyEventsCount,
			&issuesCount, &issuesBlocked, &issuesMaxVersion, &issuesMaxUpdatedAt, &issuesMaxArchivedAt,
			&attemptsCount, &attemptsActive, &attemptsMaxStartedAt, &attemptsMaxHeartbeatAt, &attemptsMaxFinishedAt, &attemptsMaxLeaseExpiresAt,
			&reservationsCount, &reservationsActive, &reservationsMaxVersion, &reservationsMaxCreatedAt, &reservationsMaxRelAt,
			&reviewsCount, &reviewsOpen, &reviewsMaxVersion, &reviewsMaxCreatedAt, &reviewsMaxResolvedAt,
			&relationsCount, &relationsMaxCreatedAt,
			&labelsCount,
			&gateEvidenceCount, &gateEvidenceMaxVersion, &gateEvidenceMaxUpdatedAt,
			&policiesCount, &policiesMaxVersion, &policiesMaxUpdatedAt,
			&reviewApprovalsCount,
			&attemptGateSnapshotsCount,
			&attemptsLiveLeaseCount,
			&attemptsLiveLeaseMaxID,
			&attemptsLiveLeaseMinExpiresAt,
		); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, "PRAGMA data_version").Scan(&dataVersion); err != nil {
			return err
		}
		if conn, ok := query.(*sql.Conn); ok {
			_ = conn.Raw(func(driverConn any) error {
				connKey = fmt.Sprintf("%T:%p", driverConn, driverConn)
				return nil
			})
		}
		return nil
	}); err != nil {
		return "", err
	}

	stateLine := fmt.Sprintf(
		"ie=%d/%d|ge=%d/%d|pe=%d/%d|i=%d/%d/%d/%s/%s|a=%d/%d/%s/%s/%s/%s|r=%d/%d/%d/%s/%s|rv=%d/%d/%d/%s/%s|rel=%d/%s|lbl=%d|gev=%d/%d/%s|pol=%d/%d/%s|rva=%d|ags=%d|alive=%d/%s/%s",
		issueEventsMaxID, issueEventsCount,
		gateEventsMaxID, gateEventsCount,
		policyEventsMaxID, policyEventsCount,
		issuesCount, issuesBlocked, issuesMaxVersion, issuesMaxUpdatedAt, issuesMaxArchivedAt,
		attemptsCount, attemptsActive, attemptsMaxStartedAt, attemptsMaxHeartbeatAt, attemptsMaxFinishedAt, attemptsMaxLeaseExpiresAt,
		reservationsCount, reservationsActive, reservationsMaxVersion, reservationsMaxCreatedAt, reservationsMaxRelAt,
		reviewsCount, reviewsOpen, reviewsMaxVersion, reviewsMaxCreatedAt, reviewsMaxResolvedAt,
		relationsCount, relationsMaxCreatedAt,
		labelsCount,
		gateEvidenceCount, gateEvidenceMaxVersion, gateEvidenceMaxUpdatedAt,
		policiesCount, policiesMaxVersion, policiesMaxUpdatedAt,
		reviewApprovalsCount,
		attemptGateSnapshotsCount,
		attemptsLiveLeaseCount, attemptsLiveLeaseMaxID, attemptsLiveLeaseMinExpiresAt,
	)
	stateSum := sha256.Sum256([]byte(stateLine))
	stateDigest := hex.EncodeToString(stateSum[:])
	return repository.resolveBoardWatermark(connKey, dataVersion, stateDigest), nil
}

func (repository *BoardSnapshotRepository) resolveBoardWatermark(connKey string, dataVersion int64, stateDigest string) string {
	repository.watermarkMu.Lock()
	defer repository.watermarkMu.Unlock()

	if repository.watermarkByConn == nil {
		repository.watermarkByConn = make(map[string]boardWatermarkConnState)
	}
	if connKey != "" {
		if prev, known := repository.watermarkByConn[connKey]; known {
			if dataVersion != prev.dataVersion && stateDigest == prev.stateDigest {
				repository.externalEpoch++
			}
		}
		repository.watermarkByConn[connKey] = boardWatermarkConnState{
			dataVersion: dataVersion,
			stateDigest: stateDigest,
		}
	}
	combined := fmt.Sprintf("%s|ext:%d", stateDigest, repository.externalEpoch)
	sum := sha256.Sum256([]byte(combined))
	return hex.EncodeToString(sum[:])
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
