package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/pagination"
	"rhizome-mcp/internal/ports"
)

// WorkflowPolicyRepository is the SQLite implementation of
// ports.WorkflowPolicyRepository (docs/02 §17, ISSUE-170).
type WorkflowPolicyRepository struct {
	db *DB
}

const (
	createWorkflowPolicyOperation  = "create_workflow_policy"
	updateWorkflowPolicyOperation  = "update_workflow_policy"
	archiveWorkflowPolicyOperation = "archive_workflow_policy"
)

var _ ports.WorkflowPolicyRepository = (*WorkflowPolicyRepository)(nil)

// NewWorkflowPolicyRepository returns a workflow policy repository backed by database.
func NewWorkflowPolicyRepository(database *DB) (*WorkflowPolicyRepository, error) {
	if database == nil {
		return nil, domain.NewError(domain.CodeStorageConfiguration, "workflow policy database is required", false)
	}
	return &WorkflowPolicyRepository{db: database}, nil
}

// workflowSelectorJSON is the canonical on-disk shape of a PolicySelector.
type workflowSelectorJSON struct {
	IssueTypes []string `json:"issue_types"`
	LabelsAll  []string `json:"labels_all"`
}

// workflowRequirementJSON is the canonical on-disk shape of one requirement.
// PolicyID is omitted: it is implicit (the owning row) when persisted and is
// reattached on load.
type workflowRequirementJSON struct {
	Key         string `json:"key"`
	Kind        string `json:"kind"`
	Field       string `json:"field,omitempty"`
	EvidenceKey string `json:"evidence_key,omitempty"`
	Purpose     string `json:"purpose,omitempty"`
}

func encodeWorkflowSelector(selector domain.PolicySelector) (string, error) {
	issueTypes := make([]string, len(selector.IssueTypes))
	for index, issueType := range selector.IssueTypes {
		issueTypes[index] = string(issueType)
	}
	encoded, err := json.Marshal(workflowSelectorJSON{IssueTypes: issueTypes, LabelsAll: append([]string(nil), selector.LabelsAll...)})
	if err != nil {
		return "", domain.WrapError(err, domain.CodeStorageFailure, "cannot encode policy selector", false)
	}
	return string(encoded), nil
}

func decodeWorkflowSelector(raw string) (domain.PolicySelector, error) {
	var decoded workflowSelectorJSON
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return domain.PolicySelector{}, workflowPolicyCorruptField(err, "selector_json", "INVALID_JSON")
	}
	issueTypes := make([]domain.Type, len(decoded.IssueTypes))
	for index, issueType := range decoded.IssueTypes {
		issueTypes[index] = domain.Type(issueType)
	}
	return domain.PolicySelector{IssueTypes: issueTypes, LabelsAll: decoded.LabelsAll}, nil
}

func encodeWorkflowRequirements(requirements []domain.PolicyRequirementInput) (string, error) {
	encoded := make([]workflowRequirementJSON, len(requirements))
	for index, requirement := range requirements {
		encoded[index] = workflowRequirementJSON{
			Key: requirement.Key, Kind: string(requirement.Kind), Field: requirement.Field,
			EvidenceKey: requirement.EvidenceKey, Purpose: requirement.Purpose,
		}
	}
	data, err := json.Marshal(encoded)
	if err != nil {
		return "", domain.WrapError(err, domain.CodeStorageFailure, "cannot encode policy requirements", false)
	}
	return string(data), nil
}

func decodeWorkflowRequirements(raw string, policyID string) ([]domain.PolicyRequirement, error) {
	var decoded []workflowRequirementJSON
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, workflowPolicyCorruptField(err, "requirements_json", "INVALID_JSON")
	}
	requirements := make([]domain.PolicyRequirement, len(decoded))
	for index, item := range decoded {
		requirements[index] = domain.PolicyRequirement{
			PolicyID: policyID, Key: item.Key, Kind: domain.RequirementKind(item.Kind),
			Field: item.Field, EvidenceKey: item.EvidenceKey, Purpose: item.Purpose,
		}
	}
	return requirements, nil
}

