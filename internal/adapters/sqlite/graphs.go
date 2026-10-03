package sqlite

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

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

// LoadGraph selects graph candidates and hydrates retained projections under
// one read snapshot. A nil traversal preserves the full-snapshot contract.
func (repository *GraphRepository) LoadGraph(ctx context.Context, command ports.LoadGraphCommand) (domain.GraphSnapshot, error) {
	if command.Stats != nil {
		*command.Stats = ports.GraphLoadStats{}
		started := time.Now()
		defer func() { command.Stats.SnapshotDuration = time.Since(started) }()
	}
	result := domain.GraphSnapshot{Nodes: []domain.IssueProjection{}, Edges: []domain.GraphEdge{}, TopLevelIssueIDs: []string{}}
	claimableSQL := issueClaimableSQLAt(command.Now)
	effectiveStatusSQL := issueEffectiveStatusSQL(command.Now)
	activeAttemptSQL := issueActiveAttemptIDSQL(command.Now)
	projectionSQL := `SELECT id, sequence_no, type, title, NULL AS description, NULL AS acceptance_criteria,
		status, priority, parent_id, blocked_reason, version,
		created_by_session_id, created_at, updated_at, closed_at,
		archived_at, archived_by_session_id,
		` + issueUnresolvedBlockerCountSQL + ` AS unresolved_blocker_count,
		` + issueBlockedSQL + ` AS is_blocked,
		` + claimableSQL + ` AS is_claimable,
		` + effectiveStatusSQL + ` AS effective_status,
		` + activeAttemptSQL + ` AS active_attempt_id,
		` + issuePriorityRankSQL + ` AS priority_rank
		FROM issues WHERE archived_at IS NULL`
	err := repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		if command.Stats != nil {
			query = graphStatsQueryer{Queryer: query, stats: command.Stats}
		}
		if command.RootIdentifier != nil {
			rootID, archived, err := loadGraphRoot(ctx, query, *command.RootIdentifier)
			if err != nil {
				return err
			}
			if archived {
				return domain.NewError(domain.CodeIssueArchived, "issue is archived", false)
			}
			result.RootIssueID = &rootID
			graphRowsRead(command.Stats, 1)
		}

		if command.Traversal != nil {
			return loadSelectedGraph(ctx, query, command, projectionSQL, &result)
		}
		rows, err := query.QueryContext(ctx, projectionSQL+` ORDER BY sequence_no ASC, id ASC`)
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
			graphRowsRead(command.Stats, 1)
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
		for _, node := range result.Nodes {
			graphRowsRead(command.Stats, len(node.Labels))
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
			graphRowsRead(command.Stats, 1)
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
			graphRowsRead(command.Stats, 1)
		}
		return rows.Err()
	})
	if err != nil {
		return domain.GraphSnapshot{}, err
	}
	if command.Stats != nil {
		command.Stats.CandidateNodes = len(result.Nodes)
		if result.SelectedResult == nil {
			command.Stats.HydratedNodes = len(result.Nodes)
		}
	}
	return result, nil
}

