//go:build integration

package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/ports"
)

// These are ISSUE-175 AC5's end-to-end proofs: the acceptance-criteria,
// implementation, tests, and security-review gates enforced through the
// real binary over a real MCP transport -- policies configured with
// manage_workflow_policy, work driven with claim_issue/finish_attempt,
// evidence recorded with submit_gate_evidence, and state observed with
// evaluate_gates and get_work_context. Nothing reaches into the database
// except the security test's review-request creation, which has no MCP
// creation tool (matching TestIntegrationReviewWorkflow).

func mustCreateGatePolicy(t *testing.T, session *mcp.ClientSession, requirements []map[string]any) string {
	t.Helper()
	created := callIntegrationTool(t, session, "manage_workflow_policy", map[string]any{
		"action":       "create",
		"selector":     map[string]any{"issue_types": []string{"task"}},
		"requirements": requirements,
	})
	var policy struct {
		ID string `json:"id"`
	}
	decodeIntegrationResult(t, created, &policy)
	if created.IsError || policy.ID == "" {
		t.Fatalf("manage_workflow_policy result = %#v, decoded = %#v", created, policy)
	}
	return policy.ID
}

// assertGateUnsatisfied asserts result is the WORKFLOW_GATE_UNSATISFIED
// envelope and that its details name exactly the expected requirement keys.
func assertGateUnsatisfied(t *testing.T, result *mcp.CallToolResult, wantKeys ...string) {
	t.Helper()
	if !result.IsError {
		t.Fatalf("call succeeded, want WORKFLOW_GATE_UNSATISFIED: %#v", result.StructuredContent)
	}
	var envelope struct {
		Code    string `json:"code"`
		Details []struct {
			Field string `json:"field"`
			Code  string `json:"code"`
		} `json:"details"`
	}
	decodeIntegrationResult(t, result, &envelope)
	if envelope.Code != "WORKFLOW_GATE_UNSATISFIED" {
		t.Fatalf("error code = %q, want WORKFLOW_GATE_UNSATISFIED: %#v", envelope.Code, result.StructuredContent)
	}
	gotKeys := make(map[string]bool, len(envelope.Details))
	for _, detail := range envelope.Details {
		gotKeys[detail.Field] = true
	}
	if len(gotKeys) != len(wantKeys) {
		t.Fatalf("unmet requirement keys = %#v, want %v", envelope.Details, wantKeys)
	}
	for _, want := range wantKeys {
		if !gotKeys[want] {
			t.Fatalf("unmet requirement keys = %#v, want %q among them", envelope.Details, want)
		}
	}
}

// TestIntegrationAcceptanceCriteriaGateBlocksClaim proves the
// acceptance-criteria gate (issue_field_nonblank) at claim_work: a matching
// task without acceptance criteria cannot be claimed, evaluate_gates
// explains why, and filling the field unlocks the claim.
func TestIntegrationAcceptanceCriteriaGateBlocksClaim(t *testing.T) {
	env := newIntegrationEnvironment(t)
	session := env.connect(t)

	policyID := mustCreateGatePolicy(t, session, []map[string]any{
		{"key": "acceptance-criteria", "kind": "issue_field_nonblank", "field": "acceptance_criteria"},
	})
	issue := mustCreateBoardIssue(t, session, map[string]any{
		"type": "task", "title": "Gated by acceptance criteria", "status": "ready",
	})

	claim := callIntegrationTool(t, session, "claim_issue", map[string]any{"issue_id": issue.DisplayID})
	assertGateUnsatisfied(t, claim, "acceptance-criteria")

	diagnostic := callIntegrationTool(t, session, "evaluate_gates", map[string]any{
		"issue_id": issue.DisplayID, "enforcement_point": "claim_work",
	})
	var evaluation struct {
		Passed bool `json:"passed"`
		Unmet  []struct {
			PolicyID       string `json:"policy_id"`
			RequirementKey string `json:"requirement_key"`
			Reason         string `json:"reason"`
		} `json:"unmet"`
	}
	decodeIntegrationResult(t, diagnostic, &evaluation)
	if diagnostic.IsError || evaluation.Passed || len(evaluation.Unmet) != 1 {
		t.Fatalf("evaluate_gates = %#v, decoded = %#v", diagnostic, evaluation)
	}
	if evaluation.Unmet[0].PolicyID != policyID || evaluation.Unmet[0].RequirementKey != "acceptance-criteria" || evaluation.Unmet[0].Reason == "" {
		t.Fatalf("diagnostic unmet = %#v, want the policy's acceptance-criteria requirement with a reason", evaluation.Unmet)
	}

	filled := callIntegrationTool(t, session, "update_issue", map[string]any{
		"issue_id": issue.DisplayID, "expected_version": 1,
		"changes": map[string]any{"acceptance_criteria": "1. The gate opens."},
	})
	if filled.IsError {
		t.Fatalf("update_issue(acceptance_criteria) failed: %#v", filled.StructuredContent)
	}
	unlocked := callIntegrationTool(t, session, "claim_issue", map[string]any{"issue_id": issue.DisplayID})
	if unlocked.IsError {
		t.Fatalf("claim_issue after filling acceptance criteria failed: %#v", unlocked.StructuredContent)
	}
}

