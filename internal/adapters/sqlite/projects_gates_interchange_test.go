package sqlite_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
)

// TestProjectRepositoryRoundTripsGateStateThroughExtensionsNamespace pins
// ISSUE-175 AC3: every durable gate entity -- policy, policy event, both
// snapshot kinds, evidence, evidence event, purpose-scoped approval, and
// non-default review purposes -- survives export and import, with reference
// columns remapped and frozen blobs carried verbatim.
func TestProjectRepositoryRoundTripsGateStateThroughExtensionsNamespace(t *testing.T) {
	db, now := openProjectDatabase(t, "Gates", "Instructions")
	ctx := context.Background()

	const (
		issueID    = "01ARZ3NDEKTSV4RRFFQ69G5GA1"
		attemptID  = "01ARZ3NDEKTSV4RRFFQ69G5GA2"
		policyID   = "01ARZ3NDEKTSV4RRFFQ69G5GA3"
		targetID   = "01ARZ3NDEKTSV4RRFFQ69G5GA4"
		requestID  = "01ARZ3NDEKTSV4RRFFQ69G5GA5"
		evidenceID = "01ARZ3NDEKTSV4RRFFQ69G5GA6"
		approvalID = "01ARZ3NDEKTSV4RRFFQ69G5GA7"
	)
	fingerprint := strings.Repeat("ab", 32)
	requirementsBlob := `[{"policy_id":"` + policyID + `","key":"impl","kind":"attempt_evidence","evidence_key":"impl"}]`
	sourcePoliciesBlob := `[{"policy_id":"` + policyID + `","version":1}]`
	later := now.Add(2 * time.Second)

	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		statements := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at) VALUES (?, 1, 'task', 'Gated issue', 'done', 'medium', 1, ?, ?)`,
				[]any{issueID, sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(now)}},
			{`INSERT INTO work_attempts(id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start, lease_token_hash, lease_expires_at, started_at, last_heartbeat_at, finished_at, result_summary, next_steps_json, verification_json) VALUES (?, ?, 'work', 'completed', 1, 0, X'03', ?, ?, ?, ?, 'done', '[]', '[]')`,
				[]any{attemptID, issueID, sqlite.FormatStorageTime(later), sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(later), sqlite.FormatStorageTime(later)}},
			{`INSERT INTO workflow_policies(id, selector_json, requirements_json, status, version, created_at, updated_at) VALUES (?, '{"issue_types":["task"]}', '[{"key":"impl","kind":"attempt_evidence","evidence_key":"impl"}]', 'active', 2, ?, ?)`,
				[]any{policyID, sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(later)}},
			{`INSERT INTO workflow_policy_events(policy_id, event_type, session_id, prior_version, new_version, payload, created_at) VALUES (?, 'policy_created', NULL, NULL, 1, '{}', ?)`,
				[]any{policyID, sqlite.FormatStorageTime(now)}},
			{`INSERT INTO workflow_policy_events(policy_id, event_type, session_id, prior_version, new_version, payload, created_at) VALUES (?, 'policy_updated', NULL, 1, 2, '{}', ?)`,
				[]any{policyID, sqlite.FormatStorageTime(later)}},
			{`INSERT INTO attempt_gate_snapshots(attempt_id, requirements_json, source_policies_json, fingerprint, issue_version, created_at) VALUES (?, ?, ?, ?, 1, ?)`,
				[]any{attemptID, requirementsBlob, sourcePoliciesBlob, fingerprint, sqlite.FormatStorageTime(now)}},
			{`INSERT INTO gate_evidence(id, attempt_id, issue_id, key, result, summary, details, artifact_ids_json, version, created_at, updated_at) VALUES (?, ?, ?, 'impl', 'satisfied', 'Implemented', 'details text', '[]', 1, ?, ?)`,
				[]any{evidenceID, attemptID, issueID, sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(later)}},
			{`INSERT INTO gate_evidence_events(evidence_id, attempt_id, issue_id, key, event_type, version, payload, created_at) VALUES (?, ?, ?, 'impl', 'evidence_submitted', 1, '{}', ?)`,
				[]any{evidenceID, attemptID, issueID, sqlite.FormatStorageTime(now)}},
			{`INSERT INTO review_targets(id, issue_id, issue_version, latest_event_id, artifact_ids_json, purposes_json, version, created_at) VALUES (?, ?, 1, 0, '[]', '["implementation","security"]', 1, ?)`,
				[]any{targetID, issueID, sqlite.FormatStorageTime(now)}},
			{`INSERT INTO review_target_gate_snapshots(target_id, requirements_json, source_policies_json, fingerprint, issue_version, created_at) VALUES (?, ?, ?, ?, 1, ?)`,
				[]any{targetID, requirementsBlob, sourcePoliciesBlob, fingerprint, sqlite.FormatStorageTime(now)}},
			{`INSERT INTO review_requests(id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, purposes_json, status, supersedes_id, active_attempt_id, version, created_at, resolved_at) VALUES (?, ?, ?, 1, 0, '[]', '["implementation","security"]', 'approved', NULL, NULL, 1, ?, ?)`,
				[]any{requestID, targetID, issueID, sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(later)}},
			{`INSERT INTO review_approvals(id, issue_id, target_id, request_id, attempt_id, purpose, target_issue_version, target_event_id, version, created_at) VALUES (?, ?, ?, ?, ?, 'security', 1, 0, 1, ?)`,
				[]any{approvalID, issueID, targetID, requestID, attemptID, sqlite.FormatStorageTime(later)}},
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed gate fixture: %v", err)
	}

	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	exported, err := repository.ExportLogicalProject(ctx)
	if err != nil {
		t.Fatalf("ExportLogicalProject() error = %v", err)
	}

	gates, err := exported.DecodeGatesExtension()
	if err != nil {
		t.Fatalf("DecodeGatesExtension() error = %v", err)
	}
	if len(gates.Policies) != 1 || len(gates.PolicyEvents) != 2 || len(gates.AttemptSnapshots) != 1 ||
		len(gates.ReviewTargetSnapshots) != 1 || len(gates.Evidence) != 1 || len(gates.EvidenceEvents) != 1 ||
		len(gates.ReviewApprovals) != 1 {
		t.Fatalf("exported gates extension = %#v, want one of each entity and two policy events", gates)
	}
	if gates.Policies[0].ID != policyID || gates.Policies[0].Status != "active" || gates.Policies[0].Version != 2 {
		t.Fatalf("exported policy = %#v", gates.Policies[0])
	}
	if gates.PolicyEvents[0].EventType != "policy_created" || gates.PolicyEvents[0].PriorVersion != nil {
		t.Fatalf("first policy event = %#v, want policy_created without prior version", gates.PolicyEvents[0])
	}
	if gates.PolicyEvents[1].EventType != "policy_updated" || gates.PolicyEvents[1].PriorVersion == nil || *gates.PolicyEvents[1].PriorVersion != 1 {
		t.Fatalf("second policy event = %#v, want policy_updated from version 1", gates.PolicyEvents[1])
	}
	if gates.AttemptSnapshots[0].Fingerprint != fingerprint || string(gates.AttemptSnapshots[0].RequirementsJSON) != requirementsBlob {
		t.Fatalf("attempt snapshot = %#v, want the frozen blob verbatim", gates.AttemptSnapshots[0])
	}
	if gates.Evidence[0].Key != "impl" || gates.Evidence[0].Result != "satisfied" || gates.Evidence[0].Details == nil {
		t.Fatalf("evidence = %#v", gates.Evidence[0])
	}
	if gates.ReviewApprovals[0].Purpose != "security" || gates.ReviewApprovals[0].RequestID != requestID {
		t.Fatalf("approval = %#v", gates.ReviewApprovals[0])
	}
	if len(exported.ReviewTargets) != 1 || len(exported.ReviewTargets[0].Purposes) != 2 {
		t.Fatalf("review target purposes = %#v, want the non-default list carried", exported.ReviewTargets)
	}
	if len(exported.ReviewRequests) != 1 || len(exported.ReviewRequests[0].Purposes) != 2 {
		t.Fatalf("review request purposes = %#v, want the non-default list carried", exported.ReviewRequests)
	}

	data, err := domain.MarshalLogicalProjectDocument(exported)
	if err != nil {
		t.Fatalf("MarshalLogicalProjectDocument() error = %v", err)
	}
	plan, err := domain.ParseLogicalProjectImportPlan(data)
	if err != nil {
		t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
	}
	counts := plan.DryRun.Counts
	if counts.WorkflowPolicies != 1 || counts.WorkflowPolicyEvents != 2 || counts.AttemptGateSnapshots != 1 ||
		counts.ReviewTargetGateSnapshots != 1 || counts.GateEvidence != 1 || counts.GateEvidenceEvents != 1 ||
		counts.ReviewApprovals != 1 {
		t.Fatalf("dry run gate counts = %+v", counts)
	}
	plan = assignImportDestinationIDs(t, plan)

	destDB, _ := openProjectDatabase(t, "Gates destination", "Instructions")
	destRepository, err := sqlite.NewProjectRepository(destDB)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	if _, err := destRepository.ApplyLogicalProjectImport(ctx, plan); err != nil {
		t.Fatalf("ApplyLogicalProjectImport() error = %v", err)
	}

	destPolicyID := plan.DestinationIDs.WorkflowPolicyIDs[policyID]
	destAttemptID := plan.DestinationIDs.AttemptIDs[attemptID]
	destTargetID := plan.DestinationIDs.ReviewTargetIDs[targetID]
	destRequestID := plan.DestinationIDs.ReviewRequestIDs[requestID]
	if destPolicyID == "" || destPolicyID == policyID {
		t.Fatalf("destination policy id not remapped: %q", destPolicyID)
	}

	if err := destDB.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		var policyStatus string
		var policyVersion int64
		if err := query.QueryRowContext(ctx, `SELECT status, version FROM workflow_policies WHERE id = ?`, destPolicyID).Scan(&policyStatus, &policyVersion); err != nil {
			t.Fatalf("imported policy row: %v", err)
		}
		if policyStatus != "active" || policyVersion != 2 {
			t.Fatalf("imported policy = %s v%d", policyStatus, policyVersion)
		}
		var eventCount int64
		if err := query.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_policy_events WHERE policy_id = ?`, destPolicyID).Scan(&eventCount); err != nil {
			t.Fatalf("imported policy events: %v", err)
		}
		if eventCount != 2 {
			t.Fatalf("imported policy events = %d, want 2", eventCount)
		}
		var storedFingerprint, storedRequirements string
		if err := query.QueryRowContext(ctx, `SELECT fingerprint, requirements_json FROM attempt_gate_snapshots WHERE attempt_id = ?`, destAttemptID).Scan(&storedFingerprint, &storedRequirements); err != nil {
			t.Fatalf("imported attempt snapshot: %v", err)
		}
		if storedFingerprint != fingerprint {
			t.Fatalf("imported snapshot fingerprint = %q, want the frozen %q", storedFingerprint, fingerprint)
		}
		if !strings.Contains(storedRequirements, policyID) || strings.Contains(storedRequirements, destPolicyID) {
			t.Fatalf("imported snapshot requirements = %q, want the source-document policy identity carried verbatim", storedRequirements)
		}
		var targetPurposes, requestPurposes string
		if err := query.QueryRowContext(ctx, `SELECT purposes_json FROM review_targets WHERE id = ?`, destTargetID).Scan(&targetPurposes); err != nil {
			t.Fatalf("imported target purposes: %v", err)
		}
		if err := query.QueryRowContext(ctx, `SELECT purposes_json FROM review_requests WHERE id = ?`, destRequestID).Scan(&requestPurposes); err != nil {
			t.Fatalf("imported request purposes: %v", err)
		}
		var parsed []string
		if err := json.Unmarshal([]byte(targetPurposes), &parsed); err != nil || len(parsed) != 2 || parsed[1] != "security" {
			t.Fatalf("imported target purposes = %q", targetPurposes)
		}
		if err := json.Unmarshal([]byte(requestPurposes), &parsed); err != nil || len(parsed) != 2 || parsed[1] != "security" {
			t.Fatalf("imported request purposes = %q", requestPurposes)
		}
		var targetSnapshotFingerprint string
		if err := query.QueryRowContext(ctx, `SELECT fingerprint FROM review_target_gate_snapshots WHERE target_id = ?`, destTargetID).Scan(&targetSnapshotFingerprint); err != nil {
			t.Fatalf("imported target snapshot: %v", err)
		}
		if targetSnapshotFingerprint != fingerprint {
			t.Fatalf("imported target snapshot fingerprint = %q, want the carried snapshot, not a backfill", targetSnapshotFingerprint)
		}
		var evidenceKey, evidenceResult, evidenceAttemptID string
		if err := query.QueryRowContext(ctx, `SELECT key, result, attempt_id FROM gate_evidence`).Scan(&evidenceKey, &evidenceResult, &evidenceAttemptID); err != nil {
			t.Fatalf("imported evidence: %v", err)
		}
		if evidenceKey != "impl" || evidenceResult != "satisfied" || evidenceAttemptID != destAttemptID {
			t.Fatalf("imported evidence = key=%q result=%q attempt=%q", evidenceKey, evidenceResult, evidenceAttemptID)
		}
		var evidenceEventCount int64
		if err := query.QueryRowContext(ctx, `SELECT COUNT(*) FROM gate_evidence_events`).Scan(&evidenceEventCount); err != nil {
			t.Fatalf("imported evidence events: %v", err)
		}
		if evidenceEventCount != 1 {
			t.Fatalf("imported evidence events = %d, want 1", evidenceEventCount)
		}
		var approvalPurpose, approvalRequestID string
		if err := query.QueryRowContext(ctx, `SELECT purpose, request_id FROM review_approvals`).Scan(&approvalPurpose, &approvalRequestID); err != nil {
			t.Fatalf("imported approval: %v", err)
		}
		if approvalPurpose != "security" || approvalRequestID != destRequestID {
			t.Fatalf("imported approval = purpose=%q request=%q", approvalPurpose, approvalRequestID)
		}
		return nil
	}); err != nil {
		t.Fatalf("read imported gate rows: %v", err)
	}
}

