package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/pagination"
	"rhizome-mcp/internal/ports"
)

type issueCursor struct {
	Sort         string `json:"sort,omitempty"`
	PriorityRank int    `json:"priority_rank,omitempty"`
	IsClaimable  bool   `json:"is_claimable,omitempty"`
	SequenceNo   int64  `json:"sequence_no"`
	CreatedAt    string `json:"created_at,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

type issueListRow struct {
	projection   domain.IssueProjection
	rawCreatedAt string
	rawUpdatedAt string
}

var issueCursorCodec = pagination.NewCodec[issueCursor](0)

const (
	issuePriorityRankSQL = `(CASE priority
		WHEN 'critical' THEN 4
		WHEN 'high' THEN 3
		WHEN 'medium' THEN 2
		WHEN 'low' THEN 1
		ELSE 0 END)`
	issueUnresolvedBlockerCountSQL = `(SELECT COUNT(*)
		FROM issue_relations AS blocker_relation
		JOIN issues AS blocker_source ON blocker_source.id = blocker_relation.source_issue_id
		WHERE blocker_relation.type = 'blocks'
			AND blocker_relation.target_issue_id = issues.id
			AND blocker_source.archived_at IS NULL
			AND blocker_source.status NOT IN ('done', 'cancelled'))`
	issueBlockedSQL = `(CASE
		WHEN issues.status = 'blocked' OR ` + issueUnresolvedBlockerCountSQL + ` > 0
		THEN 1 ELSE 0 END)`
	issueClaimableSQL = `(CASE
		WHEN issues.archived_at IS NULL
			AND issues.type IN ('task', 'bug')
			AND issues.status IN ('ready', 'review')
			AND ` + issueUnresolvedBlockerCountSQL + ` = 0
		THEN 1 ELSE 0 END)`
)

func issueActiveAttemptIDSQL(now time.Time) string {
	return `(SELECT id FROM work_attempts WHERE issue_id = issues.id AND status = 'active' AND lease_expires_at > '` +
		formatStorageTime(now) + `' LIMIT 1)`
}

func issueClaimableSQLAt(now time.Time) string {
	return `(CASE WHEN ` + issueClaimableSQL + ` = 1 AND ` + issueActiveAttemptIDSQL(now) + ` IS NULL THEN 1 ELSE 0 END)`
}

func issueEffectiveStatusSQL(now time.Time) string {
	return `(CASE WHEN ` + issueActiveAttemptIDSQL(now) + ` IS NOT NULL THEN 'in_progress' ELSE issues.status END)`
}

// GetIssueProjection returns a single issue with computed projection fields
// (effective_status, unresolved_blocker_count, is_blocked, is_claimable, active_attempt_id).
// Archived issues remain visible. now comes from the caller's injected
// clock (docs/04 §15), never the wall clock, so results stay deterministic
// under tests.
func (repository *IssueRepository) GetIssueProjection(ctx context.Context, command ports.GetIssueProjectionCommand) (domain.IssueProjection, error) {
	var where string
	var args []any
	if command.Identifier.Kind == domain.IssueIdentifierInternalID {
		where = "id = ?"
		args = []any{command.Identifier.Value}
	} else {
		where = "sequence_no = ?"
		args = []any{command.Identifier.SequenceNo}
	}
	var result domain.IssueProjection
	err := repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		projection, err := fetchIssueProjection(ctx, query, where, args, command.Now)
		if err != nil {
			return err
		}
		result = projection
		return nil
	})
	return result, err
}

// queryIssueProjectionByID loads the projection for one issue by internal
// ID, usable from either a read-only snapshot or an open write transaction
// (both satisfy Queryer) -- e.g. ClaimIssue reading the post-claim
// projection inside its own claim transaction, rather than hardcoding
// values that are only correct under today's claim preconditions.
func queryIssueProjectionByID(ctx context.Context, query Queryer, issueID string, now time.Time) (domain.IssueProjection, error) {
	return fetchIssueProjection(ctx, query, "id = ?", []any{issueID}, now)
}

