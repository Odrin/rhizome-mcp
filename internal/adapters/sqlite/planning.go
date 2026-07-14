package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

// PlanningRepository implements deterministic snapshot validation and atomic
// application for issue plans.
type PlanningRepository struct{ db *DB }

func NewPlanningRepository(database *DB) (*PlanningRepository, error) {
	if database == nil {
		return nil, domain.NewError(domain.CodeStorageConfiguration, "planning database is required", false)
	}
	return &PlanningRepository{db: database}, nil
}

func (repository *PlanningRepository) ValidateIssuePlan(ctx context.Context, plan domain.IssuePlan) ([]domain.Detail, error) {
	var details []domain.Detail
	err := repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		var err error
		details, err = validatePlanAgainstStore(ctx, query, plan)
		return err
	})
	return details, err
}

// LookupAppliedIssuePlan serves a replay before IDs are allocated. Apply still
// repeats this check in its writer transaction to close the lookup/write race.
func (repository *PlanningRepository) LookupAppliedIssuePlan(ctx context.Context, key string, hash []byte) (ports.ApplyIssuePlanResult, bool, error) {
	var result ports.ApplyIssuePlanResult
	var found bool
	err := repository.db.Read(ctx, func(ctx context.Context, query Queryer) error {
		var savedHash []byte
		var savedResponse string
		err := query.QueryRowContext(ctx, `SELECT request_hash, response_json FROM idempotency_records
			WHERE operation = 'apply_issue_plan' AND idempotency_key = ?`, key).Scan(&savedHash, &savedResponse)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if !bytes.Equal(savedHash, hash) {
			return domain.NewError(domain.CodeIdempotencyConflict, "idempotency key was used with a different request", false,
				domain.Detail{Field: "idempotency_key", Code: domain.CodeIdempotencyConflict})
		}
		if err := json.Unmarshal([]byte(savedResponse), &result); err != nil {
			return domain.WrapError(err, domain.CodeStorageCorrupt, "stored idempotency response is invalid", false)
		}
		found = true
		return nil
	})
	return result, found, err
}