// TestProjectRepositoryGateFreeExportAndPreGatesImportStayCompatible pins
// the two compatibility promises: a project with no gate state exports a
// document without the gates namespace and without purposes fields, and
// importing a pre-gates document produces implementation-only purposes plus
// the sentinel empty target snapshot the migration backfill would have
// written -- so behaviour is unchanged (an absent snapshot already
// evaluated as zero requirements).
func TestProjectRepositoryGateFreeExportAndPreGatesImportStayCompatible(t *testing.T) {
	db, now := openProjectDatabase(t, "No gates", "Instructions")
	ctx := context.Background()

	const (
		issueID  = "01ARZ3NDEKTSV4RRFFQ69G5GB1"
		targetID = "01ARZ3NDEKTSV4RRFFQ69G5GB2"
	)
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at) VALUES (?, 1, 'task', 'Plain issue', 'ready', 'medium', 1, ?, ?)`,
			issueID, sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(now)); err != nil {
			return err
		}
		// Default purposes and an (empty backfill) snapshot: the shapes a
		// pre-gates project holds after migration 012.
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_targets(id, issue_id, issue_version, latest_event_id, artifact_ids_json, version, created_at) VALUES (?, ?, 1, 0, '[]', 1, ?)`,
			targetID, issueID, sqlite.FormatStorageTime(now)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO review_target_gate_snapshots(target_id, requirements_json, source_policies_json, fingerprint, issue_version, created_at) VALUES (?, '[]', '[]', ?, 1, ?)`,
			targetID, strings.Repeat("0", 64), sqlite.FormatStorageTime(now))
		return err
	}); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}

	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	exported, err := repository.ExportLogicalProject(ctx)
	if err != nil {
		t.Fatalf("ExportLogicalProject() error = %v", err)
	}
	if len(exported.ReviewTargets) != 1 || exported.ReviewTargets[0].Purposes != nil {
		t.Fatalf("review target purposes = %#v, want omitted for the default", exported.ReviewTargets)
	}
	// The backfill snapshot is real gate state, so the namespace is present
	// for this project; a document with NO gate rows at all must omit it.
	// Simulate the pre-gates document by dropping the namespace and the
	// snapshot from the exported document, as an older build would have
	// produced.
	preGates := exported
	preGates.Extensions = nil
	data, err := domain.MarshalLogicalProjectDocument(preGates)
	if err != nil {
		t.Fatalf("MarshalLogicalProjectDocument() error = %v", err)
	}
	plan, err := domain.ParseLogicalProjectImportPlan(data)
	if err != nil {
		t.Fatalf("ParseLogicalProjectImportPlan(pre-gates) error = %v", err)
	}
	if plan.DryRun.Counts.WorkflowPolicies != 0 || plan.DryRun.Counts.ReviewTargetGateSnapshots != 0 {
		t.Fatalf("pre-gates dry run gate counts = %+v, want all zero", plan.DryRun.Counts)
	}
	plan = assignImportDestinationIDs(t, plan)

	destDB, _ := openProjectDatabase(t, "No gates destination", "Instructions")
	destRepository, err := sqlite.NewProjectRepository(destDB)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	if _, err := destRepository.ApplyLogicalProjectImport(ctx, plan); err != nil {
		t.Fatalf("ApplyLogicalProjectImport(pre-gates) error = %v", err)
	}

	destTargetID := plan.DestinationIDs.ReviewTargetIDs[targetID]
	if err := destDB.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		var policyCount int64
		if err := query.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_policies`).Scan(&policyCount); err != nil {
			t.Fatalf("imported policies: %v", err)
		}
		if policyCount != 0 {
			t.Fatalf("imported policies = %d, want 0 from a pre-gates document", policyCount)
		}
		var purposesJSON string
		if err := query.QueryRowContext(ctx, `SELECT purposes_json FROM review_targets WHERE id = ?`, destTargetID).Scan(&purposesJSON); err != nil {
			t.Fatalf("imported target purposes: %v", err)
		}
		if purposesJSON != `["implementation"]` {
			t.Fatalf("imported target purposes = %q, want the implementation-only default", purposesJSON)
		}
		var storedFingerprint, storedRequirements string
		if err := query.QueryRowContext(ctx, `SELECT fingerprint, requirements_json FROM review_target_gate_snapshots WHERE target_id = ?`, destTargetID).Scan(&storedFingerprint, &storedRequirements); err != nil {
			t.Fatalf("backfilled target snapshot: %v", err)
		}
		if storedFingerprint != strings.Repeat("0", 64) || storedRequirements != "[]" {
			t.Fatalf("backfilled snapshot = fingerprint %q requirements %q, want the migration sentinel", storedFingerprint, storedRequirements)
		}
		return nil
	}); err != nil {
		t.Fatalf("read imported rows: %v", err)
	}
}