type workflowPolicyEventPayload struct {
	PolicyID     string                    `json:"policy_id"`
	Selector     workflowSelectorJSON      `json:"selector"`
	Requirements []workflowRequirementJSON `json:"requirements"`
	Status       string                    `json:"status"`
	Version      int64                     `json:"version"`
}

func encodeWorkflowPolicyEventPayload(policy domain.WorkflowPolicy) (string, error) {
	issueTypes := make([]string, len(policy.Selector.IssueTypes))
	for index, issueType := range policy.Selector.IssueTypes {
		issueTypes[index] = string(issueType)
	}
	requirements := make([]workflowRequirementJSON, len(policy.Requirements))
	for index, requirement := range policy.Requirements {
		requirements[index] = workflowRequirementJSON{
			Key: requirement.Key, Kind: string(requirement.Kind), Field: requirement.Field,
			EvidenceKey: requirement.EvidenceKey, Purpose: requirement.Purpose,
		}
	}
	payload, err := json.Marshal(workflowPolicyEventPayload{
		PolicyID:     policy.ID,
		Selector:     workflowSelectorJSON{IssueTypes: issueTypes, LabelsAll: policy.Selector.LabelsAll},
		Requirements: requirements, Status: string(policy.Status), Version: policy.Version,
	})
	if err != nil {
		return "", domain.WrapError(err, domain.CodeStorageFailure, "cannot encode workflow policy event", false)
	}
	return string(payload), nil
}

