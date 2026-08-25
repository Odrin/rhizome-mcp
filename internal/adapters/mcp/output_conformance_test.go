package mcp_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/ports"
)

// TestOutputSchemaConformance implements ISSUE-199 AC1: success() always
// passes a nil typed output to the go-sdk (see success() in adapter.go), so
// the SDK's own output-schema validation path never runs -- an advertised
// OutputSchema can drift from what a handler actually returns with nothing
// catching it. This test drives every registered tool -- and, where a tool
// accepts a view parameter, each view variant -- through a single coherent
// fixture scenario via the in-process client, resolves each tool's
// advertised OutputSchema with jsonschema.Resolve, and validates the real
// returned structuredContent against it.
//
// Steps run in one fixed sequence (conformanceSteps below) because most
// tools need state a prior tool created (an issue ID, a lease token, a
// review request ID, ...): each step is a small function that builds that
// step's call arguments from a shared conformanceState and, after the call
// succeeds and its output validates, may capture new state for later steps.
//
// TestOutputSchemaConformanceApplyImport and TestOutputSchemaConformanceReviewRequests
// below cover apply_import (needs a genuinely empty destination database)
// and the four review-request tools (need a review-request lifecycle that
// doesn't disturb this file's single-issue work/review/done scenario) as
// separate test functions with their own fixtures, for the same reason.
//
// Not yet covered: open_project. It opens a project by absolute
// filesystem root rather than routing by project_ref, so it needs a second
// real project directory on disk stood up via projectruntime/the CLI init
// path (see main_test.go's TestNewMCPServerWithDefaultRouterStillSupportsOmittedRef
// for that pattern) rather than anything reachable from this package's
// existing single-project test harness (composeServices/openDatabase).
func TestOutputSchemaConformance(t *testing.T) {
	ctx := context.Background()
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "output-conformance.db"))
	defer db.Close(ctx)
	client, stop := newClient(t, composeServices(t, db, source))
	defer stop()

	tools, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	resolved := make(map[string]*jsonschema.Resolved, len(tools.Tools))
	for _, tool := range tools.Tools {
		schema := decodeOutputSchema(t, tool)
		if schema == nil {
			continue
		}
		r, err := schema.Resolve(nil)
		if err != nil {
			t.Fatalf("resolve %s output schema: %v", tool.Name, err)
		}
		resolved[tool.Name] = r
	}

	state := &conformanceState{}
	for _, step := range conformanceSteps {
		step := step
		t.Run(step.name, func(t *testing.T) {
			result := call(t, client, step.tool, step.args(t, state))
			if result.IsError {
				var text string
				if len(result.Content) > 0 {
					if tc, ok := result.Content[0].(*sdkmcp.TextContent); ok {
						text = tc.Text
					}
				}
				t.Fatalf("%s call failed: %s (structured=%#v)", step.tool, text, result.StructuredContent)
			}
			schema, ok := resolved[step.tool]
			if !ok {
				t.Fatalf("no advertised output schema found for tool %q (renamed or removed from the catalog?)", step.tool)
			}
			if err := schema.Validate(result.StructuredContent); err != nil {
				t.Errorf("%s structuredContent failed its own advertised OutputSchema: %v\noutput: %#v", step.tool, err, result.StructuredContent)
			}
			if step.capture != nil {
				step.capture(t, state, result)
			}
		})
	}
}