// TestIntegrationEvidenceGatesBlockCompletion proves the implementation and
// tests gates (attempt_evidence) at complete_work_to_done: completion is
// rejected while either evidence key is missing, get_work_context's gate
// summary names what is missing with next actions, submit_gate_evidence
// clears one requirement at a time, and completion succeeds once both are
// recorded.
func TestIntegrationEvidenceGatesBlockCompletion(t *testing.T) {
	env := newIntegrationEnvironment(t)
	session := env.connect(t)

	mustCreateGatePolicy(t, session, []map[string]any{
		{"key": "implementation", "kind": "attempt_evidence", "evidence_key": "implementation"},
		{"key": "tests", "kind": "attempt_evidence", "evidence_key": "tests"},
	})
	issue := mustCreateBoardIssue(t, session, map[string]any{
		"type": "task", "title": "Gated by evidence", "status": "ready",
		"acceptance_criteria": "1. Both evidence gates recorded.",
	})

	claimed := callIntegrationTool(t, session, "claim_issue", map[string]any{"issue_id": issue.DisplayID, "lease_seconds": 120})
	var claim struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeIntegrationResult(t, claimed, &claim)
	if claimed.IsError || claim.Attempt.ID == "" || claim.LeaseToken == "" {
		t.Fatalf("claim_issue result = %#v, decoded = %#v", claimed, claim)
	}

	blocked := callIntegrationTool(t, session, "finish_attempt", map[string]any{
		"attempt_id": claim.Attempt.ID, "lease_token": claim.LeaseToken,
		"outcome": "completed", "result_summary": "Trying to finish without evidence.",
		"target_issue_status": "done",
	})
	assertGateUnsatisfied(t, blocked, "implementation", "tests")

	contextResult := callIntegrationTool(t, session, "get_work_context", map[string]any{"issue_id": issue.DisplayID})
	var workContext struct {
		Gates struct {
			EnforcementPoint    string  `json:"enforcement_point"`
			SnapshotFingerprint *string `json:"snapshot_fingerprint"`
			RequirementCount    int64   `json:"requirement_count"`
			SatisfiedCount      int64   `json:"satisfied_count"`
			Unmet               []struct {
				RequirementKey string `json:"requirement_key"`
			} `json:"unmet"`
			NextActions []string `json:"next_actions"`
		} `json:"gates"`
	}
	decodeIntegrationResult(t, contextResult, &workContext)
	if contextResult.IsError {
		t.Fatalf("get_work_context failed: %#v", contextResult.StructuredContent)
	}
	gates := workContext.Gates
	if gates.EnforcementPoint != "complete_work_to_done" || gates.SnapshotFingerprint == nil {
		t.Fatalf("work context gates = %#v, want the active attempt's frozen snapshot at complete_work_to_done", gates)
	}
	if gates.RequirementCount != 2 || gates.SatisfiedCount != 0 || len(gates.Unmet) != 2 || len(gates.NextActions) != 2 {
		t.Fatalf("work context gates = %#v, want both evidence requirements unmet with next actions", gates)
	}
	if !strings.Contains(gates.NextActions[0], "submit_gate_evidence") {
		t.Fatalf("next action = %q, want it to name submit_gate_evidence", gates.NextActions[0])
	}

	implementation := callIntegrationTool(t, session, "submit_gate_evidence", map[string]any{
		"attempt_id": claim.Attempt.ID, "lease_token": claim.LeaseToken,
		"key": "implementation", "result": "satisfied", "summary": "Implemented in commit abc123.",
	})
	if implementation.IsError {
		t.Fatalf("submit_gate_evidence(implementation) failed: %#v", implementation.StructuredContent)
	}
	stillBlocked := callIntegrationTool(t, session, "finish_attempt", map[string]any{
		"attempt_id": claim.Attempt.ID, "lease_token": claim.LeaseToken,
		"outcome": "completed", "result_summary": "Trying to finish with half the evidence.",
		"target_issue_status": "done",
	})
	assertGateUnsatisfied(t, stillBlocked, "tests")

	tests := callIntegrationTool(t, session, "submit_gate_evidence", map[string]any{
		"attempt_id": claim.Attempt.ID, "lease_token": claim.LeaseToken,
		"key": "tests", "result": "satisfied", "summary": "go test ./... green.",
	})
	if tests.IsError {
		t.Fatalf("submit_gate_evidence(tests) failed: %#v", tests.StructuredContent)
	}
	finished := callIntegrationTool(t, session, "finish_attempt", map[string]any{
		"attempt_id": claim.Attempt.ID, "lease_token": claim.LeaseToken,
		"outcome": "completed", "result_summary": "Both gates satisfied.",
		"target_issue_status": "done",
	})
	var completion struct {
		Issue struct {
			Status string `json:"status"`
		} `json:"issue"`
	}
	decodeIntegrationResult(t, finished, &completion)
	if finished.IsError || completion.Issue.Status != "done" {
		t.Fatalf("finish_attempt after evidence = %#v, decoded = %#v", finished, completion)
	}
}