// CreatePolicy validates, persists, and audits a new workflow policy.
func (repository *WorkflowPolicyRepository) CreatePolicy(ctx context.Context, command ports.CreateWorkflowPolicyCommand) (domain.WorkflowPolicy, error) {
	validated, err := command.Input.Validate()
	if err != nil {
		return domain.WorkflowPolicy{}, err
	}
	if _, err := ids.ParseStrict(command.ID); err != nil {
		return domain.WorkflowPolicy{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate policy identifier", false)
	}
	now := command.CreatedAt.UTC()
	timestamp := formatStorageTime(now)
	var policy domain.WorkflowPolicy
	err = repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		if command.IdempotencyKey != "" {
			replayed, found, err := lookupWorkflowPolicyIdempotency(ctx, tx, createWorkflowPolicyOperation, command.IdempotencyKey, command.RequestHash)
			if err != nil {
				return err
			}
			if found {
				policy = replayed
				return nil
			}
		}
		selectorJSON, err := encodeWorkflowSelector(validated.Selector)
		if err != nil {
			return err
		}
		requirementsJSON, err := encodeWorkflowRequirements(validated.Requirements)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_policies(
			id, selector_json, requirements_json, status, version, created_at, updated_at
		) VALUES (?, ?, ?, 'active', 1, ?, ?)`,
			command.ID, selectorJSON, requirementsJSON, timestamp, timestamp); err != nil {
			return err
		}
		policy, err = loadWorkflowPolicy(ctx, tx, command.ID)
		if err != nil {
			return err
		}
		if err := appendWorkflowPolicyEvent(ctx, tx, policy, "policy_created", nil, command.SessionID, timestamp); err != nil {
			return err
		}
		if command.IdempotencyKey != "" {
			if err := storeWorkflowPolicyIdempotency(ctx, tx, createWorkflowPolicyOperation, command.IdempotencyKey, command.RequestHash, policy, timestamp); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domain.WorkflowPolicy{}, err
	}
	return domain.CloneWorkflowPolicy(policy), nil
}

// GetPolicy reads one policy by ID.
func (repository *WorkflowPolicyRepository) GetPolicy(ctx context.Context, command ports.GetWorkflowPolicyCommand) (domain.WorkflowPolicy, error) {
	var policy domain.WorkflowPolicy
	err := repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		loaded, err := loadWorkflowPolicy(ctx, query, command.PolicyID)
		if err != nil {
			return err
		}
		policy = loaded
		return nil
	})
	if err != nil {
		return domain.WorkflowPolicy{}, err
	}
	return domain.CloneWorkflowPolicy(policy), nil
}

type workflowPolicyCursor struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

var workflowPolicyCursorCodec = pagination.NewCodec[workflowPolicyCursor](0)

// ListPolicies returns one deterministic, cursor-paginated policy page,
// newest first, optionally filtered by status.
func (repository *WorkflowPolicyRepository) ListPolicies(ctx context.Context, command ports.ListWorkflowPoliciesCommand) (domain.WorkflowPolicyList, error) {
	input, err := command.Input.Validate()
	if err != nil {
		return domain.WorkflowPolicyList{}, err
	}
	var after *workflowPolicyCursor
	if input.Cursor != "" {
		decoded, err := workflowPolicyCursorCodec.Decode(input.Cursor)
		if err != nil || strings.TrimSpace(decoded.CreatedAt) == "" {
			return domain.WorkflowPolicyList{}, domain.NewError(domain.CodeInvalidArgument, "workflow policy cursor is invalid", false,
				domain.Detail{Field: "cursor", Code: "MALFORMED_CURSOR"})
		}
		after = &decoded
	}
	var result domain.WorkflowPolicyList
	err = repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		where := "1 = 1"
		args := make([]any, 0, 4)
		if input.Status != nil {
			where = "status = ?"
			args = append(args, string(*input.Status))
		}
		statement := `SELECT id, selector_json, requirements_json, status, version, created_at, updated_at
			FROM workflow_policies WHERE ` + where
		if after != nil {
			statement += " AND (created_at < ? OR (created_at = ? AND id < ?))"
			args = append(args, after.CreatedAt, after.CreatedAt, after.ID)
		}
		statement += " ORDER BY created_at DESC, id DESC LIMIT ?"
		args = append(args, input.Limit+1)
		rows, err := query.QueryContext(ctx, statement, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		result.Items = make([]domain.WorkflowPolicy, 0, input.Limit)
		for rows.Next() {
			item, err := scanWorkflowPolicy(rows)
			if err != nil {
				return err
			}
			result.Items = append(result.Items, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(result.Items) > input.Limit {
			result.HasMore = true
			result.Items = result.Items[:input.Limit]
			last := result.Items[len(result.Items)-1]
			cursor, err := workflowPolicyCursorCodec.Encode(workflowPolicyCursor{CreatedAt: formatStorageTime(last.CreatedAt), ID: last.ID})
			if err != nil {
				return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode workflow policy cursor", false)
			}
			result.NextCursor = &cursor
		}
		return nil
	})
	if err != nil {
		return domain.WorkflowPolicyList{}, err
	}
	return domain.CloneWorkflowPolicyList(result), nil
}

// UpdatePolicy optimistically replaces a policy's selector and requirement
// set wholesale (the locked schema has no partial-field patch) and audits
// the change. Mutating an archived policy is rejected: archive is soft and
// irreversible in v1.
func (repository *WorkflowPolicyRepository) UpdatePolicy(ctx context.Context, command ports.UpdateWorkflowPolicyCommand) (domain.WorkflowPolicy, error) {
	validated, err := command.Input.Validate()
	if err != nil {
		return domain.WorkflowPolicy{}, err
	}
	now := command.UpdatedAt.UTC()
	timestamp := formatStorageTime(now)
	var policy domain.WorkflowPolicy
	err = repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		if command.IdempotencyKey != "" {
			replayed, found, err := lookupWorkflowPolicyIdempotency(ctx, tx, updateWorkflowPolicyOperation, command.IdempotencyKey, command.RequestHash)
			if err != nil {
				return err
			}
			if found {
				policy = replayed
				return nil
			}
		}
		current, err := loadWorkflowPolicy(ctx, tx, command.PolicyID)
		if err != nil {
			return err
		}
		if current.Status == domain.PolicyStatusArchived {
			return domain.NewError(domain.CodePolicyArchived, "workflow policy is archived", false)
		}
		if current.Version != command.ExpectedVersion {
			return domain.NewError(domain.CodeVersionConflict, "workflow policy version conflict", true)
		}
		selectorJSON, err := encodeWorkflowSelector(validated.Selector)
		if err != nil {
			return err
		}
		requirementsJSON, err := encodeWorkflowRequirements(validated.Requirements)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE workflow_policies
			SET selector_json = ?, requirements_json = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND version = ?`,
			selectorJSON, requirementsJSON, timestamp, command.PolicyID, command.ExpectedVersion)
		if err != nil {
			return err
		}
		if err := requireSingleRowAffected(result); err != nil {
			return err
		}
		policy, err = loadWorkflowPolicy(ctx, tx, command.PolicyID)
		if err != nil {
			return err
		}
		priorVersion := current.Version
		if err := appendWorkflowPolicyEvent(ctx, tx, policy, "policy_updated", &priorVersion, command.SessionID, timestamp); err != nil {
			return err
		}
		if command.IdempotencyKey != "" {
			if err := storeWorkflowPolicyIdempotency(ctx, tx, updateWorkflowPolicyOperation, command.IdempotencyKey, command.RequestHash, policy, timestamp); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domain.WorkflowPolicy{}, err
	}
	return domain.CloneWorkflowPolicy(policy), nil
}

