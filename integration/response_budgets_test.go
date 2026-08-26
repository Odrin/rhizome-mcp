//go:build integration

package integration_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestIntegrationDefaultResponseBudgets(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)
	session := env.connect(t)
	baselineProject := callIntegrationTool(t, session, "get_project", map[string]any{})
	if baselineProject.IsError {
		t.Fatalf("get_project for get_changes baseline failed: %#v", baselineProject)
	}
	var baselineState struct {
		LatestEventID int64 `json:"latest_event_id"`
	}
	decodeIntegrationResult(t, baselineProject, &baselineState)

	fixture := budgetFixture{
		description:        repeatedText("response-budget-description ", 8*1024),
		acceptanceCriteria: repeatedText("response-budget-acceptance-criteria ", 8*1024),
		commentContent:     repeatedText("response-budget-comment ", 8*1024),
		decisionContent:    repeatedText("response-budget-decision ", 8*1024),
		noteContent:        repeatedText("response-budget-note ", 8*1024),
		resultSummary:      repeatedText("response-budget-finish-summary ", 8*1024),
		titleSuffix:        strings.Repeat("T", 240),
		labels:             maximumBudgetLabels(),
		maxLikeTitle:       strings.Repeat("A", 300),
	}

	issue := createBudgetIssue(t, session, fixture, "primary")
	root := createBudgetIssue(t, session, fixture, "root")
	childIssues := make([]budgetIssueRef, 0, 19)
	for i := range 19 {
		child := createBudgetIssue(t, session, fixture, fmt.Sprintf("graph-%02d", i))
		childIssues = append(childIssues, child)
		relationResult := callIntegrationTool(t, session, "manage_issue_relation", map[string]any{
			"action":          "add",
			"source_issue_id": child.DisplayID,
			"target_issue_id": root.DisplayID,
			"relation_type":   "blocks",
		})
		if relationResult.IsError {
			t.Fatalf("manage_issue_relation for child %d failed: %#v", i, relationResult)
		}
	}

	commentResult := callIntegrationTool(t, session, "add_comment", map[string]any{"issue_id": issue.DisplayID, "content": fixture.commentContent})
	if commentResult.IsError {
		t.Fatalf("add_comment result = %#v", commentResult)
	}
	decisionResult := callIntegrationTool(t, session, "record_decision", map[string]any{"issue_id": issue.DisplayID, "title": "Budget decision", "summary": "Budget summary", "content": fixture.decisionContent})
	if decisionResult.IsError {
		t.Fatalf("record_decision result = %#v", decisionResult)
	}

	getIssueDefaultBytes := mustMeasureStructuredContentBytes(t, callIntegrationTool(t, session, "get_issue", map[string]any{"issue_id": issue.DisplayID}), "get_issue default")
	if len(getIssueDefaultBytes) > 32*1024 {
		t.Fatalf("get_issue default payload size = %d bytes, want <= %d bytes", len(getIssueDefaultBytes), 32*1024)
	}

	getIssueFull := callIntegrationTool(t, session, "get_issue", map[string]any{"issue_id": issue.DisplayID, "view": "full"})
	if getIssueFull.IsError {
		t.Fatalf("get_issue full result = %#v", getIssueFull)
	}
	getIssueFullBytes := mustMeasureStructuredContentBytes(t, getIssueFull, "get_issue full")
	if len(getIssueFullBytes) <= len(getIssueDefaultBytes) {
		t.Fatalf("get_issue view=\"full\" response (%d bytes) is not meaningfully larger than the default standard projection (%d bytes)", len(getIssueFullBytes), len(getIssueDefaultBytes))
	}

	assertStructuredContentByteBudget(t, callIntegrationTool(t, session, "get_issue_graph", map[string]any{"root_issue_id": root.DisplayID, "depth": 2, "max_nodes": 20}), 32*1024, "get_issue_graph default")
	assertStructuredContentByteBudget(t, callIntegrationTool(t, session, "get_planning_graph", map[string]any{"root_issue_id": nil, "depth": 2, "max_nodes": 20, "include_review": false, "include_related": false}), 32*1024, "get_planning_graph default")

	assertStructuredContentByteBudget(t, callIntegrationTool(t, session, "manage_issue_relation", map[string]any{"action": "remove", "source_issue_id": childIssues[0].DisplayID, "target_issue_id": root.DisplayID, "relation_type": "blocks"}), 32*1024, "manage_issue_relation acknowledgement")

	assertStructuredContentByteBudget(t, callIntegrationTool(t, session, "create_issue", map[string]any{"type": "task", "title": fixture.maxLikeTitle, "description": fixture.description, "acceptance_criteria": fixture.acceptanceCriteria, "status": "ready", "labels": fixture.labels, "create_missing_labels": true, "view": "compact"}), 32*1024, "create_issue compact")
	assertStructuredContentByteBudget(t, callIntegrationTool(t, session, "update_issue", map[string]any{"issue_id": issue.DisplayID, "expected_version": 1, "changes": map[string]any{"title": fixture.maxLikeTitle, "description": fixture.description, "acceptance_criteria": fixture.acceptanceCriteria, "status": "blocked", "blocked_reason": "waiting for fixture data", "labels": fixture.labels}, "create_missing_labels": true, "view": "compact"}), 32*1024, "update_issue compact")
	assertStructuredContentByteBudget(t, callIntegrationTool(t, session, "archive_issue", map[string]any{"issue_id": issue.DisplayID, "expected_version": int64(2), "view": "compact"}), 32*1024, "archive_issue compact")

	claimIssue := createBudgetIssue(t, session, fixture, "claim")
	claimResult := callIntegrationTool(t, session, "claim_issue", map[string]any{"issue_id": claimIssue.DisplayID, "lease_seconds": 600, "view": "compact"})
	if claimResult.IsError {
		t.Fatalf("claim_issue result = %#v", claimResult)
	}
	var claimPayload struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeIntegrationResult(t, claimResult, &claimPayload)
	assertStructuredContentByteBudget(t, claimResult, 32*1024, "claim_issue compact")

	renewResult := callIntegrationTool(t, session, "renew_attempt", map[string]any{"attempt_id": claimPayload.Attempt.ID, "lease_token": claimPayload.LeaseToken, "lease_seconds": 600})
	assertStructuredContentByteBudget(t, renewResult, 32*1024, "renew_attempt compact")

	saveNoteResult := callIntegrationTool(t, session, "save_attempt_note", map[string]any{"attempt_id": claimPayload.Attempt.ID, "lease_token": claimPayload.LeaseToken, "kind": "checkpoint", "content": fixture.noteContent, "next_steps": []string{"Step one", "Step two"}, "important": true, "artifacts": []any{}})
	assertStructuredContentByteBudget(t, saveNoteResult, 64*1024, "save_attempt_note compact")

	finishIssue := createBudgetIssue(t, session, fixture, "finish")
	finishClaimResult := callIntegrationTool(t, session, "claim_issue", map[string]any{"issue_id": finishIssue.DisplayID, "lease_seconds": 600, "view": "compact"})
	if finishClaimResult.IsError {
		t.Fatalf("claim_issue (finish) result = %#v", finishClaimResult)
	}
	var finishClaimPayload struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeIntegrationResult(t, finishClaimResult, &finishClaimPayload)
	finishResult := callIntegrationTool(t, session, "finish_attempt", map[string]any{"attempt_id": finishClaimPayload.Attempt.ID, "lease_token": finishClaimPayload.LeaseToken, "outcome": "completed", "result_summary": fixture.resultSummary, "target_issue_status": "done", "view": "compact"})
	assertStructuredContentByteBudget(t, finishResult, 128*1024, "finish_attempt compact")

	assertStructuredContentByteBudget(t, callIntegrationTool(t, session, "get_work_context", map[string]any{"issue_id": issue.DisplayID}), 256*1024, "get_work_context default")
	assertStructuredContentByteBudget(t, callIntegrationTool(t, session, "get_issue_activity", map[string]any{"issue_id": issue.DisplayID}), 32*1024, "get_issue_activity default")
	assertStructuredContentByteBudget(t, callIntegrationTool(t, session, "get_changes", map[string]any{"since_event_id": baselineState.LatestEventID}), 128*1024, "get_changes default")

	validateDefault := callIntegrationTool(t, session, "validate_issue_plan", map[string]any{"issues": []map[string]any{{"ref": "budget-issue", "type": "task", "title": fixture.maxLikeTitle, "description": fixture.description, "acceptance_criteria": fixture.acceptanceCriteria, "status": "ready", "labels": fixture.labels}}, "relations": []map[string]any{}, "decisions": []map[string]any{}})
	assertStructuredContentByteBudget(t, validateDefault, 32*1024, "validate_issue_plan default")
	validateWithNormalized := callIntegrationTool(t, session, "validate_issue_plan", map[string]any{"issues": []map[string]any{{"ref": "budget-issue", "type": "task", "title": fixture.maxLikeTitle, "description": fixture.description, "acceptance_criteria": fixture.acceptanceCriteria, "status": "ready", "labels": fixture.labels}}, "relations": []map[string]any{}, "decisions": []map[string]any{}, "include_normalized_plan": true})
	if validateWithNormalized.IsError {
		t.Fatalf("validate_issue_plan include_normalized_plan result = %#v", validateWithNormalized)
	}
	validateWithNormalizedBytes := mustMeasureStructuredContentBytes(t, validateWithNormalized, "validate_issue_plan with normalized plan")
	validateDefaultBytes := mustMeasureStructuredContentBytes(t, validateDefault, "validate_issue_plan default for comparison")
	if len(validateWithNormalizedBytes) <= len(validateDefaultBytes) {
		t.Fatalf("validate_issue_plan include_normalized_plan response (%d bytes) did not exceed its default response (%d bytes)", len(validateWithNormalizedBytes), len(validateDefaultBytes))
	}

	assertStructuredContentByteBudget(t, callIntegrationTool(t, session, "export_project", map[string]any{}), 32*1024, "export_project default")
}

