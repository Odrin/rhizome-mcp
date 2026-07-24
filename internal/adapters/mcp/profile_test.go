package mcp_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpadapter "rhizome-mcp/internal/adapters/mcp"
)

// toolNamesFor returns the sorted (lexical, per the SDK's own tools/list
// ordering) tool names advertised under profile.
func toolNamesFor(t *testing.T, profile string) []string {
	t.Helper()
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "profile-"+profile+".db"))
	defer db.Close(context.Background())
	options := composeServices(t, db, source)
	options.ToolProfile = profile
	client, stop := newClient(t, options)
	defer stop()

	tools, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	names := make([]string, len(tools.Tools))
	for index, tool := range tools.Tools {
		names[index] = tool.Name
	}
	return names
}

func containsName(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

// TestToolProfileFullMatchesUnfilteredCatalog asserts that an empty
// (default) profile and an explicit "full" profile both advertise the
// exact same complete catalog asserted by TestToolAnnotationMatrixMatchesCatalog.
func TestToolProfileFullMatchesUnfilteredCatalog(t *testing.T) {
	defaultNames := toolNamesFor(t, "")
	fullNames := toolNamesFor(t, "full")
	if len(defaultNames) != len(expectedToolHints) {
		t.Fatalf("default profile tool count = %d, want %d (full catalog)", len(defaultNames), len(expectedToolHints))
	}
	if len(fullNames) != len(defaultNames) {
		t.Fatalf("full profile tool count = %d, want %d (same as default)", len(fullNames), len(defaultNames))
	}
	for name := range expectedToolHints {
		if !containsName(defaultNames, name) {
			t.Errorf("default profile is missing %q", name)
		}
		if !containsName(fullNames, name) {
			t.Errorf("full profile is missing %q", name)
		}
	}
}

// TestToolProfileReadOnlyContainsOnlyReadOnlyHintedTools enforces the
// acceptance-criteria invariant directly against the ISSUE-53 annotation
// matrix: the read-only profile can never advertise a tool this repo's own
// hints classify as mutating, because it is filtered by the same
// readOnlyHint value, not a separately maintained list.
func TestToolProfileReadOnlyContainsOnlyReadOnlyHintedTools(t *testing.T) {
	names := toolNamesFor(t, "read-only")
	if len(names) == 0 {
		t.Fatal("read-only profile advertised no tools")
	}
	for _, name := range names {
		hints, ok := expectedToolHints[name]
		if !ok {
			t.Fatalf("read-only profile advertised %q, which has no entry in the annotation matrix", name)
		}
		if !hints.ReadOnlyHint {
			t.Errorf("read-only profile advertised %q, which is not readOnlyHint: true", name)
		}
	}
	for name, hints := range expectedToolHints {
		if hints.ReadOnlyHint && !containsName(names, name) {
			t.Errorf("read-only profile is missing read-only tool %q", name)
		}
	}
}

// TestToolProfileMigrationIsMinimalTransferWorkflow asserts the migration
// profile is exactly the documented minimal set: project metadata plus
// export/validate/apply import.
func TestToolProfileMigrationIsMinimalTransferWorkflow(t *testing.T) {
	names := toolNamesFor(t, "migration")
	want := []string{"apply_import", "export_project", "get_project", "validate_import"}
	if len(names) != len(want) {
		t.Fatalf("migration profile tools = %v, want %v", names, want)
	}
	for _, name := range want {
		if !containsName(names, name) {
			t.Errorf("migration profile is missing %q", name)
		}
	}
}

// TestToolProfileAgentExcludesBulkTransferAndSync asserts the agent
// profile keeps the full issue/planning/review/knowledge/lifecycle
// workflow while excluding bulk project transfer and incremental
// synchronization tools.
func TestToolProfileAgentExcludesBulkTransferAndSync(t *testing.T) {
	names := toolNamesFor(t, "agent")
	excluded := []string{"export_project", "validate_import", "apply_import", "get_changes"}
	for _, name := range excluded {
		if containsName(names, name) {
			t.Errorf("agent profile unexpectedly advertised excluded tool %q", name)
		}
	}
	retained := []string{
		"get_project", "create_issue", "update_issue", "get_issue", "list_issues", "archive_issue",
		"manage_issue_relation", "get_issue_graph", "get_planning_graph", "validate_issue_plan", "apply_issue_plan",
		"create_review_request", "get_review_request", "list_review_requests", "cancel_review_request",
		"supersede_review_request", "replace_review_request", "add_comment", "record_decision", "list_decisions",
		"get_issue_activity", "search", "claim_issue", "renew_attempt", "save_attempt_note", "finish_attempt",
		"get_work_context", "list_labels",
	}
	for _, name := range retained {
		if !containsName(names, name) {
			t.Errorf("agent profile is missing %q", name)
		}
	}
	if len(names) != len(retained) {
		t.Fatalf("agent profile tool count = %d, want %d (got %v)", len(names), len(retained), names)
	}
}

// TestToolProfileDisabledToolsAreUncallable asserts a tool excluded by the
// active profile is not only absent from tools/list but genuinely fails as
// an unknown tool when called directly, rather than remaining callable
// through hidden registration.
func TestToolProfileDisabledToolsAreUncallable(t *testing.T) {
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "profile-agent-uncallable.db"))
	defer db.Close(context.Background())
	options := composeServices(t, db, source)
	options.ToolProfile = "agent"
	client, stop := newClient(t, options)
	defer stop()

	_, err := client.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "get_changes", Arguments: map[string]any{"since_event_id": 0}})
	if err == nil {
		t.Fatal("get_changes unexpectedly callable under the agent profile")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unknown tool") && !strings.Contains(strings.ToLower(err.Error()), "not found") {
		t.Fatalf("get_changes call error = %v, want an unknown-tool style error", err)
	}
}

// TestToolProfileRejectsUnknownName asserts NewServer fails startup with a
// structured, actionable error for an unsupported profile name, and that
// blank defaults to full.
func TestToolProfileRejectsUnknownName(t *testing.T) {
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "profile-invalid.db"))
	defer db.Close(context.Background())
	options := composeServices(t, db, source)
	options.ToolProfile = "read-write"
	if _, err := mcpadapter.NewServer(options); err == nil {
		t.Fatal("NewServer unexpectedly accepted an unsupported tool profile")
	} else if !strings.Contains(err.Error(), "read-write") || !strings.Contains(err.Error(), "valid profiles") {
		t.Fatalf("NewServer error = %v, want an actionable message naming the value and valid profiles", err)
	}
}

// TestToolProfileReportedByGetProject asserts get_project exposes the
// active profile so a client can diagnose a missing tool.
func TestToolProfileReportedByGetProject(t *testing.T) {
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "profile-get-project.db"))
	defer db.Close(context.Background())
	options := composeServices(t, db, source)
	options.ToolProfile = "read-only"
	client, stop := newClient(t, options)
	defer stop()

	result := call(t, client, "get_project", map[string]any{})
	var output struct {
		ToolProfile string `json:"tool_profile"`
	}
	decodeStructured(t, result, &output)
	if output.ToolProfile != "read-only" {
		t.Fatalf("get_project tool_profile = %q, want %q", output.ToolProfile, "read-only")
	}
}