// ArchivePolicy optimistically, irreversibly archives a policy and audits
// the change. Archiving an already-archived policy is rejected, not a
// silent no-op: retry-safety comes from idempotency_key, matching every
// other mutation in this codebase (e.g. ArchiveIssue).
func (repository *WorkflowPolicyRepository) ArchivePolicy(ctx context.Context, command ports.ArchiveWorkflowPolicyCommand) (domain.WorkflowPolicy, error) {
	now := command.ArchivedAt.UTC()
	timestamp := formatStorageTime(now)
	var policy domain.WorkflowPolicy
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		if command.IdempotencyKey != "" {
			replayed, found, err := lookupWorkflowPolicyIdempotency(ctx, tx, archiveWorkflowPolicyOperation, command.IdempotencyKey, command.RequestHash)
			if err != nil {
				return err
			}
			if found {
				policy = replayed
				return nil
			}
		}
		current, err := loadWorkflowPolicy(ctx, tx, command.PolicyID)
		if err != nil {
			return err
		}
		if current.Status == domain.PolicyStatusArchived {
			return domain.NewError(domain.CodePolicyArchived, "workflow policy is archived", false)
		}
		if current.Version != command.ExpectedVersion {
			return domain.NewError(domain.CodeVersionConflict, "workflow policy version conflict", true)
		}
		result, err := tx.ExecContext(ctx, `UPDATE workflow_policies
			SET status = 'archived', version = version + 1, updated_at = ?
			WHERE id = ? AND version = ?`,
			timestamp, command.PolicyID, command.ExpectedVersion)
		if err != nil {
			return err
		}
		if err := requireSingleRowAffected(result); err != nil {
			return err
		}
		policy, err = loadWorkflowPolicy(ctx, tx, command.PolicyID)
		if err != nil {
			return err
		}
		priorVersion := current.Version
		if err := appendWorkflowPolicyEvent(ctx, tx, policy, "policy_archived", &priorVersion, command.SessionID, timestamp); err != nil {
			return err
		}
		if command.IdempotencyKey != "" {
			if err := storeWorkflowPolicyIdempotency(ctx, tx, archiveWorkflowPolicyOperation, command.IdempotencyKey, command.RequestHash, policy, timestamp); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domain.WorkflowPolicy{}, err
	}
	return domain.CloneWorkflowPolicy(policy), nil
}

func requireSingleRowAffected(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return domain.NewError(domain.CodeVersionConflict, "workflow policy version conflict", true)
	}
	return nil
}

