//go:build integration

package integration_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestIntegrationListIssuesCompactViewStaysWithinByteBudget is the ISSUE-63
// acceptance test: a 100-issue listing in the default (compact) view stays
// within the byte budget documented in docs/03-mcp-tools.md section 5.4,
// even when every issue carries multi-kilobyte description and
// acceptance_criteria bodies. It also confirms those bodies are genuinely
// absent from compact items (not merely truncated), and that view: "full"
// is a strictly larger, opt-in response containing them.
func TestIntegrationListIssuesCompactViewStaysWithinByteBudget(t *testing.T) {
	t.Parallel()
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
	// Measured: ~48 KB for 100 items with one label each (~480 bytes/item);
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

func TestIntegrationListIssuesSortModesAndPagination(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)
	session := env.connect(t)

	create := func(title, priority, status string) string {
		res := callIntegrationTool(t, session, "create_issue", map[string]any{
			"type":     "task",
			"title":    title,
			"priority": priority,
			"status":   status,
		})
		if res.IsError {
			t.Fatalf("create_issue(%q) = %#v", title, res)
		}
		var out struct {
			ID string `json:"id"`
		}
		decodeIntegrationResult(t, res, &out)
		time.Sleep(2 * time.Millisecond)
		return out.ID
	}

	firstID := create("first-low", "low", "ready")
	secondID := create("second-critical", "critical", "open")
	thirdID := create("third-high", "high", "ready")
	fourthID := create("fourth-medium", "medium", "ready")

	updated := callIntegrationTool(t, session, "update_issue", map[string]any{
		"issue_id":         firstID,
		"expected_version": 1,
		"changes":          map[string]any{"title": "first-low-updated"},
	})
	if updated.IsError {
		t.Fatalf("update_issue = %#v", updated)
	}

	collectIDs := func(sortMode any) ([]string, string) {
		var ids []string
		var firstCursor string
		cursor := ""
		for {
			args := map[string]any{"limit": 2}
			if sortMode != nil {
				args["sort"] = sortMode
			}
			if cursor != "" {
				args["cursor"] = cursor
			}
			res := callIntegrationTool(t, session, "list_issues", args)
			if res.IsError {
				t.Fatalf("list_issues(%v) = %#v", args, res)
			}
			var page struct {
				Items []struct {
					ID string `json:"id"`
				} `json:"items"`
				NextCursor *string `json:"next_cursor"`
				HasMore    bool    `json:"has_more"`
			}
			decodeIntegrationResult(t, res, &page)
			for _, item := range page.Items {
				ids = append(ids, item.ID)
			}
			if !page.HasMore {
				break
			}
			if page.NextCursor == nil {
				t.Fatalf("has_more=true with nil next_cursor")
			}
			if firstCursor == "" {
				firstCursor = *page.NextCursor
			}
			cursor = *page.NextCursor
		}
		return ids, firstCursor
	}

	wantPriority := []string{secondID, thirdID, fourthID, firstID}
	wantCreatedDesc := []string{fourthID, thirdID, secondID, firstID}
	wantUpdatedDesc := []string{firstID, fourthID, thirdID, secondID}
	wantSequenceDesc := []string{fourthID, thirdID, secondID, firstID}

	gotPriority, priorityCursor := collectIDs(nil)
	if !reflect.DeepEqual(gotPriority, wantPriority) {
		t.Fatalf("default priority order = %v, want %v", gotPriority, wantPriority)
	}
	gotCreatedDesc, createdCursor := collectIDs("created_desc")
	if !reflect.DeepEqual(gotCreatedDesc, wantCreatedDesc) {
		t.Fatalf("created_desc order = %v, want %v", gotCreatedDesc, wantCreatedDesc)
	}
	gotUpdatedDesc, _ := collectIDs("updated_desc")
	if !reflect.DeepEqual(gotUpdatedDesc, wantUpdatedDesc) {
		t.Fatalf("updated_desc order = %v, want %v", gotUpdatedDesc, wantUpdatedDesc)
	}
	gotSequenceDesc, _ := collectIDs("sequence_desc")
	if !reflect.DeepEqual(gotSequenceDesc, wantSequenceDesc) {
		t.Fatalf("sequence_desc order = %v, want %v", gotSequenceDesc, wantSequenceDesc)
	}

	mismatch := callIntegrationTool(t, session, "list_issues", map[string]any{
		"sort":   "updated_desc",
		"cursor": createdCursor,
	})
	if !mismatch.IsError {
		t.Fatalf("expected mismatched cursor error, got %#v", mismatch)
	}
	var errOut struct {
		Code    string `json:"code"`
		Details []struct {
			Field string `json:"field"`
			Code  string `json:"code"`
		} `json:"details"`
	}
	decodeIntegrationResult(t, mismatch, &errOut)
	if errOut.Code != "INVALID_ARGUMENT" || len(errOut.Details) != 1 || errOut.Details[0].Field != "cursor" || errOut.Details[0].Code != "INVALID_CURSOR" {
		t.Fatalf("mismatch error = %#v, want INVALID_ARGUMENT/INVALID_CURSOR", errOut)
	}

	_ = priorityCursor
	invalidSort := callIntegrationTool(t, session, "list_issues", map[string]any{"sort": "bogus"})
	if !invalidSort.IsError {
		t.Fatalf("expected schema rejection for invalid sort, got %#v", invalidSort)
	}
}

