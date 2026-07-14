package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

// RelationRepository is the SQLite implementation of ports.RelationRepository.
type RelationRepository struct {
	db *DB
}

// NewRelationRepository returns a relation repository backed by database.
func NewRelationRepository(database *DB) (*RelationRepository, error) {
	if database == nil {
		return nil, domain.NewError(domain.CodeStorageConfiguration, "relation database is required", false)
	}
	return &RelationRepository{db: database}, nil
}

// ManageIssueRelation atomically resolves endpoints, canonicalizes related_to,
// validates a prospective blocks edge, mutates the relation, and appends
// events. Adding an existing or removing an absent relation is an intentional
// no-op with no event.
func (repository *RelationRepository) ManageIssueRelation(ctx context.Context, command ports.ManageIssueRelationCommand) (ports.ManageIssueRelationResult, error) {
	if !command.Action.Valid() || !command.RelationType.Valid() {
		return ports.ManageIssueRelationResult{}, domain.NewError(domain.CodeInvalidArgument, "relation command is invalid", false)
	}
	if command.Action == domain.RelationActionAdd {
		if _, err := ids.ParseStrict(command.RelationID); err != nil {
			return ports.ManageIssueRelationResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate relation identifier", false)
		}
	} else if command.RelationID != "" {
		return ports.ManageIssueRelationResult{}, domain.NewError(domain.CodeInvalidArgument, "remove relation command must not include an identifier", false)
	}

	now := command.OccurredAt.UTC()
	timestamp := now.Format(time.RFC3339Nano)
	var result ports.ManageIssueRelationResult
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		source, err := loadIssueForMutation(ctx, tx, command.SourceIdentifier)
		if err != nil {
			return err
		}
		target, err := loadIssueForMutation(ctx, tx, command.TargetIdentifier)
		if err != nil {
			return err
		}
		if source.ArchivedAt != nil || target.ArchivedAt != nil {
			return domain.NewError(domain.CodeIssueArchived, "issue is archived", false)
		}
		if source.ID == target.ID {
			return domain.NewError(domain.CodeInvalidArgument,
				"source_issue_id and target_issue_id must identify different issues", false,
				domain.Detail{Field: "target_issue_id", Code: "SELF_RELATION"})
		}
		sourceID, targetID := domain.CanonicalRelationEndpoints(command.RelationType, source.ID, target.ID)
		relation := domain.IssueRelation{
			ID:            command.RelationID,
			SourceIssueID: sourceID,
			TargetIssueID: targetID,
			Type:          command.RelationType,
			CreatedAt:     now,
		}

		switch command.Action {
		case domain.RelationActionAdd:
			existing, found, err := loadRelation(ctx, tx, relation.SourceIssueID, relation.TargetIssueID, relation.Type)
			if err != nil {
				return err
			}
			if found {
				relation = existing
				break
			}
			if relation.Type == domain.RelationTypeBlocks {
				cycle, err := blocksPathExists(ctx, tx, relation.TargetIssueID, relation.SourceIssueID)
				if err != nil {
					return err
				}
				if cycle {
					return domain.NewError(domain.CodeBlocksCycle, "blocks relation would create a cycle", false,
						domain.Detail{Field: "relation_type", Code: domain.CodeBlocksCycle})
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO issue_relations(
				id, source_issue_id, target_issue_id, type, created_by_session_id, created_at
			) VALUES (?, ?, ?, ?, NULL, ?)`,
				relation.ID, relation.SourceIssueID, relation.TargetIssueID, relation.Type, timestamp,
			); err != nil {
				return err
			}
			if err := appendRelationEvents(ctx, tx, "relation_added", relation, timestamp); err != nil {
				return err
			}
			result.Changed = true
		case domain.RelationActionRemove:
			existing, found, err := loadRelation(ctx, tx, relation.SourceIssueID, relation.TargetIssueID, relation.Type)
			if err != nil {
				return err
			}
			if found {
				relation = existing
				if _, err := tx.ExecContext(ctx, `DELETE FROM issue_relations WHERE id = ?`, relation.ID); err != nil {
					return err
				}
				if err := appendRelationEvents(ctx, tx, "relation_removed", relation, timestamp); err != nil {
					return err
				}
				result.Changed = true
			}
		}
		result.Relation = relation
		result.AffectedIssues, err = loadRelationAffectedIssues(ctx, tx, relation.SourceIssueID, relation.TargetIssueID)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return ports.ManageIssueRelationResult{}, err
	}
	return result, nil
}

// blocksPathExists tests whether an existing directed blocks path leads from
// startID to soughtID. UNION makes it safe even if legacy data already contains
// a cycle; the query runs under the writer transaction that inserts the edge.
func blocksPathExists(ctx context.Context, tx Executor, startID, soughtID string) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx, `WITH RECURSIVE reachable(issue_id) AS (
		SELECT target_issue_id
		FROM issue_relations
		WHERE source_issue_id = ? AND type = 'blocks'
		UNION
		SELECT relation.target_issue_id
		FROM issue_relations AS relation
		JOIN reachable ON relation.source_issue_id = reachable.issue_id
		WHERE relation.type = 'blocks'
	)
	SELECT EXISTS(SELECT 1 FROM reachable WHERE issue_id = ?)`, startID, soughtID).Scan(&exists)
	return exists, err
}

func loadRelation(ctx context.Context, tx Executor, sourceID, targetID string, relationType domain.RelationType) (domain.IssueRelation, bool, error) {
	var relation domain.IssueRelation
	var createdAt string
	err := tx.QueryRowContext(ctx, `SELECT id, created_at FROM issue_relations
		WHERE source_issue_id = ? AND target_issue_id = ? AND type = ?`,
		sourceID, targetID, relationType,
	).Scan(&relation.ID, &createdAt)
	if err == sql.ErrNoRows {
		return domain.IssueRelation{
			SourceIssueID: sourceID, TargetIssueID: targetID, Type: relationType,
		}, false, nil
	}
	if err != nil {
		return domain.IssueRelation{}, false, err
	}
	created, err := parseIssueTimestamp("created_at", createdAt)
	if err != nil {
		return domain.IssueRelation{}, false, err
	}
	relation.SourceIssueID = sourceID
	relation.TargetIssueID = targetID
	relation.Type = relationType
	relation.CreatedAt = created
	return relation, true, nil
}

type relationEventPayload struct {
	RelationID    string              `json:"relation_id,omitempty"`
	SourceIssueID string              `json:"source_issue_id"`
	TargetIssueID string              `json:"target_issue_id"`
	RelationType  domain.RelationType `json:"relation_type"`
}

func loadRelationAffectedIssues(ctx context.Context, tx Executor, sourceID, targetID string) ([]domain.IssueProjection, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, sequence_no, type, title, description, acceptance_criteria,
		status, priority, parent_id, blocked_reason, version,
		created_by_session_id, created_at, updated_at, closed_at,
		archived_at, archived_by_session_id,
		`+issueUnresolvedBlockerCountSQL+` AS unresolved_blocker_count,
		`+issueBlockedSQL+` AS is_blocked,
		`+issueClaimableSQL+` AS is_claimable,
		`+issuePriorityRankSQL+` AS priority_rank
		FROM issues WHERE id IN (?, ?) ORDER BY id ASC`, sourceID, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byID := make(map[string]domain.IssueProjection, 2)
	for rows.Next() {
		projection, err := scanIssueListProjection(rows)
		if err != nil {
			return nil, err
		}
		byID[projection.ID] = projection
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]domain.IssueProjection, 0, 2)
	for _, id := range []string{sourceID, targetID} {
		projection, found := byID[id]
		if !found {
			return nil, domain.NewError(domain.CodeStorageCorrupt, "stored relation endpoint is invalid", false)
		}
		result = append(result, projection)
	}
	if err := loadIssueListLabels(ctx, tx, result); err != nil {
		return nil, err
	}
	return result, nil
}

func appendRelationEvents(ctx context.Context, tx Executor, eventType string, relation domain.IssueRelation, timestamp string) error {
	payload, err := json.Marshal(relationEventPayload{
		RelationID: relation.ID, SourceIssueID: relation.SourceIssueID,
		TargetIssueID: relation.TargetIssueID, RelationType: relation.Type,
	})
	if err != nil {
		return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode relation event", false)
	}
	for _, issueID := range []string{relation.SourceIssueID, relation.TargetIssueID} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_events(
			issue_id, event_type, session_id, attempt_id, payload, created_at
		) VALUES (?, ?, NULL, NULL, ?, ?)`, issueID, eventType, string(payload), timestamp); err != nil {
			return err
		}
	}
	return nil
}

var _ ports.RelationRepository = (*RelationRepository)(nil)
