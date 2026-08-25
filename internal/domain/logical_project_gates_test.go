package domain_test

import (
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"rhizome-mcp/internal/domain"
)

// buildGatesDocument returns a v2 document carrying one finished attempt,
// one review target/request pair, and a gates namespace with one policy,
// one evidence record, and one approval. mutate may adjust the namespace
// before the document is rendered.
func buildGatesDocument(mutate func(namespace map[string]any)) []byte {
	attemptID := ulid.Make().String()
	policyID := ulid.Make().String()
	targetID := ulid.Make().String()
	requestID := ulid.Make().String()
	evidenceID := ulid.Make().String()
	approvalID := ulid.Make().String()
	fingerprint := strings.Repeat("a", 64)
	return buildLogicalProjectDocument(func(document map[string]any) {
		document["version"] = 2
		issueID := document["issues"].([]any)[0].(map[string]any)["id"]
		document["attempts"] = []any{map[string]any{
			"id":                        attemptID,
			"issue_id":                  issueID,
			"session_id":                nil,
			"agent_label":               nil,
			"kind":                      "work",
			"status":                    "completed",
			"issue_version_at_start":    1,
			"context_event_id_at_start": 0,
			"lease_expires_at":          "2026-07-17T18:24:07Z",
			"started_at":                "2026-07-17T18:24:07Z",
			"last_heartbeat_at":         "2026-07-17T18:24:07Z",
			"finished_at":               "2026-07-17T18:24:08Z",
			"result_summary":            nil,
			"next_steps":                []any{},
			"verification":              []any{},
			"failure_reason_code":       nil,
			"interruption_reason_code":  nil,
			"reason_details":            nil,
		}}
		document["review_targets"] = []any{map[string]any{
			"id": targetID, "issue_id": issueID, "issue_version": 1, "latest_event_id": 0,
			"artifact_ids": []any{}, "purposes": []any{"implementation", "security"},
			"created_at": "2026-07-17T18:24:07Z",
		}}
		document["review_requests"] = []any{map[string]any{
			"id": requestID, "target_id": targetID, "issue_id": issueID,
			"target_issue_version": 1, "target_event_id": 0, "artifact_ids": []any{},
			"purposes": []any{"implementation", "security"},
			"status":   "approved", "supersedes_id": nil,
			"created_at": "2026-07-17T18:24:07Z", "resolved_at": "2026-07-17T18:24:08Z",
		}}
		namespace := map[string]any{
			"version": 1,
			"policies": []any{map[string]any{
				"id":                policyID,
				"selector_json":     map[string]any{"issue_types": []any{"task"}},
				"requirements_json": []any{map[string]any{"key": "impl", "kind": "attempt_evidence", "evidence_key": "impl"}},
				"status":            "active",
				"version":           1,
				"created_at":        "2026-07-17T18:24:07Z",
				"updated_at":        "2026-07-17T18:24:07Z",
			}},
			"policy_events": []any{map[string]any{
				"source_id": 1, "policy_id": policyID, "event_type": "policy_created",
				"session_id": nil, "prior_version": nil, "new_version": 1,
				"payload": map[string]any{}, "created_at": "2026-07-17T18:24:07Z",
			}},
			"attempt_snapshots": []any{map[string]any{
				"attempt_id":           attemptID,
				"requirements_json":    []any{},
				"source_policies_json": nil,
				"fingerprint":          fingerprint,
				"issue_version":        1,
				"created_at":           "2026-07-17T18:24:07Z",
			}},
			"review_target_snapshots": []any{},
			"evidence": []any{map[string]any{
				"id": evidenceID, "attempt_id": attemptID, "issue_id": issueID,
				"key": "impl", "result": "satisfied", "summary": "Implemented",
				"details": nil, "artifact_ids": []any{}, "version": 1,
				"created_at": "2026-07-17T18:24:07Z", "updated_at": "2026-07-17T18:24:07Z",
			}},
			"evidence_events": []any{},
			"review_approvals": []any{map[string]any{
				"id": approvalID, "issue_id": issueID, "target_id": targetID,
				"request_id": requestID, "attempt_id": attemptID, "purpose": "security",
				"target_issue_version": 1, "target_event_id": 0, "version": 1,
				"created_at": "2026-07-17T18:24:08Z",
			}},
		}
		if mutate != nil {
			mutate(namespace)
		}
		document["extensions"] = map[string]any{"gates": namespace}
	})
}

