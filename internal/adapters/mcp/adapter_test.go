package mcp_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpadapter "rhizome-mcp/internal/adapters/mcp"
	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/migrations"
)

const projectID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

func TestPhase2ToolsLifecycleAndContracts(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "project.db")
	db, source := openDatabase(t, databasePath)
	client, stop := newClient(t, composeServices(t, db, source))

	tools, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	names := make([]string, len(tools.Tools))
	for i, tool := range tools.Tools {
		names[i] = tool.Name
	}
	// The SDK's feature-set protocol listing is explicitly lexical; registration
	// itself is kept in Phase 2 order in adapter.register.
	wantNames := []string{"archive_issue", "create_issue", "get_issue", "get_project", "list_issues", "list_labels", "update_issue"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("tools = %v, want %v", names, wantNames)
	}
	assertRequired(t, toolNamed(t, tools.Tools, "create_issue"), "type", "title")
	assertRequired(t, toolNamed(t, tools.Tools, "update_issue"), "issue_id", "expected_version", "changes")
	assertUpdateLabelsSchema(t, toolNamed(t, tools.Tools, "update_issue"))
	assertRequired(t, toolNamed(t, tools.Tools, "get_issue"), "issue_id")
	assertRequired(t, toolNamed(t, tools.Tools, "archive_issue"), "issue_id", "expected_version")

	project := call(t, client, "get_project", map[string]any{})
	if project.IsError {
		t.Fatalf("get_project result = %#v", project)
	}
	var projectOutput struct {
		Session                any      `json:"session"`
		AppVersion             string   `json:"app_version"`
		ConfigVersion          int      `json:"config_version"`
		SupportedRelationTypes []string `json:"supported_relation_types"`
		Project                struct {
			Instructions *string `json:"instructions"`
		} `json:"project"`
	}
	decodeStructured(t, project, &projectOutput)
	if projectOutput.Session != nil || projectOutput.AppVersion != "test-version" || projectOutput.ConfigVersion != 1 ||
		projectOutput.Project.Instructions != nil || len(projectOutput.SupportedRelationTypes) != 0 {
		t.Fatalf("project output = %#v", projectOutput)
	}
	projectWithInstructions := call(t, client, "get_project", map[string]any{"include_instructions": true})
	decodeStructured(t, projectWithInstructions, &projectOutput)
	if projectOutput.Project.Instructions == nil || *projectOutput.Project.Instructions != "Use focused changes." {
		t.Fatalf("instructions = %#v", projectOutput.Project.Instructions)
	}

	created := call(t, client, "create_issue", map[string]any{
		"type": "task", "title": "MCP lifecycle", "description": "clear me", "status": "open",
		"priority": "high", "labels": []string{"Database"}, "create_missing_labels": true,
	})
	var issue struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
		Version   int64  `json:"version"`
		Labels    []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Description *string `json:"description"`
	}
	decodeStructured(t, created, &issue)
	if created.IsError || issue.DisplayID != "ISSUE-1" || issue.Version != 1 || issue.Description == nil ||
		len(issue.Labels) != 1 || issue.Labels[0].Name != "Database" {
		t.Fatalf("create result = %#v, issue = %#v", created, issue)
	}

	labels := call(t, client, "list_labels", map[string]any{})
	var labelPage struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	decodeStructured(t, labels, &labelPage)
	if labels.IsError || len(labelPage.Items) != 1 || labelPage.Items[0].Name != "Database" {
		t.Fatalf("labels = %#v", labelPage)
	}

	updated := call(t, client, "update_issue", map[string]any{
		"issue_id": issue.DisplayID, "expected_version": 1,
		"changes": map[string]any{"status": "ready", "description": nil},
	})
	var updatedOutput struct {
		Issue struct {
			Version     int64   `json:"version"`
			Description *string `json:"description"`
			Status      string  `json:"status"`
			Labels      []struct {
				Name string `json:"name"`
			} `json:"labels"`
		} `json:"issue"`
		ChangedFields []string `json:"changed_fields"`
	}
	decodeStructured(t, updated, &updatedOutput)
	if updated.IsError || updatedOutput.Issue.Version != 2 || updatedOutput.Issue.Description != nil ||
		updatedOutput.Issue.Status != "ready" || !reflect.DeepEqual(updatedOutput.ChangedFields, []string{"description", "status"}) {
		t.Fatalf("update output = %#v", updatedOutput)
	}
	if len(updatedOutput.Issue.Labels) != 1 || updatedOutput.Issue.Labels[0].Name != "Database" {
		t.Fatalf("absent labels did not preserve assignments: %#v", updatedOutput.Issue.Labels)
	}

	got := call(t, client, "get_issue", map[string]any{"issue_id": issue.ID, "view": "full"})
	var gotIssue struct {
		DisplayID string `json:"display_id"`
		Version   int64  `json:"version"`
	}
	decodeStructured(t, got, &gotIssue)
	if got.IsError || gotIssue.DisplayID != issue.DisplayID || gotIssue.Version != 2 {
		t.Fatalf("get output = %#v", gotIssue)
	}

	listed := call(t, client, "list_issues", map[string]any{"view": "compact"})
	var issuePage struct {
		Items []struct {
			DisplayID       string `json:"display_id"`
			EffectiveStatus string `json:"effective_status"`
			IsClaimable     bool   `json:"is_claimable"`
		} `json:"items"`
	}
	decodeStructured(t, listed, &issuePage)
	if listed.IsError || len(issuePage.Items) != 1 || issuePage.Items[0].DisplayID != issue.DisplayID ||
		issuePage.Items[0].EffectiveStatus != "ready" || !issuePage.Items[0].IsClaimable {
		t.Fatalf("list output = %#v", issuePage)
	}

	conflict := call(t, client, "update_issue", map[string]any{
		"issue_id": issue.DisplayID, "expected_version": 1, "changes": map[string]any{"title": "stale"},
	})
	assertDomainError(t, conflict, "VERSION_CONFLICT", true)
	invalidParent := call(t, client, "create_issue", map[string]any{
		"type": "task", "title": "invalid parent", "parent_issue_id": "ISSUE-999",
	})
	assertDomainError(t, invalidParent, "INVALID_EPIC_PARENT", false)
	invalidStatus := call(t, client, "create_issue", map[string]any{"type": "task", "title": "invalid status", "status": "in_progress"})
	assertDomainError(t, invalidStatus, "INVALID_ARGUMENT", false)
	invalidTransition := call(t, client, "update_issue", map[string]any{
		"issue_id": issue.DisplayID, "expected_version": 2, "changes": map[string]any{"status": "open"},
	})
	assertDomainError(t, invalidTransition, "INVALID_STATUS_TRANSITION", false)
	labelsNull := call(t, client, "update_issue", map[string]any{
		"issue_id": issue.DisplayID, "expected_version": 2, "changes": map[string]any{"labels": nil},
	})
	if !labelsNull.IsError {
		t.Fatalf("labels null should be rejected by the advertised schema: %#v", labelsNull)
	}
	unsupported := call(t, client, "get_issue", map[string]any{"issue_id": issue.DisplayID, "include": []string{"labels"}})
	assertDomainError(t, unsupported, "INVALID_ARGUMENT", false)
	unsupportedLimits := call(t, client, "get_issue", map[string]any{"issue_id": issue.DisplayID, "limits": map[string]any{"comments": 1}})
	assertDomainError(t, unsupportedLimits, "INVALID_ARGUMENT", false)
	unsupportedListView := call(t, client, "list_issues", map[string]any{"view": "standard"})
	assertDomainError(t, unsupportedListView, "INVALID_ARGUMENT", false)
	unsupportedIdempotency := call(t, client, "create_issue", map[string]any{
		"type": "task", "title": "unsupported idempotency", "idempotency_key": "key",
	})
	assertDomainError(t, unsupportedIdempotency, "INVALID_ARGUMENT", false)

	archived := call(t, client, "archive_issue", map[string]any{"issue_id": issue.DisplayID, "expected_version": 2})
	var archivedIssue struct {
		Version    int64      `json:"version"`
		ArchivedAt *time.Time `json:"archived_at"`
	}
	decodeStructured(t, archived, &archivedIssue)
	if archived.IsError || archivedIssue.Version != 3 || archivedIssue.ArchivedAt == nil {
		t.Fatalf("archive output = %#v", archivedIssue)
	}
	hidden := call(t, client, "list_issues", map[string]any{})
	decodeStructured(t, hidden, &issuePage)
	if hidden.IsError || len(issuePage.Items) != 0 {
		t.Fatalf("default archive visibility = %#v", issuePage)
	}
	visible := call(t, client, "list_issues", map[string]any{"include_archived": true})
	decodeStructured(t, visible, &issuePage)
	if visible.IsError || len(issuePage.Items) != 1 {
		t.Fatalf("archived list visibility = %#v", issuePage)
	}

	stop()
	if err := db.Close(ctx); err != nil {
		t.Fatalf("close before restart: %v", err)
	}
	db, source = reopenDatabase(t, databasePath, source)
	restartedClient, restartedStop := newClient(t, composeServices(t, db, source))
	restarted := call(t, restartedClient, "get_issue", map[string]any{"issue_id": issue.DisplayID})
	decodeStructured(t, restarted, &archivedIssue)
	if restarted.IsError || archivedIssue.Version != 3 || archivedIssue.ArchivedAt == nil {
		t.Fatalf("restarted get = %#v", archivedIssue)
	}
	restartedStop()
	if err := db.Close(ctx); err != nil {
		t.Fatalf("close after restart: %v", err)
	}
}