func loadWorkflowPolicy(ctx context.Context, query Queryer, policyID string) (domain.WorkflowPolicy, error) {
	row := query.QueryRowContext(ctx, `SELECT id, selector_json, requirements_json, status, version, created_at, updated_at
		FROM workflow_policies WHERE id = ?`, policyID)
	policy, err := scanWorkflowPolicy(row)
	if err == sql.ErrNoRows {
		return domain.WorkflowPolicy{}, domain.NewError(domain.CodePolicyNotFound, "workflow policy not found", false)
	}
	return policy, err
}

func scanWorkflowPolicy(scanner scanner) (domain.WorkflowPolicy, error) {
	var id, selectorJSON, requirementsJSON, status, createdAt, updatedAt string
	var version int64
	if err := scanner.Scan(&id, &selectorJSON, &requirementsJSON, &status, &version, &createdAt, &updatedAt); err != nil {
		return domain.WorkflowPolicy{}, err
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return domain.WorkflowPolicy{}, workflowPolicyCorruptField(err, "id", "INVALID_ULID")
	}
	selector, err := decodeWorkflowSelector(selectorJSON)
	if err != nil {
		return domain.WorkflowPolicy{}, err
	}
	requirements, err := decodeWorkflowRequirements(requirementsJSON, id)
	if err != nil {
		return domain.WorkflowPolicy{}, err
	}
	policyStatus := domain.PolicyStatus(status)
	if !policyStatus.Valid() {
		return domain.WorkflowPolicy{}, workflowPolicyCorruptField(nil, "status", "INVALID_ENUM")
	}
	if version < 1 {
		return domain.WorkflowPolicy{}, workflowPolicyCorruptField(nil, "version", "INVALID_VALUE")
	}
	created, err := parseIssueTimestamp("created_at", createdAt)
	if err != nil {
		return domain.WorkflowPolicy{}, err
	}
	updated, err := parseIssueTimestamp("updated_at", updatedAt)
	if err != nil {
		return domain.WorkflowPolicy{}, err
	}
	return domain.WorkflowPolicy{
		ID: id, Selector: selector, Status: policyStatus, Version: version,
		Requirements: requirements, CreatedAt: created, UpdatedAt: updated,
	}, nil
}

func appendWorkflowPolicyEvent(ctx context.Context, tx Executor, policy domain.WorkflowPolicy, eventType string, priorVersion *int64, sessionID *string, timestamp string) error {
	payload, err := encodeWorkflowPolicyEventPayload(policy)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_policy_events(
		policy_id, event_type, session_id, prior_version, new_version, payload, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		policy.ID, eventType, nullableStringValuePtr(sessionID), nullableInt64Pointer(priorVersion), policy.Version, payload, timestamp)
	return err
}