func (repository *PlanningRepository) ApplyIssuePlan(ctx context.Context, command ports.ApplyIssuePlanCommand) (ports.ApplyIssuePlanResult, error) {
	if err := validatePlanCommand(command); err != nil {
		return ports.ApplyIssuePlanResult{}, err
	}
	var result ports.ApplyIssuePlanResult
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		var savedHash []byte
		var savedResponse string
		err := tx.QueryRowContext(ctx, `SELECT request_hash, response_json FROM idempotency_records
			WHERE operation = 'apply_issue_plan' AND idempotency_key = ?`, command.IdempotencyKey).Scan(&savedHash, &savedResponse)
		switch {
		case err == nil:
			if !bytes.Equal(savedHash, command.RequestHash) {
				return domain.NewError(domain.CodeIdempotencyConflict, "idempotency key was used with a different request", false,
					domain.Detail{Field: "idempotency_key", Code: domain.CodeIdempotencyConflict})
			}
			if err := json.Unmarshal([]byte(savedResponse), &result); err != nil {
				return domain.WrapError(err, domain.CodeStorageCorrupt, "stored idempotency response is invalid", false)
			}
			return nil
		case err != sql.ErrNoRows:
			return err
		}
		details, err := validatePlanAgainstStore(ctx, tx, command.Plan)
		if err != nil {
			return err
		}
		if len(details) != 0 {
			return domain.NewError(domain.CodeValidationError, "issue plan is invalid", false, details...)
		}
		result, err = applyPlan(ctx, tx, command)
		if err != nil {
			return err
		}
		response, err := json.Marshal(result)
		if err != nil {
			return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode plan response", false)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO idempotency_records(
			idempotency_key, operation, request_hash, response_json, created_at
		) VALUES (?, 'apply_issue_plan', ?, ?, ?)`,
			command.IdempotencyKey, command.RequestHash, string(response), command.OccurredAt.UTC().Format(time.RFC3339Nano))
		return err
	})
	if err != nil {
		return ports.ApplyIssuePlanResult{}, err
	}
	return result, nil
}

func validatePlanCommand(command ports.ApplyIssuePlanCommand) error {
	if len(command.RequestHash) == 0 || len(command.IssueIDs) != len(command.Plan.Issues) ||
		len(command.RelationIDs) != len(command.Plan.Relations) || len(command.DecisionIDs) != len(command.Plan.Decisions) ||
		len(command.LabelIDs) != len(command.Plan.Issues) {
		return domain.NewError(domain.CodeInvalidArgument, "plan command is invalid", false)
	}
	for _, values := range [][]string{command.IssueIDs, command.RelationIDs, command.DecisionIDs} {
		for _, value := range values {
			if _, err := ids.ParseStrict(value); err != nil {
				return domain.WrapError(err, domain.CodeIDGeneration, "cannot generate plan identifier", false)
			}
		}
	}
	for i, labels := range command.LabelIDs {
		if len(labels) != len(command.Plan.Issues[i].Labels) {
			return domain.NewError(domain.CodeIDGeneration, "cannot generate label identifier", false)
		}
		for _, value := range labels {
			if _, err := ids.ParseStrict(value); err != nil {
				return domain.WrapError(err, domain.CodeIDGeneration, "cannot generate label identifier", false)
			}
		}
	}
	return nil
}

type planReferences struct {
	ids map[string]string // plan local refs and external references to canonical IDs
}

func validatePlanAgainstStore(ctx context.Context, query Queryer, plan domain.IssuePlan) ([]domain.Detail, error) {
	refs := planReferences{ids: make(map[string]string, len(plan.Issues))}
	for i, issue := range plan.Issues {
		if issue.Ref != "" {
			refs.ids[issue.Ref] = "local:" + issue.Ref
		}
		_ = i
	}
	var details []domain.Detail
	resolve := func(value string, index int, field string) (string, bool, error) {
		if id, local := refs.ids[value]; local {
			return id, true, nil
		}
		identifier, err := domain.ParseIssueIdentifier(value)
		if err != nil {
			details = append(details, planStoreDetail(index, field, "INVALID_IDENTIFIER", "must be a local ref, canonical ULID, or ISSUE-N"))
			return "", false, nil
		}
		issue, err := loadIssueForMutation(ctx, query, identifier)
		if err != nil {
			if domainErr, ok := err.(*domain.Error); ok && domainErr.Code == domain.CodeIssueNotFound {
				details = append(details, planStoreDetail(index, field, domain.CodeIssueNotFound, "issue not found"))
				return "", false, nil
			}
			return "", false, err
		}
		if issue.ArchivedAt != nil {
			details = append(details, planStoreDetail(index, field, domain.CodeIssueArchived, "issue is archived"))
			return "", false, nil
		}
		refs.ids[value] = issue.ID
		return issue.ID, false, nil
	}
	for i, issue := range plan.Issues {
		if issue.ParentRef == nil {
			continue
		}
		parentID, local, err := resolve(*issue.ParentRef, i, fmt.Sprintf("issues[%d].parent_ref", i))
		if err != nil {
			return nil, err
		}
		if parentID == "" {
			continue
		}
		if issue.Type == domain.TypeEpic {
			details = append(details, planStoreDetail(i, fmt.Sprintf("issues[%d].parent_ref", i), domain.CodeInvalidEpicParent, "epic issues cannot have a parent"))
			continue
		}
		if local {
			parentRef := *issue.ParentRef
			for _, candidate := range plan.Issues {
				if candidate.Ref == parentRef && candidate.Type != domain.TypeEpic {
					details = append(details, planStoreDetail(i, fmt.Sprintf("issues[%d].parent_ref", i), domain.CodeInvalidEpicParent, "parent must be an epic"))
				}
			}
			continue
		}
		parent, err := loadIssueForMutation(ctx, query, domain.IssueIdentifier{Kind: domain.IssueIdentifierInternalID, Value: parentID})
		if err != nil {
			return nil, err
		}
		if parent.Type != domain.TypeEpic {
			details = append(details, planStoreDetail(i, fmt.Sprintf("issues[%d].parent_ref", i), domain.CodeInvalidEpicParent, "parent must be an epic"))
		}
	}

	blocks, err := loadBlocksEdges(ctx, query)
	if err != nil {
		return nil, err
	}
	proposed := make(map[string]bool)
	for i, relation := range plan.Relations {
		source, _, err := resolve(relation.SourceRef, i, fmt.Sprintf("relations[%d].source_ref", i))
		if err != nil {
			return nil, err
		}
		target, _, err := resolve(relation.TargetRef, i, fmt.Sprintf("relations[%d].target_ref", i))
		if err != nil {
			return nil, err
		}
		if source == "" || target == "" {
			continue
		}
		if source == target {
			details = append(details, planStoreDetail(i, fmt.Sprintf("relations[%d].target_ref", i), "SELF_RELATION", "endpoints must differ"))
			continue
		}
		if relation.Type == domain.RelationTypeRelatedTo && source > target {
			source, target = target, source
		}
		key := string(relation.Type) + "\x00" + source + "\x00" + target
		if proposed[key] {
			details = append(details, planStoreDetail(i, fmt.Sprintf("relations[%d]", i), "DUPLICATE_RELATION", "duplicate relation"))
			continue
		}
		proposed[key] = true
		var existing bool
		if err := query.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_relations
			WHERE source_issue_id = ? AND target_issue_id = ? AND type = ?)`, source, target, relation.Type).Scan(&existing); err != nil {
			return nil, err
		}
		if existing {
			details = append(details, planStoreDetail(i, fmt.Sprintf("relations[%d]", i), "DUPLICATE_RELATION", "relation already exists"))
			continue
		}
		if relation.Type == domain.RelationTypeBlocks {
			if planPathExists(blocks, target, source) {
				details = append(details, planStoreDetail(i, fmt.Sprintf("relations[%d].type", i), domain.CodeBlocksCycle, "blocks relation would create a cycle"))
				continue
			}
			blocks = append(blocks, struct{ source, target string }{source, target})
		}
	}
	for i, decision := range plan.Decisions {
		if decision.IssueRef == nil {
			continue
		}
		if _, _, err := resolve(*decision.IssueRef, i, fmt.Sprintf("decisions[%d].issue_ref", i)); err != nil {
			return nil, err
		}
	}
	domain.SortDetails(details)
	return details, nil
}