// TestProjectRepositoryRemapsEventCursorsOnImport is ISSUE-231's regression.
// Imported events get fresh destination IDs, but every durable cursor that
// names an event-log position used to be restored verbatim. A source project
// whose log had run past the destination's (here: source IDs 900 and 4100
// replayed into a two-event destination) therefore left cursors above every
// ID the destination could ever assign, so the "did anything happen after
// this position" question they exist to answer stayed answered "no" forever.
func TestProjectRepositoryRemapsEventCursorsOnImport(t *testing.T) {
	db, now := openProjectDatabase(t, "Cursors", "Instructions")
	ctx := context.Background()

	const (
		issueID    = "01ARZ3NDEKTSV4RRFFQ69G5HA1"
		attemptID  = "01ARZ3NDEKTSV4RRFFQ69G5HA2"
		targetID   = "01ARZ3NDEKTSV4RRFFQ69G5HA3"
		requestID  = "01ARZ3NDEKTSV4RRFFQ69G5HA4"
		approvalID = "01ARZ3NDEKTSV4RRFFQ69G5HA5"
		// Deliberately non-contiguous and far above anything an empty
		// destination will assign.
		firstSourceEventID  = 900
		secondSourceEventID = 4100
	)
	later := now.Add(2 * time.Second)

	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		statements := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at) VALUES (?, 1, 'task', 'Cursor issue', 'review', 'medium', 2, ?, ?)`,
				[]any{issueID, sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(later)}},
			{`INSERT INTO issue_events(id, issue_id, event_type, payload, created_at, source) VALUES (?, ?, 'issue_created', '{}', ?, 'issue')`,
				[]any{firstSourceEventID, issueID, sqlite.FormatStorageTime(now)}},
			{`INSERT INTO work_attempts(id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start, lease_token_hash, lease_expires_at, started_at, last_heartbeat_at, finished_at, result_summary, next_steps_json, verification_json) VALUES (?, ?, 'work', 'completed', 1, ?, X'04', ?, ?, ?, ?, 'done', '[]', '[]')`,
				[]any{attemptID, issueID, firstSourceEventID, sqlite.FormatStorageTime(later), sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(later), sqlite.FormatStorageTime(later)}},
			{`INSERT INTO issue_events(id, issue_id, event_type, payload, created_at, source) VALUES (?, ?, 'issue_updated', '{}', ?, 'issue')`,
				[]any{secondSourceEventID, issueID, sqlite.FormatStorageTime(later)}},
			{`INSERT INTO review_targets(id, issue_id, issue_version, latest_event_id, artifact_ids_json, purposes_json, version, created_at) VALUES (?, ?, 2, ?, '[]', '["implementation"]', 1, ?)`,
				[]any{targetID, issueID, secondSourceEventID, sqlite.FormatStorageTime(later)}},
			{`INSERT INTO review_requests(id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, purposes_json, status, supersedes_id, active_attempt_id, version, created_at, resolved_at) VALUES (?, ?, ?, 2, ?, '[]', '["implementation"]', 'open', NULL, NULL, 1, ?, NULL)`,
				[]any{requestID, targetID, issueID, secondSourceEventID, sqlite.FormatStorageTime(later)}},
			{`INSERT INTO review_approvals(id, issue_id, target_id, request_id, attempt_id, purpose, target_issue_version, target_event_id, version, created_at) VALUES (?, ?, ?, ?, ?, 'implementation', 2, ?, 1, ?)`,
				[]any{approvalID, issueID, targetID, requestID, attemptID, secondSourceEventID, sqlite.FormatStorageTime(later)}},
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed cursor fixture: %v", err)
	}

	repository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	exported, err := repository.ExportLogicalProject(ctx)
	if err != nil {
		t.Fatalf("ExportLogicalProject() error = %v", err)
	}
	if len(exported.Events) != 2 || exported.Events[0].SourceID != firstSourceEventID || exported.Events[1].SourceID != secondSourceEventID {
		t.Fatalf("exported events = %#v, want the two seeded source IDs", exported.Events)
	}

	data, err := domain.MarshalLogicalProjectDocument(exported)
	if err != nil {
		t.Fatalf("MarshalLogicalProjectDocument() error = %v", err)
	}
	plan, err := domain.ParseLogicalProjectImportPlan(data)
	if err != nil {
		t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
	}
	plan = assignImportDestinationIDs(t, plan)

	destDB, _ := openProjectDatabase(t, "Cursors destination", "Instructions")
	destRepository, err := sqlite.NewProjectRepository(destDB)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	if _, err := destRepository.ApplyLogicalProjectImport(ctx, plan); err != nil {
		t.Fatalf("ApplyLogicalProjectImport() error = %v", err)
	}

	var latestDestEventID, firstDestEventID, attemptCursor, targetCursor, requestCursor, approvalCursor int64
	var destRequestID, destIssueID string
	if err := destDB.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT COALESCE(MIN(id), 0), COALESCE(MAX(id), 0) FROM issue_events`).Scan(&firstDestEventID, &latestDestEventID); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT context_event_id_at_start FROM work_attempts`).Scan(&attemptCursor); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT latest_event_id FROM review_targets`).Scan(&targetCursor); err != nil {
			return err
		}
		if err := query.QueryRowContext(ctx, `SELECT id, issue_id, target_event_id FROM review_requests`).Scan(&destRequestID, &destIssueID, &requestCursor); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT target_event_id FROM review_approvals`).Scan(&approvalCursor)
	}); err != nil {
		t.Fatalf("read imported cursors: %v", err)
	}

	// AC1/AC3: not one cursor still names a source-log position.
	for _, cursor := range []struct {
		name string
		got  int64
		want int64
	}{
		{name: "work_attempts.context_event_id_at_start", got: attemptCursor, want: firstDestEventID},
		{name: "review_targets.latest_event_id", got: targetCursor, want: latestDestEventID},
		{name: "review_requests.target_event_id", got: requestCursor, want: latestDestEventID},
		{name: "review_approvals.target_event_id", got: approvalCursor, want: latestDestEventID},
	} {
		if cursor.got != cursor.want {
			t.Fatalf("%s = %d, want the destination position %d (destination log ends at %d)", cursor.name, cursor.got, cursor.want, latestDestEventID)
		}
	}

	destReviews, err := sqlite.NewReviewRepository(destDB)
	if err != nil {
		t.Fatalf("NewReviewRepository() error = %v", err)
	}
	imported, err := destReviews.GetReviewRequest(ctx, destRequestID)
	if err != nil {
		t.Fatalf("GetReviewRequest() error = %v", err)
	}
	if imported.TargetStale {
		t.Fatalf("imported review request is stale immediately after import")
	}

	// AC5: the third leg keeps the destination's own positions rather than
	// resurrecting the source's.
	reExported, err := destRepository.ExportLogicalProject(ctx)
	if err != nil {
		t.Fatalf("re-export ExportLogicalProject() error = %v", err)
	}
	if len(reExported.ReviewTargets) != 1 || reExported.ReviewTargets[0].LatestEventID != latestDestEventID {
		t.Fatalf("re-exported review target = %#v, want latest_event_id %d", reExported.ReviewTargets, latestDestEventID)
	}

	// AC2: post-import implementation activity disqualifies the imported
	// review exactly as it would a natively created one -- the whole point
	// of a cursor the destination log can actually pass.
	if err := destDB.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_type, payload, created_at, source) VALUES (?, 'issue_updated', '{}', ?, 'issue')`,
			destIssueID, sqlite.FormatStorageTime(later.Add(time.Minute)))
		return err
	}); err != nil {
		t.Fatalf("record post-import activity: %v", err)
	}
	afterActivity, err := destReviews.GetReviewRequest(ctx, destRequestID)
	if err != nil {
		t.Fatalf("GetReviewRequest() after activity error = %v", err)
	}
	if !afterActivity.TargetStale {
		t.Fatalf("imported review request survived post-import implementation activity; its cursor is still unreachable")
	}
}