func nullableInt64Pointer(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func lookupWorkflowPolicyIdempotency(ctx context.Context, tx Executor, operation, key string, requestHash []byte) (domain.WorkflowPolicy, bool, error) {
	var savedHash []byte
	var savedResponse string
	err := tx.QueryRowContext(ctx, `SELECT request_hash, response_json FROM idempotency_records
		WHERE operation = ? AND idempotency_key = ?`, operation, key).Scan(&savedHash, &savedResponse)
	if err == sql.ErrNoRows {
		return domain.WorkflowPolicy{}, false, nil
	}
	if err != nil {
		return domain.WorkflowPolicy{}, false, err
	}
	if !bytes.Equal(savedHash, requestHash) {
		return domain.WorkflowPolicy{}, false, domain.NewError(domain.CodeIdempotencyConflict, "idempotency key was used with a different request", false,
			domain.Detail{Field: "idempotency_key", Code: domain.CodeIdempotencyConflict})
	}
	var policy domain.WorkflowPolicy
	if err := json.Unmarshal([]byte(savedResponse), &policy); err != nil {
		return domain.WorkflowPolicy{}, false, domain.WrapError(err, domain.CodeStorageCorrupt, "stored idempotency response is invalid", false)
	}
	return policy, true, nil
}

func storeWorkflowPolicyIdempotency(ctx context.Context, tx Executor, operation, key string, requestHash []byte, policy domain.WorkflowPolicy, timestamp string) error {
	response, err := json.Marshal(policy)
	if err != nil {
		return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode workflow policy idempotency response", false)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO idempotency_records(
		idempotency_key, operation, request_hash, response_json, created_at
	) VALUES (?, ?, ?, ?, ?)`, key, operation, requestHash, string(response), timestamp)
	return err
}

// GetAttemptGateSnapshot reads and validates one work attempt's immutable
// frozen gate snapshot.
func (repository *WorkflowPolicyRepository) GetAttemptGateSnapshot(ctx context.Context, command ports.GetAttemptGateSnapshotCommand) (domain.GateSnapshot, error) {
	var snapshot domain.GateSnapshot
	err := repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		loaded, err := loadGateSnapshot(ctx, query, "attempt_gate_snapshots", "attempt_id", command.AttemptID)
		if err != nil {
			return err
		}
		snapshot = loaded
		return nil
	})
	if err != nil {
		return domain.GateSnapshot{}, err
	}
	return domain.CloneGateSnapshot(snapshot), nil
}

// GetReviewTargetGateSnapshot reads and validates one review target's
// immutable frozen gate snapshot.
func (repository *WorkflowPolicyRepository) GetReviewTargetGateSnapshot(ctx context.Context, command ports.GetReviewTargetGateSnapshotCommand) (domain.GateSnapshot, error) {
	var snapshot domain.GateSnapshot
	err := repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		loaded, err := loadGateSnapshot(ctx, query, "review_target_gate_snapshots", "target_id", command.TargetID)
		if err != nil {
			return err
		}
		snapshot = loaded
		return nil
	})
	if err != nil {
		return domain.GateSnapshot{}, err
	}
	return domain.CloneGateSnapshot(snapshot), nil
}

func loadGateSnapshot(ctx context.Context, query Queryer, table, keyColumn, keyValue string) (domain.GateSnapshot, error) {
	row := query.QueryRowContext(ctx, `SELECT requirements_json, source_policies_json, fingerprint, issue_version, created_at
		FROM `+table+` WHERE `+keyColumn+` = ?`, keyValue)
	var requirementsJSON, sourcePoliciesJSON, fingerprint, createdAt string
	var issueVersion int64
	if err := row.Scan(&requirementsJSON, &sourcePoliciesJSON, &fingerprint, &issueVersion, &createdAt); err != nil {
		if err == sql.ErrNoRows {
			return domain.GateSnapshot{}, domain.NewError(domain.CodeGateSnapshotNotFound, "gate snapshot not found", false)
		}
		return domain.GateSnapshot{}, err
	}
	var requirementsDecoded []snapshotRequirementJSON
	if err := json.Unmarshal([]byte(requirementsJSON), &requirementsDecoded); err != nil {
		return domain.GateSnapshot{}, workflowPolicyCorruptField(err, "requirements_json", "INVALID_JSON")
	}
	requirements := make([]domain.PolicyRequirement, len(requirementsDecoded))
	for index, item := range requirementsDecoded {
		requirements[index] = domain.PolicyRequirement{
			// PolicyID is carried inside the snapshot payload itself (unlike
			// workflow_policies rows, a snapshot spans requirements from
			// multiple source policies, so it cannot be implied from the
			// owning row).
			PolicyID: item.PolicyIDValue, Key: item.Key, Kind: domain.RequirementKind(item.Kind),
			Field: item.Field, EvidenceKey: item.EvidenceKey, Purpose: item.Purpose,
		}
	}
	var sourcePolicies []domain.SourcePolicyRef
	if err := json.Unmarshal([]byte(sourcePoliciesJSON), &sourcePolicies); err != nil {
		return domain.GateSnapshot{}, workflowPolicyCorruptField(err, "source_policies_json", "INVALID_JSON")
	}
	if len(fingerprint) != 64 {
		return domain.GateSnapshot{}, workflowPolicyCorruptField(nil, "fingerprint", "INVALID_VALUE")
	}
	if issueVersion < 1 {
		return domain.GateSnapshot{}, workflowPolicyCorruptField(nil, "issue_version", "INVALID_VALUE")
	}
	created, err := parseIssueTimestamp("created_at", createdAt)
	if err != nil {
		return domain.GateSnapshot{}, err
	}
	return domain.GateSnapshot{
		Requirements: requirements, SourcePolicies: sourcePolicies, Fingerprint: fingerprint,
		IssueVersion: issueVersion, CreatedAt: created,
	}, nil
}

// insertAttemptGateSnapshot writes one immutable snapshot row inside the
// caller's own transaction -- per docs/02 §17.6, snapshot rows are inserted
// in the claim transaction itself (ISSUE-172), not through a later service
// call. This is an unexported package-level helper, not part of
// ports.WorkflowPolicyRepository, because it must run inside a transaction
// the caller (attempts.go) already owns.
func insertAttemptGateSnapshot(ctx context.Context, tx Executor, attemptID string, snapshot domain.GateSnapshot) error {
	requirementsJSON, sourcePoliciesJSON, err := encodeGateSnapshotPayload(snapshot)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO attempt_gate_snapshots(
		attempt_id, requirements_json, source_policies_json, fingerprint, issue_version, created_at
	) VALUES (?, ?, ?, ?, ?, ?)`,
		attemptID, requirementsJSON, sourcePoliciesJSON, snapshot.Fingerprint, snapshot.IssueVersion, formatStorageTime(snapshot.CreatedAt.UTC()))
	return err
}