// decodeOutputSchema re-decodes a tool's wire-level OutputSchema (typed any
// on sdkmcp.Tool) into *jsonschema.Schema, mirroring
// schema_coverage_test.go's decodeInputSchema for the input side.
func decodeOutputSchema(t *testing.T, tool *sdkmcp.Tool) *jsonschema.Schema {
	t.Helper()
	if tool.OutputSchema == nil {
		return nil
	}
	data, err := json.Marshal(tool.OutputSchema)
	if err != nil {
		t.Fatalf("marshal %s output schema: %v", tool.Name, err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("unmarshal %s output schema: %v", tool.Name, err)
	}
	return &schema
}

// conformanceState threads IDs and tokens captured from one step's output
// into later steps' input, mirroring a real agent session's use of the tool
// catalog against one issue/attempt/review lifecycle.
type conformanceState struct {
	sessionHandle string

	epicID string

	issueID        string
	issueDisplayID string

	labelID string

	archivableID string

	attemptID  string
	leaseToken string

	commentID string

	exportedDocument any
}

type conformanceStep struct {
	// name identifies the subtest; distinct from tool for a view variant
	// (e.g. "get_issue/full") so both variants of one tool run and report
	// independently.
	name    string
	tool    string
	args    func(t *testing.T, state *conformanceState) map[string]any
	capture func(t *testing.T, state *conformanceState, result *sdkmcp.CallToolResult)
}

// structuredField reads one top-level field out of a successful call's
// structuredContent, failing the test if it's absent or the wrong type --
// used by capture funcs below to pull IDs/tokens forward into later steps.
func structuredField[T any](t *testing.T, result *sdkmcp.CallToolResult, path ...string) T {
	t.Helper()
	current, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structuredContent is not an object: %#v", result.StructuredContent)
	}
	for i, key := range path {
		value, present := current[key]
		if !present {
			t.Fatalf("structuredContent missing field %q (path %v): %#v", key, path, result.StructuredContent)
		}
		if i == len(path)-1 {
			typed, ok := value.(T)
			if !ok {
				t.Fatalf("structuredContent field %q = %#v, want type %T", key, value, *new(T))
			}
			return typed
		}
		next, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("structuredContent field %q is not an object: %#v", key, value)
		}
		current = next
	}
	panic("unreachable: empty path")
}