// TestIntegrationSecurityReviewGateRequiresPurposeScopedApproval proves the
// security-review gate (review_approval, purpose "security"): a work
// attempt cannot complete to done without a security approval, completing
// to review instead is allowed, and approving a review request whose
// purposes cover "security" grants the approval and closes the issue
// through the approve_review enforcement point.
func TestIntegrationSecurityReviewGateRequiresPurposeScopedApproval(t *testing.T) {
	env := newIntegrationEnvironment(t)
	session := env.connect(t)

	mustCreateGatePolicy(t, session, []map[string]any{
		{"key": "security-review", "kind": "review_approval", "purpose": "security"},
	})
	issue := mustCreateBoardIssue(t, session, map[string]any{
		"type": "task", "title": "Gated by security review", "status": "ready",
		"acceptance_criteria": "1. Security review approved.",
	})

	claimed := callIntegrationTool(t, session, "claim_issue", map[string]any{"issue_id": issue.DisplayID, "lease_seconds": 120})
	var claim struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeIntegrationResult(t, claimed, &claim)
	if claimed.IsError || claim.Attempt.ID == "" {
		t.Fatalf("claim_issue result = %#v, decoded = %#v", claimed, claim)
	}

	blocked := callIntegrationTool(t, session, "finish_attempt", map[string]any{
		"attempt_id": claim.Attempt.ID, "lease_token": claim.LeaseToken,
		"outcome": "completed", "result_summary": "Trying to finish without the security approval.",
		"target_issue_status": "done",
	})
	assertGateUnsatisfied(t, blocked, "security-review")

	toReview := callIntegrationTool(t, session, "finish_attempt", map[string]any{
		"attempt_id": claim.Attempt.ID, "lease_token": claim.LeaseToken,
		"outcome": "completed", "result_summary": "Handing off for security review.",
		"target_issue_status": "review",
	})
	if toReview.IsError {
		t.Fatalf("finish_attempt to review failed (review_approval must not gate complete_work_to_review): %#v", toReview.StructuredContent)
	}

	// A review request whose purposes cover "security". There is no MCP
	// creation tool for review requests, so this uses the repository like
	// TestIntegrationReviewWorkflow; its target snapshot freezes the
	// security requirement at creation time.
	databasePath := mustProjectDatabasePath(t, env)
	db, err := sqlite.Open(context.Background(), databasePath, sqlite.Options{})
	if err != nil {
		t.Fatalf("open project database: %v", err)
	}
	defer func() { _ = db.Close(context.Background()) }()
	var latestEventID int64
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM issue_events`).Scan(&latestEventID)
	}); err != nil {
		t.Fatalf("read latest issue event id: %v", err)
	}
	var issueVersion int64
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT version FROM issues WHERE id = ?`, issue.ID).Scan(&issueVersion)
	}); err != nil {
		t.Fatalf("read issue version: %v", err)
	}
	reviewRepository, err := sqlite.NewReviewRepository(db)
	if err != nil {
		t.Fatalf("new review repository: %v", err)
	}
	if _, err := reviewRepository.CreateReviewRequest(context.Background(), ports.CreateReviewRequestCommand{
		RequestID: newIntegrationULID(t), TargetID: newIntegrationULID(t),
		Purposes:           []string{"implementation", "security"},
		IssueID:            issue.ID,
		TargetIssueVersion: issueVersion,
		TargetEventID:      latestEventID,
		OccurredAt:         time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create security review request: %v", err)
	}

	reviewClaimed := callIntegrationTool(t, session, "claim_issue", map[string]any{"issue_id": issue.DisplayID, "lease_seconds": 120})
	var reviewClaim struct {
		Attempt struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeIntegrationResult(t, reviewClaimed, &reviewClaim)
	if reviewClaimed.IsError || reviewClaim.Attempt.Kind != "review" {
		t.Fatalf("claim_issue for review = %#v, decoded = %#v", reviewClaimed, reviewClaim)
	}

	approved := callIntegrationTool(t, session, "finish_attempt", map[string]any{
		"attempt_id": reviewClaim.Attempt.ID, "lease_token": reviewClaim.LeaseToken,
		"outcome": "completed", "result_summary": "Security review approved.",
		"review_outcome": "approved",
	})
	var approval struct {
		Issue struct {
			Status string `json:"status"`
		} `json:"issue"`
	}
	decodeIntegrationResult(t, approved, &approval)
	if approved.IsError || approval.Issue.Status != "done" {
		t.Fatalf("finish_attempt(approved) = %#v, decoded = %#v", approved, approval)
	}

	// The purpose-scoped approval now exists, so the done-gate the work
	// attempt failed earlier evaluates satisfied.
	diagnostic := callIntegrationTool(t, session, "evaluate_gates", map[string]any{
		"issue_id": issue.DisplayID, "enforcement_point": "complete_work_to_done",
	})
	var evaluation struct {
		Passed bool `json:"passed"`
	}
	decodeIntegrationResult(t, diagnostic, &evaluation)
	if diagnostic.IsError || !evaluation.Passed {
		t.Fatalf("evaluate_gates after approval = %#v, decoded = %#v", diagnostic, evaluation)
	}
}
