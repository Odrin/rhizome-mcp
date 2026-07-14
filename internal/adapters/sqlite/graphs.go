package sqlite

import (
	"context"
	"database/sql"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

// GraphRepository is the SQLite implementation of ports.GraphRepository.
type GraphRepository struct {
	db *DB
}

// NewGraphRepository returns a graph snapshot repository backed by database.
func NewGraphRepository(database *DB) (*GraphRepository, error) {
	if database == nil {
		return nil, domain.NewError(domain.CodeStorageConfiguration, "graph database is required", false)
	}
	return &GraphRepository{db: database}, nil
}

// LoadGraph reads all nonarchived graph candidates, their projections, and
// labels under one read snapshot. Traversal deliberately occurs after this
// short read transaction in the domain graph engine.
func (repository *GraphRepository) LoadGraph(ctx context.Context, command ports.LoadGraphCommand) (domain.GraphSnapshot, error) {
	result := domain.GraphSnapshot{Nodes: []domain.IssueProjection{}, Edges: []domain.GraphEdge{}, TopLevelIssueIDs: []string{}}
	err := repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		if command.RootIdentifier != nil {
			rootID, archived, err := loadGraphRoot(ctx, query, *command.RootIdentifier)
			if err != nil {
				return err
			}
			if archived {
				return domain.NewError(domain.CodeIssueArchived, "issue is archived", false)
			}
			result.RootIssueID = &rootID
		}

		rows, err := query.QueryContext(ctx, `SELECT id, sequence_no, type, title, NULL AS description, NULL AS acceptance_criteria,
			status, priority, parent_id, blocked_reason, version,
			created_by_session_id, created_at, updated_at, closed_at,
			archived_at, archived_by_session_id,
			`+issueUnresolvedBlockerCountSQL+` AS unresolved_blocker_count,
			`+issueBlockedSQL+` AS is_blocked,
			`+issueClaimableSQL+` AS is_claimable,
			`+issuePriorityRankSQL+` AS priority_rank
			FROM issues WHERE archived_at IS NULL ORDER BY sequence_no ASC, id ASC`)
		if err != nil {
			return err
		}
		for rows.Next() {
			node, err := scanIssueListProjection(rows)
			if err != nil {
				rows.Close()
				return err
			}
			result.Nodes = append(result.Nodes, node)
			if node.ParentID == nil {
				result.TopLevelIssueIDs = append(result.TopLevelIssueIDs, node.ID)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := loadGraphLabels(ctx, query, result.Nodes); err != nil {
			return err
		}

		rows, err = query.QueryContext(ctx, `SELECT relation.source_issue_id, relation.target_issue_id, relation.type
			FROM issue_relations AS relation
			JOIN issues AS source ON source.id = relation.source_issue_id AND source.archived_at IS NULL
			JOIN issues AS target ON target.id = relation.target_issue_id AND target.archived_at IS NULL
			ORDER BY relation.type ASC, source.sequence_no ASC, source.id ASC, target.sequence_no ASC, target.id ASC`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var edge domain.GraphEdge
			if err := rows.Scan(&edge.SourceIssueID, &edge.TargetIssueID, &edge.Type); err != nil {
				rows.Close()
				return domain.WrapError(err, domain.CodeStorageCorrupt, "stored graph relation is invalid", false)
			}
			result.Edges = append(result.Edges, edge)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}

		rows, err = query.QueryContext(ctx, `SELECT parent.id, child.id
			FROM issues AS child JOIN issues AS parent ON parent.id = child.parent_id
			WHERE child.archived_at IS NULL AND parent.archived_at IS NULL
			ORDER BY parent.sequence_no ASC, parent.id ASC, child.sequence_no ASC, child.id ASC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var edge domain.GraphEdge
			edge.Type = "contains"
			if err := rows.Scan(&edge.SourceIssueID, &edge.TargetIssueID); err != nil {
				return domain.WrapError(err, domain.CodeStorageCorrupt, "stored graph hierarchy is invalid", false)
			}
			result.Edges = append(result.Edges, edge)
		}
		return rows.Err()
	})
	if err != nil {
		return domain.GraphSnapshot{}, err
	}
	return result, nil
}

func loadGraphRoot(ctx context.Context, query Queryer, identifier domain.IssueIdentifier) (string, bool, error) {
	var id string
	var archivedAt sql.NullString
	var err error
	switch identifier.Kind {
	case domain.IssueIdentifierInternalID:
		err = query.QueryRowContext(ctx, `SELECT id, archived_at FROM issues WHERE id = ?`, identifier.Value).Scan(&id, &archivedAt)
	case domain.IssueIdentifierDisplayID:
		err = query.QueryRowContext(ctx, `SELECT id, archived_at FROM issues WHERE sequence_no = ?`, identifier.SequenceNo).Scan(&id, &archivedAt)
	default:
		return "", false, domain.NewError(domain.CodeInvalidArgument, "issue identifier is invalid", false,
			domain.Detail{Field: "root_issue_id", Code: "INVALID_IDENTIFIER"})
	}
	if err == sql.ErrNoRows {
		return "", false, domain.NewError(domain.CodeIssueNotFound, "issue not found", false)
	}
	if err != nil {
		return "", false, err
	}
	return id, archivedAt.Valid, nil
}

func loadGraphLabels(ctx context.Context, query Queryer, nodes []domain.IssueProjection) error {
	const labelBatchSize = 500
	for start := 0; start < len(nodes); start += labelBatchSize {
		end := start + labelBatchSize
		if end > len(nodes) {
			end = len(nodes)
		}
		if err := loadIssueListLabels(ctx, query, nodes[start:end]); err != nil {
			return err
		}
	}
	return nil
}

var _ ports.GraphRepository = (*GraphRepository)(nil)