// conformanceSteps is the fixed, ordered fixture scenario. Add new steps by
// appending -- later steps may depend on any earlier step's captured state,
// but never the reverse, so keep dependency order in mind when inserting.
var conformanceSteps = []conformanceStep{
	{
		name: "create_agent_session",
		tool: "create_agent_session",
		args: func(_ *testing.T, _ *conformanceState) map[string]any {
			return map[string]any{"client_name": "conformance-test", "client_version": "1.0"}
		},
		capture: func(t *testing.T, state *conformanceState, result *sdkmcp.CallToolResult) {
			state.sessionHandle = structuredField[string](t, result, "agent_session_handle")
		},
	},
	{
		name: "create_issue/epic",
		tool: "create_issue",
		args: func(t *testing.T, _ *conformanceState) map[string]any {
			return map[string]any{"type": "epic", "title": "Conformance epic", "priority": "medium"}
		},
		capture: func(t *testing.T, state *conformanceState, result *sdkmcp.CallToolResult) {
			state.epicID = structuredField[string](t, result, "id")
		},
	},
	{
		name: "create_issue/task",
		tool: "create_issue",
		args: func(_ *testing.T, state *conformanceState) map[string]any {
			return map[string]any{
				"type": "task", "title": "Conformance task", "priority": "high", "status": "ready",
				"parent_issue_id": state.epicID, "description": "exercised by the output conformance harness",
				"acceptance_criteria": "structuredContent validates against OutputSchema",
			}
		},
		capture: func(t *testing.T, state *conformanceState, result *sdkmcp.CallToolResult) {
			state.issueID = structuredField[string](t, result, "id")
			state.issueDisplayID = structuredField[string](t, result, "display_id")
		},
	},
	{
		name: "list_labels",
		tool: "list_labels",
		args: func(_ *testing.T, _ *conformanceState) map[string]any { return map[string]any{} },
	},
	{
		name: "update_issue",
		tool: "update_issue",
		args: func(_ *testing.T, state *conformanceState) map[string]any {
			return map[string]any{"issue_id": state.issueID, "expected_version": float64(1), "changes": map[string]any{"priority": "critical"}}
		},
	},
	{
		name: "get_issue/compact",
		tool: "get_issue",
		args: func(_ *testing.T, state *conformanceState) map[string]any {
			return map[string]any{"issue_id": state.issueDisplayID, "view": "compact"}
		},
	},
	{
		name: "get_issue/full",
		tool: "get_issue",
		args: func(_ *testing.T, state *conformanceState) map[string]any {
			return map[string]any{"issue_id": state.issueDisplayID, "view": "full"}
		},
	},
	{
		name: "list_issues",
		tool: "list_issues",
		args: func(_ *testing.T, _ *conformanceState) map[string]any {
			return map[string]any{"statuses": []string{"ready"}, "limit": float64(5)}
		},
	},
	{
		name: "claim_issue",
		tool: "claim_issue",
		args: func(_ *testing.T, state *conformanceState) map[string]any {
			return map[string]any{"issue_id": state.issueDisplayID, "lease_seconds": float64(900)}
		},
		capture: func(t *testing.T, state *conformanceState, result *sdkmcp.CallToolResult) {
			state.attemptID = structuredField[string](t, result, "attempt", "id")
			state.leaseToken = structuredField[string](t, result, "lease_token")
		},
	},
	{
		name: "renew_attempt",
		tool: "renew_attempt",
		args: func(_ *testing.T, state *conformanceState) map[string]any {
			return map[string]any{"attempt_id": state.attemptID, "lease_token": state.leaseToken, "lease_seconds": float64(900)}
		},
	},
	{
		name: "add_comment",
		tool: "add_comment",
		args: func(_ *testing.T, state *conformanceState) map[string]any {
			return map[string]any{"issue_id": state.issueDisplayID, "content": "conformance comment"}
		},
		capture: func(t *testing.T, state *conformanceState, result *sdkmcp.CallToolResult) {
			state.commentID = structuredField[string](t, result, "comment", "id")
		},
	},
	{
		name: "record_decision",
		tool: "record_decision",
		args: func(_ *testing.T, state *conformanceState) map[string]any {
			return map[string]any{
				"issue_id": state.issueDisplayID, "title": "Conformance decision",
				"summary": "one-line summary", "content": "full decision content",
			}
		},
	},
	{
		name: "get_issue_activity",
		tool: "get_issue_activity",
		args: func(_ *testing.T, state *conformanceState) map[string]any {
			return map[string]any{"issue_id": state.issueDisplayID, "limit": float64(10)}
		},
	},
	{
		name: "get_work_context",
		tool: "get_work_context",
		args: func(_ *testing.T, state *conformanceState) map[string]any {
			return map[string]any{"issue_id": state.issueDisplayID}
		},
	},
	{
		name: "get_changes",
		tool: "get_changes",
		args: func(_ *testing.T, _ *conformanceState) map[string]any {
			return map[string]any{"since_event_id": float64(0), "limit": float64(10)}
		},
	},
	{
		name: "search",
		tool: "search",
		args: func(_ *testing.T, _ *conformanceState) map[string]any { return map[string]any{"query": "conformance"} },
	},
	{
		name: "list_decisions",
		tool: "list_decisions",
		args: func(_ *testing.T, state *conformanceState) map[string]any {
			return map[string]any{"issue_id": state.issueDisplayID}
		},
	},
	{
		name: "get_issue_graph",
		tool: "get_issue_graph",
		args: func(_ *testing.T, state *conformanceState) map[string]any {
			return map[string]any{"root_issue_id": state.issueDisplayID}
		},
	},
	{
		name: "get_planning_graph",
		tool: "get_planning_graph",
		args: func(_ *testing.T, _ *conformanceState) map[string]any { return map[string]any{} },
	},
	{
		name: "get_project",
		tool: "get_project",
		args: func(_ *testing.T, _ *conformanceState) map[string]any { return map[string]any{} },
	},
	{
		name: "finish_attempt/work_to_review",
		tool: "finish_attempt",
		args: func(_ *testing.T, state *conformanceState) map[string]any {
			return map[string]any{
				"attempt_id": state.attemptID, "lease_token": state.leaseToken,
				"outcome": "completed", "result_summary": "moved to review", "target_issue_status": "review",
			}
		},
	},
	{
		name: "claim_issue/review",
		tool: "claim_issue",
		args: func(_ *testing.T, state *conformanceState) map[string]any {
			return map[string]any{"issue_id": state.issueDisplayID, "lease_seconds": float64(900)}
		},
		capture: func(t *testing.T, state *conformanceState, result *sdkmcp.CallToolResult) {
			state.attemptID = structuredField[string](t, result, "attempt", "id")
			state.leaseToken = structuredField[string](t, result, "lease_token")
		},
	},
	{
		name: "save_attempt_note",
		tool: "save_attempt_note",
		args: func(_ *testing.T, state *conformanceState) map[string]any {
			return map[string]any{
				"attempt_id": state.attemptID, "lease_token": state.leaseToken,
				"kind": "progress", "content": "reviewing",
			}
		},
	},
	{
		name: "finish_attempt/review_to_done",
		tool: "finish_attempt",
		args: func(_ *testing.T, state *conformanceState) map[string]any {
			return map[string]any{
				"attempt_id": state.attemptID, "lease_token": state.leaseToken,
				"outcome": "completed", "result_summary": "approved", "review_outcome": "approved",
			}
		},
	},
	{
		name: "manage_issue_relation",
		tool: "manage_issue_relation",
		args: func(_ *testing.T, state *conformanceState) map[string]any {
			return map[string]any{
				"action": "add", "source_issue_id": state.epicID, "target_issue_id": state.issueDisplayID,
				"relation_type": "related_to",
			}
		},
	},
	{
		name: "create_issue/archivable",
		tool: "create_issue",
		args: func(_ *testing.T, _ *conformanceState) map[string]any {
			return map[string]any{"type": "task", "title": "Conformance archivable", "priority": "low"}
		},
		capture: func(t *testing.T, state *conformanceState, result *sdkmcp.CallToolResult) {
			state.archivableID = structuredField[string](t, result, "id")
		},
	},
	{
		name: "archive_issue",
		tool: "archive_issue",
		args: func(_ *testing.T, state *conformanceState) map[string]any {
			return map[string]any{"issue_id": state.archivableID, "expected_version": float64(1)}
		},
	},
	{
		name: "end_agent_session",
		tool: "end_agent_session",
		args: func(_ *testing.T, state *conformanceState) map[string]any {
			return map[string]any{"agent_session_handle": state.sessionHandle}
		},
	},
	{
		name: "export_project",
		tool: "export_project",
		args: func(_ *testing.T, _ *conformanceState) map[string]any {
			return map[string]any{"delivery": "inline"}
		},
		capture: func(t *testing.T, state *conformanceState, result *sdkmcp.CallToolResult) {
			state.exportedDocument = result.StructuredContent
		},
	},
	{
		name: "validate_import",
		tool: "validate_import",
		args: func(t *testing.T, state *conformanceState) map[string]any {
			doc, err := json.Marshal(state.exportedDocument)
			if err != nil {
				t.Fatalf("marshal exported document: %v", err)
			}
			return map[string]any{"document": string(doc)}
		},
	},
	{
		name: "validate_issue_plan",
		tool: "validate_issue_plan",
		args: func(t *testing.T, _ *conformanceState) map[string]any {
			return map[string]any{
				"issues": []map[string]any{
					{
						"ref":      "plan-task-1",
						"type":     "task",
						"title":    "Plan issue",
						"priority": "medium",
						"status":   "ready",
					},
				},
				"relations": []map[string]any{},
				"decisions": []map[string]any{},
			}
		},
	},
	{
		name: "apply_issue_plan",
		tool: "apply_issue_plan",
		args: func(t *testing.T, _ *conformanceState) map[string]any {
			return map[string]any{
				"issues": []map[string]any{
					{
						"ref":      "plan-task-2",
						"type":     "task",
						"title":    "Applied plan issue",
						"priority": "low",
						"status":   "ready",
					},
				},
				"relations":       []map[string]any{},
				"decisions":       []map[string]any{},
				"idempotency_key": "apply-plan-1",
			}
		},
	},
}