// TestLogicalProjectGatesExtensionIsVersionGated: v1's frozen key table has
// no extensions map, so a v1 document cannot smuggle gate state in.
func TestLogicalProjectGatesExtensionIsVersionGated(t *testing.T) {
	payload := buildLogicalProjectDocument(func(document map[string]any) {
		document["version"] = 1
		document["extensions"] = map[string]any{"gates": map[string]any{"version": 1}}
	})
	_, err := domain.ParseLogicalProjectImportPlan(payload)
	assertDomainErrorDetail(t, err, domain.CodeUnsupportedField, domain.CodeUnsupportedField, "$.extensions")
}

// TestLogicalProjectGatesExtensionRoundTripsThroughParse: a well-formed
// namespace parses, is counted in the dry run, and mints destination IDs
// for its identified records.
func TestLogicalProjectGatesExtensionRoundTripsThroughParse(t *testing.T) {
	plan, err := domain.ParseLogicalProjectImportPlan(buildGatesDocument(nil))
	if err != nil {
		t.Fatalf("ParseLogicalProjectImportPlan() error = %v", err)
	}
	counts := plan.DryRun.Counts
	if counts.WorkflowPolicies != 1 || counts.WorkflowPolicyEvents != 1 || counts.AttemptGateSnapshots != 1 ||
		counts.GateEvidence != 1 || counts.ReviewApprovals != 1 {
		t.Fatalf("gate counts = %+v", counts)
	}
	destinationIDs, err := domain.NewLogicalProjectImportDestinationIDs(plan.Document, func() (string, error) {
		return ulid.Make().String(), nil
	})
	if err != nil {
		t.Fatalf("NewLogicalProjectImportDestinationIDs() error = %v", err)
	}
	if len(destinationIDs.WorkflowPolicyIDs) != 1 || len(destinationIDs.GateEvidenceIDs) != 1 || len(destinationIDs.ReviewApprovalIDs) != 1 {
		t.Fatalf("gate destination IDs = %#v", destinationIDs)
	}
}

// TestLogicalProjectGatesExtensionRejectsBrokenReferences pins the
// referential and consistency rules a hand-crafted namespace must satisfy.
func TestLogicalProjectGatesExtensionRejectsBrokenReferences(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(namespace map[string]any)
	}{
		{name: "policy event names an unknown policy", mutate: func(namespace map[string]any) {
			namespace["policy_events"].([]any)[0].(map[string]any)["policy_id"] = ulid.Make().String()
		}},
		{name: "evidence issue does not match its attempt's issue", mutate: func(namespace map[string]any) {
			namespace["evidence"].([]any)[0].(map[string]any)["issue_id"] = ulid.Make().String()
		}},
		{name: "approval duplicates a request purpose", mutate: func(namespace map[string]any) {
			approvals := namespace["review_approvals"].([]any)
			duplicate := map[string]any{}
			for key, value := range approvals[0].(map[string]any) {
				duplicate[key] = value
			}
			duplicate["id"] = ulid.Make().String()
			namespace["review_approvals"] = append(approvals, duplicate)
		}},
		{name: "snapshot fingerprint has the wrong length", mutate: func(namespace map[string]any) {
			namespace["attempt_snapshots"].([]any)[0].(map[string]any)["fingerprint"] = "short"
		}},
		{name: "unsupported namespace version", mutate: func(namespace map[string]any) {
			namespace["version"] = 2
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := domain.ParseLogicalProjectImportPlan(buildGatesDocument(tt.mutate)); err == nil {
				t.Fatal("ParseLogicalProjectImportPlan() succeeded, want a validation error")
			}
		})
	}
}

// TestLogicalProjectReviewPurposesValidateWhenPresent: a malformed purposes
// list on a review target is rejected; an absent one is the compatibility
// default and parses.
func TestLogicalProjectReviewPurposesValidateWhenPresent(t *testing.T) {
	bad := buildGatesDocument(nil)
	badDocument := strings.Replace(string(bad), `"implementation",`, `"implementation","implementation",`, 1)
	if _, err := domain.ParseLogicalProjectImportPlan([]byte(badDocument)); err == nil {
		t.Fatal("duplicate purpose accepted, want a validation error")
	}
}