func planStoreDetail(index int, field, code, message string) domain.Detail {
	return domain.Detail{EntityIndex: &index, Field: field, Code: code, Message: message}
}

func loadBlocksEdges(ctx context.Context, query Queryer) ([]struct{ source, target string }, error) {
	rows, err := query.QueryContext(ctx, `SELECT source_issue_id, target_issue_id FROM issue_relations
		WHERE type = 'blocks' ORDER BY source_issue_id ASC, target_issue_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	edges := make([]struct{ source, target string }, 0)
	for rows.Next() {
		var edge struct{ source, target string }
		if err := rows.Scan(&edge.source, &edge.target); err != nil {
			return nil, err
		}
		edges = append(edges, edge)
	}
	return edges, rows.Err()
}

func planPathExists(edges []struct{ source, target string }, start, sought string) bool {
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) != 0 {
		current := queue[0]
		queue = queue[1:]
		for _, edge := range edges {
			if edge.source != current || seen[edge.target] {
				continue
			}
			if edge.target == sought {
				return true
			}
			seen[edge.target] = true
			queue = append(queue, edge.target)
		}
	}
	return false
}

func applyPlan(ctx context.Context, tx Executor, command ports.ApplyIssuePlanCommand) (ports.ApplyIssuePlanResult, error) {
	now := command.OccurredAt.UTC()
	timestamp := now.Format(time.RFC3339Nano)
	localIDs := make(map[string]string, len(command.Plan.Issues))
	for i, issue := range command.Plan.Issues {
		if issue.Ref != "" {
			localIDs[issue.Ref] = command.IssueIDs[i]
		}
	}
	resolveID := func(value string) (string, error) {
		if id, ok := localIDs[value]; ok {
			return id, nil
		}
		identifier, err := domain.ParseIssueIdentifier(value)
		if err != nil {
			return "", err
		}
		issue, err := loadIssueForMutation(ctx, tx, identifier)
		return issue.ID, err
	}
	result := ports.ApplyIssuePlanResult{
		CreatedIssues: []ports.CreatedPlanIssue{}, CreatedRelations: []domain.IssueRelation{}, CreatedDecisions: []ports.Decision{},
	}
	for i, planned := range command.Plan.Issues {
		var parentID *string
		if planned.ParentRef != nil {
			id, err := resolveID(*planned.ParentRef)
			if err != nil {
				return result, err
			}
			parentID = &id
		}
		var sequence int64
		if err := tx.QueryRowContext(ctx, `UPDATE projects SET next_issue_number = next_issue_number + 1, updated_at = ?
			RETURNING next_issue_number - 1`, timestamp).Scan(&sequence); err != nil {
			if err == sql.ErrNoRows {
				return result, domain.NewError(domain.CodeProjectNotInitialized, "project database is not initialized", false)
			}
			return result, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, description, acceptance_criteria,
			status, priority, parent_id, blocked_reason, version, created_by_session_id, created_at, updated_at, closed_at, archived_at, archived_by_session_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, NULL, ?, ?, NULL, NULL, NULL)`,
			command.IssueIDs[i], sequence, planned.Type, planned.Title, nullableString(planned.Description), nullableString(planned.AcceptanceCriteria),
			planned.Status, planned.Priority, nullableString(parentID), nullableString(planned.BlockedReason), timestamp, timestamp); err != nil {
			return result, err
		}
		labels, err := resolveIssueLabels(ctx, tx, planned.Labels, planned.CreateMissingLabels, command.LabelIDs[i], now)
		if err != nil {
			return result, err
		}
		if err := replaceIssueLabels(ctx, tx, command.IssueIDs[i], labels); err != nil {
			return result, err
		}
		payload, err := json.Marshal(issueCreatedPayload{SequenceNo: sequence, Type: planned.Type, Status: planned.Status, Priority: planned.Priority, ParentID: parentID, Labels: labelNames(labels)})
		if err != nil {
			return result, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_type, session_id, attempt_id, payload, created_at)
			VALUES (?, 'issue_created', NULL, NULL, ?, ?)`, command.IssueIDs[i], string(payload), timestamp); err != nil {
			return result, err
		}
		result.CreatedIssues = append(result.CreatedIssues, ports.CreatedPlanIssue{Ref: planned.Ref, Issue: domain.Issue{
			ID: command.IssueIDs[i], DisplayID: fmt.Sprintf("ISSUE-%d", sequence), SequenceNo: sequence, Type: planned.Type, Title: planned.Title,
			Description: planned.Description, AcceptanceCriteria: planned.AcceptanceCriteria, Status: planned.Status, Priority: planned.Priority,
			ParentID: parentID, BlockedReason: planned.BlockedReason, Version: 1, CreatedAt: now, UpdatedAt: now, Labels: labels,
		}})
	}
	for i, planned := range command.Plan.Relations {
		source, err := resolveID(planned.SourceRef)
		if err != nil {
			return result, err
		}
		target, err := resolveID(planned.TargetRef)
		if err != nil {
			return result, err
		}
		source, target = domain.CanonicalRelationEndpoints(planned.Type, source, target)
		relation := domain.IssueRelation{ID: command.RelationIDs[i], SourceIssueID: source, TargetIssueID: target, Type: planned.Type, CreatedAt: now}
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_relations(id, source_issue_id, target_issue_id, type, created_by_session_id, created_at)
			VALUES (?, ?, ?, ?, NULL, ?)`, relation.ID, source, target, relation.Type, timestamp); err != nil {
			return result, err
		}
		if err := appendRelationEvents(ctx, tx, "relation_added", relation, timestamp); err != nil {
			return result, err
		}
		result.CreatedRelations = append(result.CreatedRelations, relation)
	}
	for i, planned := range command.Plan.Decisions {
		var issueID *string
		if planned.IssueRef != nil {
			id, err := resolveID(*planned.IssueRef)
			if err != nil {
				return result, err
			}
			issueID = &id
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO decisions(id, issue_id, title, summary, content, status, supersedes_id, created_by_session_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?)`, command.DecisionIDs[i], nullableString(issueID), planned.Title, planned.Summary, planned.Content, planned.Status, timestamp); err != nil {
			return result, err
		}
		payload, err := json.Marshal(map[string]string{"decision_id": command.DecisionIDs[i], "status": planned.Status})
		if err != nil {
			return result, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_type, session_id, attempt_id, payload, created_at)
			VALUES (?, 'decision_recorded', NULL, NULL, ?, ?)`, nullableString(issueID), string(payload), timestamp); err != nil {
			return result, err
		}
		result.CreatedDecisions = append(result.CreatedDecisions, ports.Decision{ID: command.DecisionIDs[i], IssueID: issueID, Title: planned.Title, Summary: planned.Summary, Content: planned.Content, Status: planned.Status, CreatedAt: now})
	}
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM issue_events").Scan(&result.LatestEventID); err != nil {
		return result, err
	}
	return result, nil
}

var _ ports.PlanningRepository = (*PlanningRepository)(nil)