// TestOutputSchemaConformanceApplyImport tests apply_import with an empty
// destination database, verifying the tool's output validates against its
// advertised OutputSchema. It uses export_project's output from a separate
// conformance instance as the import document.
func TestOutputSchemaConformanceApplyImport(t *testing.T) {
	ctx := context.Background()
	// Create two databases: one to export from, one to import into (empty).
	exportDB, exportSource := openDatabase(t, filepath.Join(t.TempDir(), "export.db"))
	defer exportDB.Close(ctx)
	importDB, importSource := openDatabase(t, filepath.Join(t.TempDir(), "import.db"))
	defer importDB.Close(ctx)

	// Create client from export DB to generate export data.
	exportClient, exportStop := newClient(t, composeServices(t, exportDB, exportSource))
	defer exportStop()

	// Create test content in export database.
	createdIssue := call(t, exportClient, "create_issue", map[string]any{
		"type": "task", "title": "Export test", "priority": "medium",
	})
	if createdIssue.IsError {
		t.Fatalf("create issue for export = %#v", createdIssue)
	}

	// Export the project.
	exported := call(t, exportClient, "export_project", map[string]any{"delivery": "inline"})
	if exported.IsError {
		t.Fatalf("export_project = %#v", exported)
	}

	// Resolve schemas to validate the import result.
	allTools, err := exportClient.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	var importToolSchema *jsonschema.Resolved
	for _, tool := range allTools.Tools {
		if tool.Name == "apply_import" {
			schema := decodeOutputSchema(t, tool)
			if schema == nil {
				t.Fatalf("apply_import has no output schema")
			}
			resolved, err := schema.Resolve(nil)
			if err != nil {
				t.Fatalf("resolve apply_import output schema: %v", err)
			}
			importToolSchema = resolved
			break
		}
	}
	if importToolSchema == nil {
		t.Fatalf("apply_import tool not found in catalog")
	}

	// Create client from empty import DB and apply the export.
	importClient, importStop := newClient(t, composeServices(t, importDB, importSource))
	defer importStop()

	doc, err := json.Marshal(exported.StructuredContent)
	if err != nil {
		t.Fatalf("marshal exported document: %v", err)
	}
	applied := call(t, importClient, "apply_import", map[string]any{"document": string(doc)})
	if applied.IsError {
		var text string
		if len(applied.Content) > 0 {
			if tc, ok := applied.Content[0].(*sdkmcp.TextContent); ok {
				text = tc.Text
			}
		}
		t.Fatalf("apply_import failed: %s (structured=%#v)", text, applied.StructuredContent)
	}

	// Validate result against schema.
	if err := importToolSchema.Validate(applied.StructuredContent); err != nil {
		t.Errorf("apply_import structuredContent failed its advertised OutputSchema: %v\noutput: %#v", err, applied.StructuredContent)
	}
}