type budgetFixture struct {
	description        string
	acceptanceCriteria string
	commentContent     string
	decisionContent    string
	noteContent        string
	resultSummary      string
	titleSuffix        string
	labels             []string
	maxLikeTitle       string
}

type budgetIssueRef struct {
	ID        string
	DisplayID string
}

func createBudgetIssue(t *testing.T, session *mcp.ClientSession, fixture budgetFixture, suffix string) budgetIssueRef {
	t.Helper()
	created := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type":                  "task",
		"title":                 fmt.Sprintf("Budget fixture %s %s", suffix, fixture.titleSuffix),
		"description":           fixture.description,
		"acceptance_criteria":   fixture.acceptanceCriteria,
		"status":                "ready",
		"labels":                fixture.labels,
		"create_missing_labels": true,
		"view":                  "compact",
	})
	if created.IsError {
		t.Fatalf("create_issue %s result = %#v", suffix, created)
	}
	var issue struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeIntegrationResult(t, created, &issue)
	return budgetIssueRef{ID: issue.ID, DisplayID: issue.DisplayID}
}

func repeatedText(prefix string, minimumBytes int) string {
	var builder strings.Builder
	for builder.Len() < minimumBytes {
		builder.WriteString(prefix)
	}
	return builder.String()
}

func maximumBudgetLabels() []string {
	labels := make([]string, 50)
	for index := range labels {
		labels[index] = fmt.Sprintf("budget-label-%02d-%s", index, strings.Repeat("x", 48))
	}
	return labels
}

func assertStructuredContentByteBudget(t *testing.T, result *mcp.CallToolResult, budgetBytes int, label string) []byte {
	t.Helper()
	if result.IsError {
		t.Fatalf("%s returned tool error: %#v", label, result)
	}
	return mustMeasureStructuredContentBytes(t, result, label, budgetBytes)
}

func mustMeasureStructuredContentBytes(t *testing.T, result *mcp.CallToolResult, label string, budgetBytes ...int) []byte {
	t.Helper()
	if result.IsError {
		t.Fatalf("%s returned tool error: %#v", label, result)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal %s structured content: %v", label, err)
	}
	if len(budgetBytes) > 0 && len(data) > budgetBytes[0] {
		t.Fatalf("%s payload size = %d bytes, want <= %d bytes", label, len(data), budgetBytes[0])
	}
	return data
}
