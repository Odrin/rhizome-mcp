//go:build integration

package integration_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestIntegrationListIssuesCompactViewStaysWithinByteBudget is the ISSUE-63
// acceptance test: a 100-issue listing in the default (compact) view stays
// within the byte budget documented in docs/03-mcp-tools.md section 5.4,
// even when every issue carries multi-kilobyte description and
// acceptance_criteria bodies. It also confirms those bodies are genuinely
// absent from compact items (not merely truncated), and that view: "full"
// is a strictly larger, opt-in response containing them.
func TestIntegrationListIssuesCompactViewStaysWithinByteBudget(t *testing.T) {
	env := newIntegrationEnvironment(t)
	session := env.connect(t)

	const issueCount = 100
	// Multi-kilobyte bodies: large enough that, if compact accidentally
	// included them, the response would blow past any reasonable budget.
	description := strings.Repeat("Realistic description body text for the ISSUE-63 response-size budget test. ", 40)       // ~3.1 KB
	acceptanceCriteria := strings.Repeat("Realistic acceptance-criteria body text for the same budget test scenario. ", 30) // ~2.3 KB
	if len(description) < 2048 || len(acceptanceCriteria) < 2048 {
		t.Fatalf("test fixture bodies must be multi-kilobyte: description=%d acceptance_criteria=%d bytes", len(description), len(acceptanceCriteria))
	}

	for i := range issueCount {
		created := callIntegrationTool(t, session, "create_issue", map[string]any{
			"type":                  pickIssueType(i),
			"title":                 fmt.Sprintf("Budget verification issue %03d", i),
			"description":           description,
			"acceptance_criteria":   acceptanceCriteria,
			"status":                "ready",
			"labels":                []string{"budget-test"},
			"create_missing_labels": true,
		})
		if created.IsError {
			t.Fatalf("create_issue %d result = %#v", i, created)
		}
	}

	compact := callIntegrationTool(t, session, "list_issues", map[string]any{"limit": issueCount, "labels": []string{"budget-test"}})
	if compact.IsError {
		t.Fatalf("list_issues (compact) result = %#v", compact)
	}
	var compactPage struct {
		Items   []map[string]json.RawMessage `json:"items"`
		HasMore bool                         `json:"has_more"`
	}
	decodeIntegrationResult(t, compact, &compactPage)
	if len(compactPage.Items) != issueCount || compactPage.HasMore {
		t.Fatalf("list_issues (compact) returned %d items, has_more=%v; want %d items, has_more=false", len(compactPage.Items), compactPage.HasMore, issueCount)
	}
	for _, item := range compactPage.Items {
		for _, forbiddenField := range []string{"description", "acceptance_criteria"} {
			if _, present := item[forbiddenField]; present {
				t.Fatalf("compact list_issues item unexpectedly includes %q: %v", forbiddenField, item)
			}
		}
	}

	compactBytes, err := json.Marshal(compact.StructuredContent)
	if err != nil {
		t.Fatalf("marshal compact structured content: %v", err)
	}
	// Documented budget: docs/03-mcp-tools.md section 5.4 commits to a
	// 100-issue default (compact) listing staying under 64 KB, regardless of
	// how large each issue's description/acceptance_criteria bodies are.
	// Measured: ~46 KB for 100 items with one label each (~470 bytes/item);
	// 64 KB leaves headroom for longer titles and more labels per issue.
	const compactByteBudget = 64 * 1024
	if len(compactBytes) > compactByteBudget {
		t.Fatalf("default list_issues response for %d issues = %d bytes, want <= %d bytes (documented budget)", issueCount, len(compactBytes), compactByteBudget)
	}
	t.Logf("default (compact) list_issues response for %d issues = %d bytes (budget %d bytes)", issueCount, len(compactBytes), compactByteBudget)

	full := callIntegrationTool(t, session, "list_issues", map[string]any{"limit": issueCount, "labels": []string{"budget-test"}, "view": "full"})
	if full.IsError {
		t.Fatalf("list_issues (full) result = %#v", full)
	}
	var fullPage struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	decodeIntegrationResult(t, full, &fullPage)
	if len(fullPage.Items) != issueCount {
		t.Fatalf("list_issues (full) returned %d items, want %d", len(fullPage.Items), issueCount)
	}
	for _, item := range fullPage.Items {
		var itemDescription, itemAcceptanceCriteria string
		if err := json.Unmarshal(item["description"], &itemDescription); err != nil {
			t.Fatalf("full item missing description: %v (%v)", err, item)
		}
		if err := json.Unmarshal(item["acceptance_criteria"], &itemAcceptanceCriteria); err != nil {
			t.Fatalf("full item missing acceptance_criteria: %v (%v)", err, item)
		}
		if itemDescription != description || itemAcceptanceCriteria != acceptanceCriteria {
			t.Fatalf("full item body mismatch: description=%q acceptance_criteria=%q", itemDescription, itemAcceptanceCriteria)
		}
	}
	fullBytes, err := json.Marshal(full.StructuredContent)
	if err != nil {
		t.Fatalf("marshal full structured content: %v", err)
	}
	if len(fullBytes) <= len(compactBytes)*2 {
		t.Fatalf("view: \"full\" response (%d bytes) is not meaningfully larger than compact (%d bytes)", len(fullBytes), len(compactBytes))
	}
	t.Logf("view: \"full\" list_issues response for %d issues = %d bytes", issueCount, len(fullBytes))

	invalidView := callIntegrationTool(t, session, "list_issues", map[string]any{"view": "detailed"})
	if !invalidView.IsError {
		t.Fatalf("list_issues detailed view should be rejected: %#v", invalidView)
	}
}

func pickIssueType(index int) string {
	if index%7 == 0 {
		return "bug"
	}
	return "task"
}
