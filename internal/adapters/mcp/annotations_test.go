package mcp_test

import (
	"context"
	"path/filepath"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// expectedToolHints is the reviewable annotation matrix asserted against the
// live catalog. Adding a tool without a matching entry here (or vice versa)
// fails the test below, so catalog/matrix drift is caught in CI rather than
// discovered at runtime. See docs/03-mcp-tools.md for the rationale behind
// every non-obvious classification.
var expectedToolHints = map[string]sdkmcp.ToolAnnotations{
	"create_agent_session":       {ReadOnlyHint: false, DestructiveHint: boolPointer(false), IdempotentHint: false, OpenWorldHint: boolPointer(false)},
	"end_agent_session":          {ReadOnlyHint: false, DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"get_project":                {ReadOnlyHint: true, DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"open_project":               {ReadOnlyHint: true, DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"export_project":             {ReadOnlyHint: false, DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"validate_import":            {ReadOnlyHint: true, DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"apply_import":               {ReadOnlyHint: false, DestructiveHint: boolPointer(true), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"list_labels":                {ReadOnlyHint: true, DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"create_issue":               {ReadOnlyHint: false, DestructiveHint: boolPointer(false), IdempotentHint: false, OpenWorldHint: boolPointer(false)},
	"update_issue":               {ReadOnlyHint: false, DestructiveHint: boolPointer(true), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"get_issue":                  {ReadOnlyHint: true, DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"list_issues":                {ReadOnlyHint: true, DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"archive_issue":              {ReadOnlyHint: false, DestructiveHint: boolPointer(true), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"cancel_review_request":      {ReadOnlyHint: false, DestructiveHint: boolPointer(true), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"get_review_request":         {ReadOnlyHint: true, DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"list_review_requests":       {ReadOnlyHint: true, DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"manage_issue_relation":      {ReadOnlyHint: false, DestructiveHint: boolPointer(true), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"get_issue_graph":            {ReadOnlyHint: true, DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"get_planning_graph":         {ReadOnlyHint: true, DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"validate_issue_plan":        {ReadOnlyHint: true, DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"apply_issue_plan":           {ReadOnlyHint: false, DestructiveHint: boolPointer(true), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"add_comment":                {ReadOnlyHint: false, DestructiveHint: boolPointer(false), IdempotentHint: false, OpenWorldHint: boolPointer(false)},
	"record_decision":            {ReadOnlyHint: false, DestructiveHint: boolPointer(true), IdempotentHint: false, OpenWorldHint: boolPointer(false)},
	"list_decisions":             {ReadOnlyHint: true, DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"get_issue_activity":         {ReadOnlyHint: true, DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"claim_issue":                {ReadOnlyHint: false, DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"renew_attempt":              {ReadOnlyHint: false, DestructiveHint: boolPointer(false), IdempotentHint: false, OpenWorldHint: boolPointer(false)},
	"replace_review_request":     {ReadOnlyHint: false, DestructiveHint: boolPointer(true), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"save_attempt_note":          {ReadOnlyHint: false, DestructiveHint: boolPointer(false), IdempotentHint: false, OpenWorldHint: boolPointer(false)},
	"finish_attempt":             {ReadOnlyHint: false, DestructiveHint: boolPointer(true), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"get_work_context":           {ReadOnlyHint: true, DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"reserve_resources":          {ReadOnlyHint: false, DestructiveHint: boolPointer(false), IdempotentHint: false, OpenWorldHint: boolPointer(false)},
	"release_resources":          {ReadOnlyHint: false, DestructiveHint: boolPointer(true), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"list_resource_reservations": {ReadOnlyHint: true, DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"get_resource_reservation":   {ReadOnlyHint: true, DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"search":                     {ReadOnlyHint: true, DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	"get_changes":                {ReadOnlyHint: true, DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
}

func boolPointer(value bool) *bool { return &value }

// TestToolAnnotationMatrixMatchesCatalog asserts that every tool advertised by
// tools/list carries exactly the expected annotation set, and that the
// catalog and the expected matrix contain exactly the same tool names in
// both directions: a newly registered tool without a matrix entry, a matrix
// entry for a tool that no longer exists, or a drifted hint value all fail
// this test.
func TestToolAnnotationMatrixMatchesCatalog(t *testing.T) {
	ctx := context.Background()
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "annotations.db"))
	defer db.Close(ctx)
	client, stop := newClient(t, composeServices(t, db, source))
	defer stop()

	tools, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("no tools advertised")
	}

	seen := make(map[string]bool, len(tools.Tools))
	for _, catalogTool := range tools.Tools {
		seen[catalogTool.Name] = true
		want, ok := expectedToolHints[catalogTool.Name]
		if !ok {
			t.Errorf("tool %q is advertised but has no entry in the expected annotation matrix", catalogTool.Name)
			continue
		}
		if catalogTool.Annotations == nil {
			t.Errorf("tool %q has no annotations at all", catalogTool.Name)
			continue
		}
		got := *catalogTool.Annotations
		if got.ReadOnlyHint != want.ReadOnlyHint {
			t.Errorf("%s: ReadOnlyHint = %v, want %v", catalogTool.Name, got.ReadOnlyHint, want.ReadOnlyHint)
		}
		if !boolPointerEqual(got.DestructiveHint, want.DestructiveHint) {
			t.Errorf("%s: DestructiveHint = %v, want %v", catalogTool.Name, derefBool(got.DestructiveHint), derefBool(want.DestructiveHint))
		}
		if got.IdempotentHint != want.IdempotentHint {
			t.Errorf("%s: IdempotentHint = %v, want %v", catalogTool.Name, got.IdempotentHint, want.IdempotentHint)
		}
		if !boolPointerEqual(got.OpenWorldHint, want.OpenWorldHint) {
			t.Errorf("%s: OpenWorldHint = %v, want %v", catalogTool.Name, derefBool(got.OpenWorldHint), derefBool(want.OpenWorldHint))
		}
	}
	for name := range expectedToolHints {
		if !seen[name] {
			t.Errorf("expected annotation matrix has entry %q but the tool is not advertised", name)
		}
	}
}

func boolPointerEqual(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func derefBool(value *bool) any {
	if value == nil {
		return nil
	}
	return *value
}