func loadSelectedGraph(ctx context.Context, query Queryer, command ports.LoadGraphCommand, projectionSQL string, snapshot *domain.GraphSnapshot) error {
	traversal := *command.Traversal
	known := make(map[string]domain.IssueProjection)
	if snapshot.RootIssueID == nil {
		if err := loadGraphMetadata(ctx, query, nil, command.Stats, known); err != nil {
			return err
		}
	} else {
		traversal.RootIssueIDs = []string{*snapshot.RootIssueID}
		traversal.ExplicitRootID = *snapshot.RootIssueID
		frontier := traversal.RootIssueIDs
		if err := loadGraphMetadata(ctx, query, frontier, command.Stats, known); err != nil {
			return err
		}
		for depth := 0; depth < traversal.Depth && len(frontier) > 0; depth++ {
			expand := make([]string, 0, len(frontier))
			for _, id := range frontier {
				node := known[id]
				terminal := node.Status == domain.StatusDone || node.Status == domain.StatusCancelled
				deferred := terminal && traversal.PreferNonTerminal && id != traversal.ExplicitRootID
				if (!traversal.IncludeTerminal && terminal && !deferred) || (traversal.ExcludeReview && node.Status == domain.StatusReview) {
					continue
				}
				expand = append(expand, id)
			}
			if len(expand) == 0 {
				break
			}
			edges, err := loadGraphTopology(ctx, query, expand, traversal, true, command.Stats)
			if err != nil {
				return err
			}
			next := make(map[string]bool)
			for _, edge := range edges {
				for _, id := range []string{edge.SourceIssueID, edge.TargetIssueID} {
					if _, exists := known[id]; !exists {
						next[id] = true
					}
				}
			}
			frontier = make([]string, 0, len(next))
			for id := range next {
				frontier = append(frontier, id)
			}
			if len(frontier) > 0 {
				if err := loadGraphMetadata(ctx, query, frontier, command.Stats, known); err != nil {
					return err
				}
			}
		}
	}
	ids := make([]string, 0, len(known))
	for id, node := range known {
		ids = append(ids, id)
		snapshot.Nodes = append(snapshot.Nodes, node)
	}
	sort.Slice(snapshot.Nodes, func(left, right int) bool {
		if snapshot.Nodes[left].SequenceNo != snapshot.Nodes[right].SequenceNo {
			return snapshot.Nodes[left].SequenceNo < snapshot.Nodes[right].SequenceNo
		}
		return snapshot.Nodes[left].ID < snapshot.Nodes[right].ID
	})
	for _, node := range snapshot.Nodes {
		if node.ParentID == nil {
			snapshot.TopLevelIssueIDs = append(snapshot.TopLevelIssueIDs, node.ID)
		}
	}
	if snapshot.RootIssueID == nil {
		traversal.RootIssueIDs = snapshot.TopLevelIssueIDs
	}
	if snapshot.RootIssueID == nil || traversal.Depth > 0 {
		var scope []string
		if snapshot.RootIssueID != nil {
			scope = ids
		}
		edges, err := loadGraphTopology(ctx, query, scope, traversal, false, command.Stats)
		if err != nil {
			return err
		}
		for _, edge := range edges {
			_, source := known[edge.SourceIssueID]
			_, target := known[edge.TargetIssueID]
			if source && target {
				snapshot.Edges = append(snapshot.Edges, edge)
			}
		}
	}
	if traversal.PreferNonTerminal {
		predicate := `archived_at IS NULL AND (` + issueClaimableSQLAt(command.Now) + `) AND status NOT IN ('done','cancelled')`
		if traversal.ExcludeReview {
			predicate += ` AND status != 'review'`
		}
		var count int
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM issues WHERE `+predicate).Scan(&count); err != nil {
			return err
		}
		graphRowsRead(command.Stats, 1)
		snapshot.PlanningEntryPointCount = &count
		rows, err := query.QueryContext(ctx, `SELECT id FROM issues WHERE `+predicate+` ORDER BY sequence_no ASC, id ASC LIMIT ?`, traversal.MaxNodes)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			snapshot.PlanningEntryPoints = append(snapshot.PlanningEntryPoints, id)
			graphRowsRead(command.Stats, 1)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	selected := domain.BuildGraph(*snapshot, traversal)
	retained := make(map[string]domain.IssueProjection, len(selected.Nodes))
	for start := 0; start < len(selected.Nodes); start += 500 {
		end := start + 500
		if end > len(selected.Nodes) {
			end = len(selected.Nodes)
		}
		args := make([]any, 0, end-start)
		for _, node := range selected.Nodes[start:end] {
			args = append(args, node.ID)
		}
		rows, err := query.QueryContext(ctx, projectionSQL+` AND id IN (`+graphPlaceholders(len(args))+`)`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			node, err := scanIssueListProjection(rows)
			if err != nil {
				rows.Close()
				return err
			}
			retained[node.ID] = node
			graphRowsRead(command.Stats, 1)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	for index, node := range selected.Nodes {
		selected.Nodes[index] = retained[node.ID]
	}
	if err := loadGraphLabels(ctx, query, selected.Nodes); err != nil {
		return err
	}
	for _, node := range selected.Nodes {
		retained[node.ID] = node
		graphRowsRead(command.Stats, len(node.Labels))
	}
	for index, node := range snapshot.Nodes {
		if hydrated, exists := retained[node.ID]; exists {
			snapshot.Nodes[index] = hydrated
		}
	}
	result := domain.BuildGraph(*snapshot, traversal)
	snapshot.SelectedResult = &result
	if command.Stats != nil {
		command.Stats.HydratedNodes = len(retained)
	}
	return nil
}

func graphPlaceholders(count int) string { return strings.TrimSuffix(strings.Repeat("?,", count), ",") }

func loadGraphMetadata(ctx context.Context, query Queryer, ids []string, stats *ports.GraphLoadStats, known map[string]domain.IssueProjection) error {
	for start := 0; start < len(ids) || (ids == nil && start == 0); start += 500 {
		statement := `SELECT id, sequence_no, status, parent_id FROM issues WHERE archived_at IS NULL`
		args := []any{}
		if ids != nil {
			end := start + 500
			if end > len(ids) {
				end = len(ids)
			}
			for _, id := range ids[start:end] {
				args = append(args, id)
			}
			statement += ` AND id IN (` + graphPlaceholders(len(args)) + `)`
		}
		rows, err := query.QueryContext(ctx, statement, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var node domain.IssueProjection
			var parent sql.NullString
			if err := rows.Scan(&node.ID, &node.SequenceNo, &node.Status, &parent); err != nil {
				rows.Close()
				return err
			}
			if parent.Valid {
				node.ParentID = &parent.String
			}
			known[node.ID] = node
			graphRowsRead(stats, 1)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if ids == nil {
			break
		}
	}
	return nil
}

func loadGraphTopology(ctx context.Context, query Queryer, ids []string, traversal domain.GraphTraversal, frontier bool, stats *ports.GraphLoadStats) ([]domain.GraphEdge, error) {
	edges := []domain.GraphEdge{}
	for start := 0; start < len(ids) || (ids == nil && start == 0); start += 500 {
		var batch []string
		if ids != nil {
			end := start + 500
			if end > len(ids) {
				end = len(ids)
			}
			batch = ids[start:end]
		}
		for _, hierarchy := range []bool{false, true} {
			if hierarchy && !traversal.IncludeHierarchy {
				continue
			}
			if !hierarchy && len(traversal.RelationTypes) == 0 {
				continue
			}
			statement := `SELECT relation.source_issue_id, relation.target_issue_id, relation.type FROM issue_relations AS relation
			 JOIN issues AS source ON source.id = relation.source_issue_id JOIN issues AS target ON target.id = relation.target_issue_id
			 WHERE source.archived_at IS NULL AND target.archived_at IS NULL`
			args := []any{}
			sourceColumn, targetColumn := "relation.source_issue_id", "relation.target_issue_id"
			if hierarchy {
				statement = `SELECT source.id, target.id, 'contains' FROM issues AS target JOIN issues AS source ON source.id = target.parent_id WHERE source.archived_at IS NULL AND target.archived_at IS NULL`
				sourceColumn, targetColumn = "target.parent_id", "target.id"
			} else {
				statement += ` AND relation.type IN (` + graphPlaceholders(len(traversal.RelationTypes)) + `)`
				for _, relationType := range traversal.RelationTypes {
					args = append(args, string(relationType))
				}
			}
			if ids != nil {
				clauses := []string{}
				for _, incoming := range []bool{false, true} {
					if incoming && !frontier {
						continue
					}
					column := sourceColumn
					if incoming {
						column = targetColumn
					}
					clause := column + ` IN (` + graphPlaceholders(len(batch)) + `)`
					wrongDirection := frontier && ((!incoming && traversal.Direction == domain.GraphDirectionIncoming) || (incoming && traversal.Direction == domain.GraphDirectionOutgoing))
					if wrongDirection {
						if hierarchy {
							continue
						}
						clause = "(" + clause + ` AND relation.type = 'related_to')`
					}
					clauses = append(clauses, clause)
					for _, id := range batch {
						args = append(args, id)
					}
				}
				if len(clauses) == 0 {
					continue
				}
				statement += " AND (" + strings.Join(clauses, " OR ") + ")"
			}
			rows, err := query.QueryContext(ctx, statement, args...)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var edge domain.GraphEdge
				if err := rows.Scan(&edge.SourceIssueID, &edge.TargetIssueID, &edge.Type); err != nil {
					rows.Close()
					return nil, err
				}
				edges = append(edges, edge)
				graphRowsRead(stats, 1)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return nil, err
			}
			if err := rows.Close(); err != nil {
				return nil, err
			}
		}
		if ids == nil {
			break
		}
	}
	return edges, nil
}

type graphStatsQueryer struct {
	Queryer
	stats *ports.GraphLoadStats
}

func (query graphStatsQueryer) QueryContext(ctx context.Context, statement string, args ...any) (*sql.Rows, error) {
	query.stats.Queries++
	return query.Queryer.QueryContext(ctx, statement, args...)
}

func (query graphStatsQueryer) QueryRowContext(ctx context.Context, statement string, args ...any) *sql.Row {
	query.stats.Queries++
	return query.Queryer.QueryRowContext(ctx, statement, args...)
}

func graphRowsRead(stats *ports.GraphLoadStats, rows int) {
	if stats != nil {
		stats.RowsRead += rows
	}
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
