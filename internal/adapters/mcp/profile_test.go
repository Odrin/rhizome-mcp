package mcp_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpadapter "rhizome-mcp/internal/adapters/mcp"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/projectrouting"
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
func TestToolSessionResolutionUsesRoutedBundle(t *testing.T) {
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "profile-session.db"))
	defer db.Close(context.Background())

	leaseDB, leaseSource := openDatabase(t, filepath.Join(t.TempDir(), "profile-session-lease.db"))
	defer leaseDB.Close(context.Background())
	leaseOptions := composeServices(t, leaseDB, leaseSource)
	leaseServices := servicesFromOptions(t, leaseOptions)
	clientVersion := "1.0.0"
	agentLabel := "lease-agent"
	created, err := leaseServices.SessionService.CreateWithHandle(context.Background(), domain.CreateAgentSessionInput{
		ClientName:    "lease-client",
		ClientVersion: &clientVersion,
		AgentLabel:    &agentLabel,
	})
	if err != nil {
		t.Fatalf("CreateWithHandle() error = %v", err)
	}

	releaseCount := 0
	lease := &trackingLease{ProjectLease: projectrouting.NewStaticLease(projectID, leaseServices), releaseCount: &releaseCount}
	router := &trackingRouter{lease: lease}
	options := composeServices(t, db, source)
	options.ProjectRouter = router
	client, stop := newClient(t, options)
	defer stop()

	result := call(t, client, "get_project", map[string]any{"agent_session_handle": created.Handle, "include_instructions": true})
	if result.IsError {
		t.Fatalf("get_project result = %#v", result)
	}
	if releaseCount != 2 {
		t.Fatalf("lease release count = %d, want 2 (bootstrap + tool request)", releaseCount)
	}
}

// TestTouchSessionForMutatingToolRejectsUnknownHandleAsStructuredError proves
// an unknown agent_session_handle reaches the client as the documented
// SESSION_NOT_FOUND structured code rather than a raw error, end-to-end
// through touchSessionForMutatingTool's ResolveAndTouch call.
func TestTouchSessionForMutatingToolRejectsUnknownHandleAsStructuredError(t *testing.T) {
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "profile-session-not-found.db"))
	defer db.Close(context.Background())
	options := composeServices(t, db, source)
	client, stop := newClient(t, options)
	defer stop()

	result := call(t, client, "create_issue", map[string]any{
		"agent_session_handle": "unknown-handle",
		"title":                "test issue",
		"type":                 "task",
	})
	assertDomainError(t, result, "SESSION_NOT_FOUND", false)
}

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
// profile is exactly the documented minimal set: project opening and
// metadata plus export/validate/apply import.
func TestToolProfileMigrationIsMinimalTransferWorkflow(t *testing.T) {
	names := toolNamesFor(t, "migration")
	want := []string{"apply_import", "export_project", "get_project", "open_project", "validate_import"}
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
		"create_agent_session", "end_agent_session",
		"get_project", "open_project", "create_issue", "update_issue", "get_issue", "list_issues", "archive_issue",
		"manage_issue_relation", "get_issue_graph", "get_planning_graph", "validate_issue_plan", "apply_issue_plan",
		"get_review_request", "list_review_requests", "cancel_review_request",
		"replace_review_request", "add_comment", "record_decision", "list_decisions",
		"get_issue_activity", "search", "claim_issue", "renew_attempt", "save_attempt_note", "finish_attempt",
		"get_work_context", "list_labels",
		"reserve_resources", "release_resources", "list_resource_reservations", "get_resource_reservation",
		// ISSUE-174: an agent must be able to satisfy a gate and to see why one
		// failed. It must NOT be able to rewrite the policies that define the
		// gates, so manage_workflow_policy and the two policy reads are
		// groupGovernance and deliberately absent here.
		"submit_gate_evidence", "evaluate_gates",
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

// TestToolProfileReadOnlyIgnoresGroupCoreBypassForMutatingTool is the
// ISSUE-99 regression test for toolProfileIncludes itself: groupCore's
// "always advertised" rule (so open_project and get_project are never
// excluded, letting a client route explicitly and diagnose a missing tool)
// must not let a hypothetical
// future mutating core tool into the read-only profile. Every other
// profile still treats groupCore as unconditional.
func TestToolProfileReadOnlyIgnoresGroupCoreBypassForMutatingTool(t *testing.T) {
	if mcpadapter.ToolProfileIncludesCoreToolForTest("read-only", false) {
		t.Fatal("read-only profile included a mutating (readOnlyHint: false) core tool")
	}
	if !mcpadapter.ToolProfileIncludesCoreToolForTest("read-only", true) {
		t.Fatal("read-only profile excluded a read-only core tool")
	}
	for _, profile := range []string{"full", "agent", "migration"} {
		if !mcpadapter.ToolProfileIncludesCoreToolForTest(profile, false) {
			t.Errorf("%s profile excluded a mutating core tool, want groupCore's unconditional inclusion preserved", profile)
		}
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