// insertReviewTargetGateSnapshot is insertAttemptGateSnapshot's review-target
// counterpart, for ISSUE-173.
func insertReviewTargetGateSnapshot(ctx context.Context, tx Executor, targetID string, snapshot domain.GateSnapshot) error {
	requirementsJSON, sourcePoliciesJSON, err := encodeGateSnapshotPayload(snapshot)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO review_target_gate_snapshots(
		target_id, requirements_json, source_policies_json, fingerprint, issue_version, created_at
	) VALUES (?, ?, ?, ?, ?, ?)`,
		targetID, requirementsJSON, sourcePoliciesJSON, snapshot.Fingerprint, snapshot.IssueVersion, formatStorageTime(snapshot.CreatedAt.UTC()))
	return err
}

// snapshotRequirementJSON carries PolicyID explicitly (unlike
// workflowRequirementJSON, used for workflow_policies rows where PolicyID is
// implicit): a snapshot's requirements can originate from several source
// policies at once.
type snapshotRequirementJSON struct {
	PolicyIDValue string `json:"policy_id"`
	Key           string `json:"key"`
	Kind          string `json:"kind"`
	Field         string `json:"field,omitempty"`
	EvidenceKey   string `json:"evidence_key,omitempty"`
	Purpose       string `json:"purpose,omitempty"`
}

func encodeGateSnapshotPayload(snapshot domain.GateSnapshot) (requirementsJSON, sourcePoliciesJSON string, err error) {
	requirements := make([]snapshotRequirementJSON, len(snapshot.Requirements))
	for index, requirement := range snapshot.Requirements {
		requirements[index] = snapshotRequirementJSON{
			PolicyIDValue: requirement.PolicyID, Key: requirement.Key, Kind: string(requirement.Kind),
			Field: requirement.Field, EvidenceKey: requirement.EvidenceKey, Purpose: requirement.Purpose,
		}
	}
	requirementsBytes, err := json.Marshal(requirements)
	if err != nil {
		return "", "", domain.WrapError(err, domain.CodeStorageFailure, "cannot encode gate snapshot requirements", false)
	}
	sourcePoliciesBytes, err := json.Marshal(snapshot.SourcePolicies)
	if err != nil {
		return "", "", domain.WrapError(err, domain.CodeStorageFailure, "cannot encode gate snapshot source policies", false)
	}
	return string(requirementsBytes), string(sourcePoliciesBytes), nil
}

func workflowPolicyCorruptField(cause error, field, code string) error {
	return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored workflow policy projection is invalid", false,
		domain.Detail{Field: field, Code: code})
}