func fetchIssueProjection(ctx context.Context, query Queryer, where string, args []any, now time.Time) (domain.IssueProjection, error) {
	statement := `SELECT id, sequence_no, type, title, description, acceptance_criteria,
		status, priority, parent_id, blocked_reason, version,
		created_by_session_id, created_at, updated_at, closed_at,
		archived_at, archived_by_session_id,
		` + issueUnresolvedBlockerCountSQL + ` AS unresolved_blocker_count,
		` + issueBlockedSQL + ` AS is_blocked,
		` + issueClaimableSQLAt(now) + ` AS is_claimable,
		` + issueEffectiveStatusSQL(now) + ` AS effective_status,
		` + issueActiveAttemptIDSQL(now) + ` AS active_attempt_id,
		` + issuePriorityRankSQL + ` AS priority_rank
		FROM issues WHERE ` + where

	row := query.QueryRowContext(ctx, statement, args...)
	projection, err := scanIssueListProjection(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.IssueProjection{}, domain.NewError(domain.CodeIssueNotFound, "issue not found", false)
		}
		return domain.IssueProjection{}, err
	}
	// loadIssueListLabels mutates its items slice's elements in place; it
	// must be the same slice variable we read back from, not a fresh
	// slice literal wrapping a copy of projection (which would silently
	// discard the loaded labels -- caught by
	// TestIssueRepositoryGetIssueProjectionMatchesListIssuesAndReportsNotFound).
	items := []domain.IssueProjection{projection}
	if err := loadIssueListLabels(ctx, query, items); err != nil {
		return domain.IssueProjection{}, err
	}
	return items[0], nil
}

// ListIssues returns a bounded, deterministic issue page. Label filters have
// any-label semantics, and the base projection plus batched labels are read
// inside one SQLite snapshot.
func (repository *IssueRepository) ListIssues(ctx context.Context, command ports.ListIssuesCommand) (domain.IssueList, error) {
	input, err := command.Input.Validate()
	if err != nil {
		return domain.IssueList{}, err
	}
	var after *issueCursor
	if input.Cursor != "" {
		decoded, err := decodeAndValidateIssueCursor(input.Cursor, input.Sort)
		if err != nil {
			return domain.IssueList{}, err
		}
		after = decoded
	}

	var result domain.IssueList
	now := command.Now.UTC()
	err = repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		claimableSQL := issueClaimableSQLAt(now)
		effectiveStatusSQL := issueEffectiveStatusSQL(now)
		where := []string{"1 = 1"}
		args := make([]any, 0, 16)
		if !input.IncludeArchived {
			where = append(where, "archived_at IS NULL")
		}
		appendIssueListInFilter(&where, &args, "type", input.Types)
		appendIssueListInFilter(&where, &args, "status", input.Statuses)
		appendIssueListInFilter(&where, &args, effectiveStatusSQL, input.EffectiveStatuses)
		appendIssueListInFilter(&where, &args, "priority", input.Priorities)
		if input.ParentIssueID != nil {
			identifier, err := domain.ParseIssueIdentifier(*input.ParentIssueID)
			if err != nil {
				return err
			}
			if identifier.Kind == domain.IssueIdentifierInternalID {
				where = append(where, "parent_id = ?")
				args = append(args, identifier.Value)
			} else {
				where = append(where, "parent_id = (SELECT id FROM issues WHERE sequence_no = ?)")
				args = append(args, identifier.SequenceNo)
			}
		}
		if input.IsBlocked != nil {
			where = append(where, "("+issueBlockedSQL+") = ?")
			args = append(args, boolInt(*input.IsBlocked))
		}
		if input.IsClaimable != nil {
			where = append(where, claimableSQL+" = ?")
			args = append(args, boolInt(*input.IsClaimable))
		}
		if len(input.Labels) > 0 {
			conditions := make([]string, len(input.Labels))
			for index, label := range input.Labels {
				conditions[index] = "labels.name = ? COLLATE NOCASE"
				args = append(args, label)
			}
			where = append(where, "EXISTS (SELECT 1 FROM issue_labels JOIN labels ON labels.id = issue_labels.label_id "+
				"WHERE issue_labels.issue_id = issues.id AND ("+strings.Join(conditions, " OR ")+"))")
		}
		orderBySQL, cursorPredicate, cursorArgs := issueListOrderAndCursorClause(input.Sort, claimableSQL, after)
		if cursorPredicate != "" {
			where = append(where, cursorPredicate)
			args = append(args, cursorArgs...)
		}

		statement := issueListProjectionSelect(now) + ` WHERE ` + strings.Join(where, " AND ") + orderBySQL
		args = append(args, input.Limit+1)
		rows, err := query.QueryContext(ctx, statement, args...)
		if err != nil {
			return err
		}
		listedRows := make([]issueListRow, 0, input.Limit)
		for rows.Next() {
			row, err := scanIssueListProjectionRow(rows)
			if err != nil {
				rows.Close()
				return err
			}
			listedRows = append(listedRows, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}

		if len(listedRows) > input.Limit {
			result.HasMore = true
			listedRows = listedRows[:input.Limit]
			lastRow := listedRows[len(listedRows)-1]
			last := lastRow.projection
			cursor, err := issueCursorCodec.Encode(issueCursor{
				Sort:         string(input.Sort),
				PriorityRank: issuePriorityRank(last.Priority),
				IsClaimable:  last.IsClaimable,
				SequenceNo:   last.SequenceNo,
				CreatedAt:    lastRow.rawCreatedAt,
				UpdatedAt:    lastRow.rawUpdatedAt,
			})
			if err != nil {
				return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode issue cursor", false)
			}
			result.NextCursor = &cursor
		}
		items := make([]domain.IssueProjection, len(listedRows))
		for index, row := range listedRows {
			items[index] = row.projection
		}
		if err := loadIssueListLabels(ctx, query, items); err != nil {
			return err
		}
		result.Items = items
		return nil
	})
	if err != nil {
		return domain.IssueList{}, err
	}
	if result.Items == nil {
		result.Items = []domain.IssueProjection{}
	}
	return result, nil
}