// TestOutputSchemaConformanceReviewRequests tests the review request tool
// lifecycle (get_review_request, list_review_requests, cancel_review_request,
// replace_review_request) with a separate issue and review attempt, distinct
// from the main conformance scenario to avoid interference.
func TestOutputSchemaConformanceReviewRequests(t *testing.T) {
	ctx := context.Background()
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "review-requests.db"))
	defer db.Close(ctx)
	client, stop := newClient(t, composeServices(t, db, source))
	defer stop()

	// Resolve all schemas for review-request tools.
	allTools, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	resolved := make(map[string]*jsonschema.Resolved)
	for _, tool := range allTools.Tools {
		if tool.Name == "get_review_request" || tool.Name == "list_review_requests" ||
			tool.Name == "cancel_review_request" || tool.Name == "replace_review_request" {
			schema := decodeOutputSchema(t, tool)
			if schema == nil {
				continue
			}
			r, err := schema.Resolve(nil)
			if err != nil {
				t.Fatalf("resolve %s output schema: %v", tool.Name, err)
			}
			resolved[tool.Name] = r
		}
	}

	// Create a review request using direct repository access (create_review_request MCP tool is deprecated).
	reviewRepository, err := sqlite.NewReviewRepository(db)
	if err != nil {
		t.Fatalf("new review repository: %v", err)
	}

	// Create a review-status issue for get/list testing.
	createdIssue1 := call(t, client, "create_issue", map[string]any{
		"type": "task", "title": "Review request 1", "status": "review",
	})
	if createdIssue1.IsError {
		t.Fatalf("create issue 1 = %#v", createdIssue1)
	}
	var issueOutput1 struct {
		ID string `json:"id"`
	}
	decodeStructured(t, createdIssue1, &issueOutput1)

	firstTargetVersion, firstTargetEventID := currentReviewTargetPosition(t, db, issueOutput1.ID)

	// Create first review request: for get_review_request and list_review_requests testing.
	firstRequest, err := reviewRepository.CreateReviewRequest(ctx, ports.CreateReviewRequestCommand{
		Purposes:           []string{"implementation"},
		RequestID:          "01ARZ3NDEKTSV4RRFFQ69G5FB1",
		TargetID:           "01ARZ3NDEKTSV4RRFFQ69G5FB2",
		IssueID:            issueOutput1.ID,
		TargetIssueVersion: firstTargetVersion,
		TargetEventID:      firstTargetEventID,
		OccurredAt:         source.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create first review request: %v", err)
	}

	// Test get_review_request.
	gotRequest := call(t, client, "get_review_request", map[string]any{
		"review_request_id": firstRequest.Request.ID,
	})
	if gotRequest.IsError {
		t.Fatalf("get_review_request failed: %#v", gotRequest)
	}
	if err := resolved["get_review_request"].Validate(gotRequest.StructuredContent); err != nil {
		t.Errorf("get_review_request output failed schema validation: %v\noutput: %#v", err, gotRequest.StructuredContent)
	}

	// Test list_review_requests.
	listedRequests := call(t, client, "list_review_requests", map[string]any{
		"status": "open", "limit": 10,
	})
	if listedRequests.IsError {
		t.Fatalf("list_review_requests failed: %#v", listedRequests)
	}
	if err := resolved["list_review_requests"].Validate(listedRequests.StructuredContent); err != nil {
		t.Errorf("list_review_requests output failed schema validation: %v\noutput: %#v", err, listedRequests.StructuredContent)
	}

	// Create a second review-status issue for cancel testing.
	createdIssue2 := call(t, client, "create_issue", map[string]any{
		"type": "task", "title": "Review request 2", "status": "review",
	})
	if createdIssue2.IsError {
		t.Fatalf("create issue 2 = %#v", createdIssue2)
	}
	var issueOutput2 struct {
		ID string `json:"id"`
	}
	decodeStructured(t, createdIssue2, &issueOutput2)

	secondTargetVersion, secondTargetEventID := currentReviewTargetPosition(t, db, issueOutput2.ID)

	// Create a second review request for cancel testing.
	secondRequest, err := reviewRepository.CreateReviewRequest(ctx, ports.CreateReviewRequestCommand{
		Purposes:           []string{"implementation"},
		RequestID:          "01ARZ3NDEKTSV4RRFFQ69G5FB3",
		TargetID:           "01ARZ3NDEKTSV4RRFFQ69G5FB4",
		IssueID:            issueOutput2.ID,
		TargetIssueVersion: secondTargetVersion,
		TargetEventID:      secondTargetEventID,
		OccurredAt:         source.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("create second review request: %v", err)
	}

	// Test cancel_review_request.
	cancelled := call(t, client, "cancel_review_request", map[string]any{
		"review_request_id": secondRequest.Request.ID,
		"expected_version":  1,
	})
	if cancelled.IsError {
		t.Fatalf("cancel_review_request failed: %#v", cancelled)
	}
	if err := resolved["cancel_review_request"].Validate(cancelled.StructuredContent); err != nil {
		t.Errorf("cancel_review_request output failed schema validation: %v\noutput: %#v", err, cancelled.StructuredContent)
	}

	// Create a third review-status issue for replace testing.
	createdIssue3 := call(t, client, "create_issue", map[string]any{
		"type": "task", "title": "Review request 3", "status": "review",
	})
	if createdIssue3.IsError {
		t.Fatalf("create issue 3 = %#v", createdIssue3)
	}
	var issueOutput3 struct {
		ID string `json:"id"`
	}
	decodeStructured(t, createdIssue3, &issueOutput3)

	thirdTargetVersion, thirdTargetEventID := currentReviewTargetPosition(t, db, issueOutput3.ID)

	// Create a third review request for replace testing.
	thirdRequest, err := reviewRepository.CreateReviewRequest(ctx, ports.CreateReviewRequestCommand{
		Purposes:           []string{"implementation"},
		RequestID:          "01ARZ3NDEKTSV4RRFFQ69G5FB5",
		TargetID:           "01ARZ3NDEKTSV4RRFFQ69G5FB6",
		IssueID:            issueOutput3.ID,
		TargetIssueVersion: thirdTargetVersion,
		TargetEventID:      thirdTargetEventID,
		OccurredAt:         source.Now().UTC().Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create third review request: %v", err)
	}

	// Test replace_review_request. The successor freezes the issue's new
	// state, since a replace onto a target that no longer matches the issue
	// is rejected as stale (ISSUE-188).
	successorVersion, successorEventID := advanceReviewedIssueForTest(t, db, issueOutput3.ID, source.Now().UTC())
	replaced := call(t, client, "replace_review_request", map[string]any{
		"predecessor_request_id":       thirdRequest.Request.ID,
		"predecessor_expected_version": 1,
		"target_issue_version":         successorVersion,
		"target_event_id":              successorEventID,
		"idempotency_key":              "replace-key-conformance-1",
	})
	if replaced.IsError {
		t.Fatalf("replace_review_request failed: %#v", replaced)
	}
	if err := resolved["replace_review_request"].Validate(replaced.StructuredContent); err != nil {
		t.Errorf("replace_review_request output failed schema validation: %v\noutput: %#v", err, replaced.StructuredContent)
	}
}