func openDatabase(t *testing.T, path string) (*sqlite.DB, *clock.FakeClock) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	source := clock.NewFakeClock(time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC))
	if _, err := migrations.Migrate(context.Background(), db, source); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id, instructions, next_issue_number, created_at, updated_at)
			VALUES (?, ?, 1, ?, ?)`, projectID, "Use focused changes.", source.Now().Format(time.RFC3339Nano), source.Now().Format(time.RFC3339Nano))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return db, source
}

func reopenDatabase(t *testing.T, path string, source *clock.FakeClock) (*sqlite.DB, *clock.FakeClock) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Migrate(context.Background(), db, source); err != nil {
		t.Fatal(err)
	}
	return db, source
}

func composeServices(t *testing.T, db *sqlite.DB, source *clock.FakeClock) mcpadapter.Options {
	t.Helper()
	issueRepository, err := sqlite.NewIssueRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	projectRepository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	generator, err := ids.NewGenerator(source, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issues, err := application.NewIssueService(issueRepository, source, generator)
	if err != nil {
		t.Fatal(err)
	}
	projects, err := application.NewProjectService(projectRepository)
	if err != nil {
		t.Fatal(err)
	}
	return mcpadapter.Options{
		IssueService: issues, ProjectService: projects, ServerName: "test-server", ServerVersion: "test-version", ConfigVersion: 1,
	}
}

func newClient(t *testing.T, options mcpadapter.Options) (*sdkmcp.ClientSession, func()) {
	t.Helper()
	server, err := mcpadapter.NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, serverTransport) }()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	var once bool
	return session, func() {
		if once {
			return
		}
		once = true
		_ = session.Close()
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("server.Run() error = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	}
}

func call(t *testing.T, client *sdkmcp.ClientSession, name string, arguments map[string]any) *sdkmcp.CallToolResult {
	t.Helper()
	result, err := client.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s protocol error: %v", name, err)
	}
	return result
}

func decodeStructured(t *testing.T, result *sdkmcp.CallToolResult, destination any) {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, destination); err != nil {
		t.Fatalf("decode structured content %s: %v", data, err)
	}
}

func assertDomainError(t *testing.T, result *sdkmcp.CallToolResult, code string, retryable bool) {
	t.Helper()
	var output struct {
		Code      string            `json:"code"`
		Message   string            `json:"message"`
		Details   []json.RawMessage `json:"details"`
		Retryable bool              `json:"retryable"`
	}
	decodeStructured(t, result, &output)
	if !result.IsError || output.Code != code || output.Message == "" || output.Details == nil || output.Retryable != retryable {
		t.Fatalf("error result = %#v, output = %#v", result, output)
	}
}

func assertRequired(t *testing.T, tool *sdkmcp.Tool, fields ...string) {
	t.Helper()
	data, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}

	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	for _, field := range fields {
		if !contains(schema.Required, field) {
			t.Fatalf("%s required fields %v do not contain %q", tool.Name, schema.Required, field)
		}
	}
}

func assertUpdateLabelsSchema(t *testing.T, tool *sdkmcp.Tool) {
	t.Helper()
	data, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}

	var schema struct {
		Properties struct {
			Changes struct {
				Properties struct {
					Labels struct {
						Type  string   `json:"type"`
						Types []string `json:"types"`
					} `json:"labels"`
				} `json:"properties"`
			} `json:"changes"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties.Changes.Properties.Labels.Type != "array" {
		t.Fatalf("update_issue changes.labels type = %q, want array", schema.Properties.Changes.Properties.Labels.Type)
	}
	if contains(schema.Properties.Changes.Properties.Labels.Types, "null") {
		t.Fatal("update_issue changes.labels schema must not permit null")
	}
}

func toolNamed(t *testing.T, tools []*sdkmcp.Tool, name string) *sdkmcp.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("missing tool %q", name)
	return nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