func TestIntegrationIssueListCLISort(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)
	session := env.connect(t)

	create := func(title, priority, status string) string {
		res := callIntegrationTool(t, session, "create_issue", map[string]any{
			"type":     "task",
			"title":    title,
			"priority": priority,
			"status":   status,
		})
		if res.IsError {
			t.Fatalf("create_issue(%q) = %#v", title, res)
		}
		var out struct {
			ID string `json:"id"`
		}
		decodeIntegrationResult(t, res, &out)
		time.Sleep(2 * time.Millisecond)
		return out.ID
	}

	firstID := create("cli-first-low", "low", "ready")
	secondID := create("cli-second-critical", "critical", "open")
	thirdID := create("cli-third-medium", "medium", "ready")

	updated := callIntegrationTool(t, session, "update_issue", map[string]any{
		"issue_id":         firstID,
		"expected_version": 1,
		"changes":          map[string]any{"title": "cli-first-low-updated"},
	})
	if updated.IsError {
		t.Fatalf("update_issue = %#v", updated)
	}

	cliIDs := func(sortFlag string) []string {
		args := []string{"--data-root", env.dataRoot, "issue", "list", "--format", "json"}
		if sortFlag != "" {
			args = append(args, "--sort", sortFlag)
		}
		raw := runIntegrationCommand(t, env, args...)
		var out struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode CLI json (%q): %v", string(raw), err)
		}
		ids := make([]string, len(out.Items))
		for i, item := range out.Items {
			ids[i] = item.ID
		}
		return ids
	}

	if got := cliIDs(""); !reflect.DeepEqual(got, []string{secondID, thirdID, firstID}) {
		t.Fatalf("CLI default sort = %v", got)
	}
	if got := cliIDs("created_desc"); !reflect.DeepEqual(got, []string{thirdID, secondID, firstID}) {
		t.Fatalf("CLI created_desc = %v", got)
	}
	if got := cliIDs("updated_desc"); !reflect.DeepEqual(got, []string{firstID, thirdID, secondID}) {
		t.Fatalf("CLI updated_desc = %v", got)
	}
	if got := cliIDs("sequence_desc"); !reflect.DeepEqual(got, []string{thirdID, secondID, firstID}) {
		t.Fatalf("CLI sequence_desc = %v", got)
	}

	_, stderr, err := runIntegrationCommandExpectingFailure(t, env.repository, "--data-root", env.dataRoot, "issue", "list", "--sort", "bogus")
	if err == nil || !strings.Contains(stderr, "unsupported sort") {
		t.Fatalf("expected failure for invalid CLI --sort, err=%v stderr=%q", err, stderr)
	}
}
