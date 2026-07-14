package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/pagination"
	"rhizome-mcp/internal/ports"
)

type issueCursor struct {
	PriorityRank int   `json:"priority_rank"`
	IsClaimable  bool  `json:"is_claimable"`
	SequenceNo   int64 `json:"sequence_no"`
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
			AND issues.status = 'ready'
			AND ` + issueUnresolvedBlockerCountSQL + ` = 0
		THEN 1 ELSE 0 END)`
)

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
		decoded, err := issueCursorCodec.Decode(input.Cursor)
		if err != nil || decoded.PriorityRank < 1 || decoded.PriorityRank > 4 ||
			decoded.SequenceNo < 1 {
			if err == nil {
				err = errors.New("invalid issue cursor payload")
			}
			return domain.IssueList{}, issueCursorError(err)
		}
		after = &decoded
	}

	var result domain.IssueList
	err = repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		where := []string{"1 = 1"}
		args := make([]any, 0, 16)
		if !input.IncludeArchived {
			where = append(where, "archived_at IS NULL")
		}
		appendIssueListInFilter(&where, &args, "type", input.Types)
		appendIssueListInFilter(&where, &args, "status", input.Statuses)
		appendIssueListInFilter(&where, &args, "status", input.EffectiveStatuses)
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
			where = append(where, issueClaimableSQL+" = ?")
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
		if after != nil {
			prioritySQL := issuePriorityRankSQL
			claimableSQL := issueClaimableSQL
			where = append(where, "("+prioritySQL+" < ? OR ("+prioritySQL+" = ? AND ("+
				claimableSQL+" < ? OR ("+claimableSQL+" = ? AND sequence_no > ?))))")
			args = append(args, after.PriorityRank, after.PriorityRank, boolInt(after.IsClaimable),
				boolInt(after.IsClaimable), after.SequenceNo)
		}

		statement := `SELECT id, sequence_no, type, title, description, acceptance_criteria,
			status, priority, parent_id, blocked_reason, version,
			created_by_session_id, created_at, updated_at, closed_at,
			archived_at, archived_by_session_id,
			` + issueUnresolvedBlockerCountSQL + ` AS unresolved_blocker_count,
			` + issueBlockedSQL + ` AS is_blocked,
			` + issueClaimableSQL + ` AS is_claimable,
			` + issuePriorityRankSQL + ` AS priority_rank
			FROM issues WHERE ` + strings.Join(where, " AND ") +
			` ORDER BY priority_rank DESC, is_claimable DESC, sequence_no ASC LIMIT ?`
		args = append(args, input.Limit+1)
		rows, err := query.QueryContext(ctx, statement, args...)
		if err != nil {
			return err
		}
		items := make([]domain.IssueProjection, 0, input.Limit)
		for rows.Next() {
			item, err := scanIssueListProjection(rows)
			if err != nil {
				rows.Close()
				return err
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}

		if len(items) > input.Limit {
			result.HasMore = true
			items = items[:input.Limit]
			last := items[len(items)-1]
			cursor, err := issueCursorCodec.Encode(issueCursor{
				PriorityRank: issuePriorityRank(last.Priority),
				IsClaimable:  last.IsClaimable,
				SequenceNo:   last.SequenceNo,
			})
			if err != nil {
				return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode issue cursor", false)
			}
			result.NextCursor = &cursor
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

func scanIssueListProjection(scanner labelScanner) (domain.IssueProjection, error) {
	var (
		id, issueType, title, status, priority, createdAt, updatedAt                      string
		description, acceptanceCriteria, parentID, blockedReason                          sql.NullString
		createdBySessionID, closedAt, archivedAt, archivedBySessionID                     sql.NullString
		sequenceNo, version, unresolvedBlockerCount, isBlocked, isClaimable, priorityRank int64
	)
	if err := scanner.Scan(
		&id, &sequenceNo, &issueType, &title, &description, &acceptanceCriteria,
		&status, &priority, &parentID, &blockedReason, &version,
		&createdBySessionID, &createdAt, &updatedAt, &closedAt, &archivedAt, &archivedBySessionID,
		&unresolvedBlockerCount, &isBlocked, &isClaimable, &priorityRank,
	); err != nil {
		if err == sql.ErrNoRows {
			return domain.IssueProjection{}, err
		}
		return domain.IssueProjection{}, domain.WrapError(err, domain.CodeStorageCorrupt, "stored issue projection is invalid", false)
	}
	issue, err := parseIssueProjectionColumns(id, sequenceNo, issueType, title, description, acceptanceCriteria,
		parentID, blockedReason, status, priority, version, createdBySessionID, createdAt, updatedAt,
		closedAt, archivedAt, archivedBySessionID)
	if err != nil {
		return domain.IssueProjection{}, err
	}
	return domain.IssueProjection{
		Issue:                  issue,
		EffectiveStatus:        domain.EffectiveStatus(status),
		UnresolvedBlockerCount: unresolvedBlockerCount,
		IsBlocked:              isBlocked != 0,
		IsClaimable:            isClaimable != 0,
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
		created, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return domain.WrapError(err, domain.CodeStorageCorrupt, "stored label projection is invalid", false,
				domain.Detail{Field: "created_at", Code: "INVALID_TIMESTAMP"})
		}
		if _, offset := created.Zone(); offset != 0 {
			return domain.NewError(domain.CodeStorageCorrupt, "stored label projection is invalid", false,
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