func issueListProjectionSelect(now time.Time) string {
	return `SELECT id, sequence_no, type, title, description, acceptance_criteria,
		status, priority, parent_id, blocked_reason, version,
		created_by_session_id, created_at, updated_at, closed_at,
		archived_at, archived_by_session_id,
		` + issueUnresolvedBlockerCountSQL + ` AS unresolved_blocker_count,
		` + issueBlockedSQL + ` AS is_blocked,
		` + issueClaimableSQLAt(now) + ` AS is_claimable,
		` + issueEffectiveStatusSQL(now) + ` AS effective_status,
		` + issueActiveAttemptIDSQL(now) + ` AS active_attempt_id,
		` + issuePriorityRankSQL + ` AS priority_rank
		FROM issues`
}

// CountIssuesByEffectiveStatus returns one row per effective status present
// among non-archived issues. The result set is bounded by the fixed number of
// possible effective statuses regardless of backlog size.
func (repository *IssueRepository) CountIssuesByEffectiveStatus(ctx context.Context, command ports.CountIssuesByEffectiveStatusCommand) ([]domain.EffectiveStatusCount, error) {
	now := command.Now.UTC()
	var result []domain.EffectiveStatusCount
	err := repository.db.Read(ctx, func(ctx context.Context, query Queryer) error {
		counts, err := countIssuesByEffectiveStatus(ctx, query, now)
		if err != nil {
			return err
		}
		result = counts
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func countIssuesByEffectiveStatus(ctx context.Context, query Queryer, now time.Time) ([]domain.EffectiveStatusCount, error) {
	effectiveStatusSQL := issueEffectiveStatusSQL(now.UTC())
	rows, err := query.QueryContext(ctx, `SELECT `+effectiveStatusSQL+` AS effective_status, COUNT(*)
		FROM issues WHERE archived_at IS NULL
		GROUP BY effective_status
		ORDER BY effective_status ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]domain.EffectiveStatusCount, 0)
	for rows.Next() {
		var effectiveStatus string
		var count int64
		if err := rows.Scan(&effectiveStatus, &count); err != nil {
			return nil, domain.WrapError(err, domain.CodeStorageCorrupt, "stored effective status count is invalid", false)
		}
		result = append(result, domain.EffectiveStatusCount{EffectiveStatus: domain.EffectiveStatus(effectiveStatus), Count: count})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func scanIssueListProjection(scanner labelScanner) (domain.IssueProjection, error) {
	row, err := scanIssueListProjectionRow(scanner)
	if err != nil {
		return domain.IssueProjection{}, err
	}
	return row.projection, nil
}

func scanIssueListProjectionRow(scanner labelScanner) (issueListRow, error) {
	var (
		id, issueType, title, status, priority, createdAt, updatedAt                      string
		description, acceptanceCriteria, parentID, blockedReason                          sql.NullString
		createdBySessionID, closedAt, archivedAt, archivedBySessionID, activeAttemptID    sql.NullString
		effectiveStatus                                                                   string
		sequenceNo, version, unresolvedBlockerCount, isBlocked, isClaimable, priorityRank int64
	)
	if err := scanner.Scan(
		&id, &sequenceNo, &issueType, &title, &description, &acceptanceCriteria,
		&status, &priority, &parentID, &blockedReason, &version,
		&createdBySessionID, &createdAt, &updatedAt, &closedAt, &archivedAt, &archivedBySessionID,
		&unresolvedBlockerCount, &isBlocked, &isClaimable, &effectiveStatus, &activeAttemptID, &priorityRank,
	); err != nil {
		if err == sql.ErrNoRows {
			return issueListRow{}, err
		}
		return issueListRow{}, domain.WrapError(err, domain.CodeStorageCorrupt, "stored issue projection is invalid", false)
	}
	issue, err := parseIssueProjectionColumns(id, sequenceNo, issueType, title, description, acceptanceCriteria,
		parentID, blockedReason, status, priority, version, createdBySessionID, createdAt, updatedAt,
		closedAt, archivedAt, archivedBySessionID)
	if err != nil {
		return issueListRow{}, err
	}
	return issueListRow{
		projection: domain.IssueProjection{
			Issue:                  issue,
			EffectiveStatus:        domain.EffectiveStatus(effectiveStatus),
			UnresolvedBlockerCount: unresolvedBlockerCount,
			IsBlocked:              isBlocked != 0,
			IsClaimable:            isClaimable != 0,
			ActiveAttemptID:        nullableStringPointer(activeAttemptID),
		},
		rawCreatedAt: createdAt,
		rawUpdatedAt: updatedAt,
	}, nil
}

func loadIssueListLabels(ctx context.Context, query Queryer, items []domain.IssueProjection) error {
	if len(items) == 0 {
		return nil
	}
	placeholders := make([]string, len(items))
	args := make([]any, len(items))
	byID := make(map[string]int, len(items))
	for index := range items {
		placeholders[index] = "?"
		args[index] = items[index].ID
		byID[items[index].ID] = index
	}
	rows, err := query.QueryContext(ctx, `SELECT issue_labels.issue_id,
		labels.id, labels.name, labels.description, labels.created_at
		FROM issue_labels JOIN labels ON labels.id = issue_labels.label_id
		WHERE issue_labels.issue_id IN (`+strings.Join(placeholders, ",")+`)
		ORDER BY issue_labels.issue_id ASC, labels.name COLLATE NOCASE ASC, labels.id ASC`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var issueID, id, name, createdAt string
		var description sql.NullString
		if err := rows.Scan(&issueID, &id, &name, &description, &createdAt); err != nil {
			return domain.WrapError(err, domain.CodeStorageCorrupt, "stored issue label projection is invalid", false)
		}
		displayName, normalizedName, err := domain.NormalizeLabelName(name)
		if err != nil {
			return domain.WrapError(err, domain.CodeStorageCorrupt, "stored label projection is invalid", false)
		}
		created, err := parseStorageTime(createdAt)
		if err != nil {
			return domain.WrapError(err, domain.CodeStorageCorrupt, "stored label projection is invalid", false,
				domain.Detail{Field: "created_at", Code: "INVALID_TIMESTAMP"})
		}
		index, exists := byID[issueID]
		if !exists {
			return domain.NewError(domain.CodeStorageCorrupt, "stored issue label projection is invalid", false)
		}
		items[index].Labels = append(items[index].Labels, domain.Label{
			ID: id, Name: displayName, NormalizedName: normalizedName,
			Description: nullableStringPointer(description), CreatedAt: created.UTC(),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

func appendIssueListInFilter[T ~string](where *[]string, args *[]any, column string, values []T) {
	if len(values) == 0 {
		return
	}
	placeholders := make([]string, len(values))
	for index, value := range values {
		placeholders[index] = "?"
		*args = append(*args, value)
	}
	*where = append(*where, column+" IN ("+strings.Join(placeholders, ",")+")")
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func issuePriorityRank(priority domain.Priority) int {
	switch priority {
	case domain.PriorityCritical:
		return 4
	case domain.PriorityHigh:
		return 3
	case domain.PriorityMedium:
		return 2
	case domain.PriorityLow:
		return 1
	default:
		return 0
	}
}

func decodeAndValidateIssueCursor(encoded string, effectiveSort domain.IssueSortOrder) (*issueCursor, error) {
	decoded, err := issueCursorCodec.Decode(encoded)
	if err != nil {
		return nil, issueCursorError(err)
	}
	cursorSort := domain.IssueSortOrder(decoded.Sort)
	if cursorSort == "" {
		cursorSort = domain.IssueSortPriority
	}
	if !cursorSort.Valid() {
		return nil, issueCursorError(errors.New("invalid issue cursor sort"))
	}
	if cursorSort != effectiveSort {
		message := fmt.Sprintf("issue cursor sort %q does not match requested sort %q", cursorSort, effectiveSort)
		return nil, domain.NewError(domain.CodeInvalidArgument, message, false,
			domain.Detail{Field: "cursor", Code: "INVALID_CURSOR", Message: message})
	}
	if decoded.SequenceNo < 1 {
		return nil, issueCursorError(errors.New("invalid issue cursor payload"))
	}
	switch effectiveSort {
	case domain.IssueSortPriority:
		if decoded.PriorityRank < 1 || decoded.PriorityRank > 4 {
			return nil, issueCursorError(errors.New("invalid issue cursor payload"))
		}
	case domain.IssueSortCreatedDesc:
		if decoded.CreatedAt == "" {
			return nil, issueCursorError(errors.New("invalid issue cursor payload"))
		}
		if _, err := parseStorageTime(decoded.CreatedAt); err != nil {
			return nil, issueCursorError(errors.New("invalid issue cursor payload"))
		}
	case domain.IssueSortUpdatedDesc:
		if decoded.UpdatedAt == "" {
			return nil, issueCursorError(errors.New("invalid issue cursor payload"))
		}
		if _, err := parseStorageTime(decoded.UpdatedAt); err != nil {
			return nil, issueCursorError(errors.New("invalid issue cursor payload"))
		}
	case domain.IssueSortSequenceDesc:
	}
	return &decoded, nil
}

func issueListOrderAndCursorClause(sortOrder domain.IssueSortOrder, claimableSQL string, after *issueCursor) (orderBy string, predicate string, args []any) {
	switch sortOrder {
	case domain.IssueSortCreatedDesc:
		if after != nil {
			predicate = "(issues.created_at < ? OR (issues.created_at = ? AND issues.sequence_no < ?))"
			args = []any{after.CreatedAt, after.CreatedAt, after.SequenceNo}
		}
		return " ORDER BY issues.created_at DESC, issues.sequence_no DESC LIMIT ?", predicate, args
	case domain.IssueSortUpdatedDesc:
		if after != nil {
			predicate = "(issues.updated_at < ? OR (issues.updated_at = ? AND issues.sequence_no < ?))"
			args = []any{after.UpdatedAt, after.UpdatedAt, after.SequenceNo}
		}
		return " ORDER BY issues.updated_at DESC, issues.sequence_no DESC LIMIT ?", predicate, args
	case domain.IssueSortSequenceDesc:
		if after != nil {
			predicate = "issues.sequence_no < ?"
			args = []any{after.SequenceNo}
		}
		return " ORDER BY issues.sequence_no DESC LIMIT ?", predicate, args
	default:
		if after != nil {
			prioritySQL := issuePriorityRankSQL
			predicate = "(" + prioritySQL + " < ? OR (" + prioritySQL + " = ? AND (" +
				claimableSQL + " < ? OR (" + claimableSQL + " = ? AND sequence_no > ?))))"
			args = []any{after.PriorityRank, after.PriorityRank, boolInt(after.IsClaimable),
				boolInt(after.IsClaimable), after.SequenceNo}
		}
		return " ORDER BY priority_rank DESC, is_claimable DESC, sequence_no ASC LIMIT ?", predicate, args
	}
}

func issueCursorError(err error) error {
	code := "MALFORMED_CURSOR"
	if errors.Is(err, pagination.ErrCursorTooLarge) {
		code = "CURSOR_TOO_LARGE"
	} else if errors.Is(err, pagination.ErrUnsupportedVersion) {
		code = "UNSUPPORTED_CURSOR_VERSION"
	}
	return domain.NewError(domain.CodeInvalidArgument, "issue cursor is invalid", false,
		domain.Detail{Field: "cursor", Code: code})
}
