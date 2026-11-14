package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpadapter "rhizome-mcp/internal/adapters/mcp"
	"rhizome-mcp/internal/domain"
)

func TestNewServerRejectsUnsupportedContentMode(t *testing.T) {
	ctx := context.Background()
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "content-mode-invalid.db"))
	defer db.Close(ctx)
	options := composeServices(t, db, source)
	options.ContentMode = "hybrid"

	_, err := mcpadapter.NewServer(options)
	if !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
		t.Fatalf("NewServer(ContentMode=%q) error = %v, want %s", options.ContentMode, err, domain.CodeInvalidArgument)
	}
	if !strings.Contains(err.Error(), "valid modes: auto, structured, text") {
		t.Fatalf("NewServer(ContentMode=%q) error = %v, want valid modes listed", options.ContentMode, err)
	}
}

func TestContentModeAutoDefaultIncludesJSONInTextAndFallsBackOver64KiB(t *testing.T) {
	ctx := context.Background()
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "content-mode-auto.db"))
	defer db.Close(ctx)
	options := composeServices(t, db, source)
	// Leave ContentMode blank to verify auto is the default.
	client, stop := newClient(t, options)
	defer stop()

	tools, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	for _, tool := range tools.Tools {
		if tool.OutputSchema == nil {
			t.Errorf("auto mode tool %q has nil OutputSchema", tool.Name)
		}
	}

	sessionResult := call(t, client, "create_agent_session", map[string]any{
		"client_name": "test-client",
	})
	if sessionResult.IsError {
		t.Fatalf("create_agent_session failed: %#v", sessionResult)
	}
	if sessionResult.StructuredContent == nil {
		t.Fatal("create_agent_session StructuredContent is nil in auto mode")
	}
	sessionText := firstTextContent(t, sessionResult)
	var decodedSession struct {
		AgentSessionHandle string `json:"agent_session_handle"`
		Session            struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.Unmarshal([]byte(sessionText), &decodedSession); err != nil {
		t.Fatalf("auto mode create_agent_session Content[0].Text is not valid JSON (%q): %v", sessionText, err)
	}
	if decodedSession.AgentSessionHandle == "" || decodedSession.Session.ID == "" {
		t.Fatalf("decoded session from Content[0].Text = %#v, want non-empty handle and session ID", decodedSession)
	}

	// Create an issue with a >64 KiB body (description + acceptance_criteria = ~70 KiB).
	largeDescription := strings.Repeat("d", 40*1024)
	largeAcceptance := strings.Repeat("a", 30*1024)
	created := call(t, client, "create_issue", map[string]any{
		"type":                "task",
		"title":               "Large body issue",
		"description":         largeDescription,
		"acceptance_criteria": largeAcceptance,
		"status":              "ready",
	})
	if created.IsError {
		t.Fatalf("create_issue failed: %#v", created)
	}
	// Default create_issue view is compact (<64 KiB), so Content[0].Text is JSON.
	createdText := firstTextContent(t, created)
	var createdCompact struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
		Version   int    `json:"version"`
	}
	if err := json.Unmarshal([]byte(createdText), &createdCompact); err != nil {
		t.Fatalf("auto mode create_issue compact Content[0].Text is not valid JSON (%q): %v", createdText, err)
	}
	if createdCompact.DisplayID != "ISSUE-1" || createdCompact.ID == "" || createdCompact.Version != 1 {
		t.Fatalf("createdCompact = %#v", createdCompact)
	}

	// Explicit opt-in full view exceeds 64 KiB: StructuredContent holds the full
	// object while Content[0].Text falls back to the enriched summary string.
	fullIssue := call(t, client, "get_issue", map[string]any{
		"issue_id": "ISSUE-1",
		"view":     "full",
	})
	if fullIssue.IsError {
		t.Fatalf("get_issue(view=full) failed: %#v", fullIssue)
	}
	if fullIssue.StructuredContent == nil {
		t.Fatal("get_issue(view=full) StructuredContent is nil")
	}
	fullText := firstTextContent(t, fullIssue)
	if !strings.HasPrefix(fullText, "issue returned (display_id=ISSUE-1, id="+createdCompact.ID+", status=ready, version=1)") {
		t.Fatalf("oversized auto mode Content[0].Text = %q, want enriched summary fallback", fullText)
	}
	if len(fullText) > 256 {
		t.Fatalf("oversized auto mode Content[0].Text length = %d, want compact summary fallback", len(fullText))
	}

	// Error response in auto mode serializes errorOutput JSON in Content[0].Text.
	errResult := call(t, client, "get_issue", map[string]any{
		"issue_id": "ISSUE-999",
	})
	if !errResult.IsError {
		t.Fatalf("get_issue(ISSUE-999) unexpectedly succeeded: %#v", errResult)
	}
	errText := firstTextContent(t, errResult)
	var decodedErr struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	}
	if err := json.Unmarshal([]byte(errText), &decodedErr); err != nil {
		t.Fatalf("auto mode error Content[0].Text is not valid JSON (%q): %v", errText, err)
	}
	if decodedErr.Code != domain.CodeIssueNotFound {
		t.Fatalf("decodedErr = %#v, want code %s", decodedErr, domain.CodeIssueNotFound)
	}
}

func TestContentModeStructuredEmitsEnrichedSummariesWithoutJSONDuplication(t *testing.T) {
	ctx := context.Background()
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "content-mode-structured.db"))
	defer db.Close(ctx)
	options := composeServices(t, db, source)
	options.ContentMode = string(domain.ContentModeStructured)
	client, stop := newClient(t, options)
	defer stop()

	sessionResult := call(t, client, "create_agent_session", map[string]any{
		"client_name": "structured-client",
	})
	if sessionResult.IsError {
		t.Fatalf("create_agent_session failed: %#v", sessionResult)
	}
	sessionText := firstTextContent(t, sessionResult)
	if strings.HasPrefix(strings.TrimSpace(sessionText), "{") {
		t.Fatalf("structured mode Content[0].Text = %q, want summary string rather than JSON", sessionText)
	}
	if !strings.Contains(sessionText, "agent session created (session_id=") {
		t.Fatalf("structured mode session summary = %q, want session_id", sessionText)
	}
	if strings.Contains(sessionText, "agent_session_handle") {
		t.Fatalf("structured mode session summary = %q, must not leak agent_session_handle in text summary", sessionText)
	}

	projectResult := call(t, client, "get_project", map[string]any{})
	projectText := firstTextContent(t, projectResult)
	if !strings.Contains(projectText, "project metadata returned (project_ref="+projectID) || !strings.Contains(projectText, "latest_event_id=") {
		t.Fatalf("structured mode get_project summary = %q, want project_ref and latest_event_id", projectText)
	}

	created := call(t, client, "create_issue", map[string]any{
		"type":   "task",
		"title":  "Structured summary check",
		"status": "ready",
	})
	createdText := firstTextContent(t, created)
	if !strings.Contains(createdText, "issue created (display_id=ISSUE-1, id=") || !strings.Contains(createdText, "status=ready, version=1)") {
		t.Fatalf("structured mode create_issue summary = %q, want display_id, id, status, and version", createdText)
	}

	claimed := call(t, client, "claim_issue", map[string]any{
		"issue_id":      "ISSUE-1",
		"lease_seconds": 300,
	})
	claimedText := firstTextContent(t, claimed)
	if !strings.Contains(claimedText, "issue claimed (display_id=ISSUE-1, id=") ||
		!strings.Contains(claimedText, "attempt_id=") {
		t.Fatalf("structured mode claim_issue summary = %q, want display_id, id, and attempt_id", claimedText)
	}
	if strings.Contains(claimedText, "lease_token") {
		t.Fatalf("structured mode claim_issue summary = %q, must not leak lease_token in text summary", claimedText)
	}
}

func TestContentModeTextOmitsStructuredContentAndOutputSchemas(t *testing.T) {
	ctx := context.Background()
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "content-mode-text.db"))
	defer db.Close(ctx)
	options := composeServices(t, db, source)
	options.ContentMode = string(domain.ContentModeText)
	client, stop := newClient(t, options)
	defer stop()

	tools, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	for _, tool := range tools.Tools {
		if tool.OutputSchema != nil {
			t.Errorf("text mode tool %q advertised non-nil OutputSchema: %#v", tool.Name, tool.OutputSchema)
		}
	}

	created := call(t, client, "create_issue", map[string]any{
		"type":   "task",
		"title":  "Text mode issue",
		"status": "ready",
	})
	if created.IsError {
		t.Fatalf("create_issue failed: %#v", created)
	}
	if created.StructuredContent != nil {
		t.Fatalf("text mode StructuredContent = %#v, want nil", created.StructuredContent)
	}
	createdText := firstTextContent(t, created)
	var decoded struct {
		DisplayID string `json:"display_id"`
		Status    string `json:"status"`
		Version   int    `json:"version"`
	}
	if err := json.Unmarshal([]byte(createdText), &decoded); err != nil {
		t.Fatalf("text mode Content[0].Text is not valid JSON (%q): %v", createdText, err)
	}
	if decoded.DisplayID != "ISSUE-1" || decoded.Status != "ready" || decoded.Version != 1 {
		t.Fatalf("decoded text mode create_issue = %#v", decoded)
	}

	errResult := call(t, client, "get_issue", map[string]any{
		"issue_id": "ISSUE-999",
	})
	if !errResult.IsError {
		t.Fatalf("get_issue(ISSUE-999) unexpectedly succeeded: %#v", errResult)
	}
	if errResult.StructuredContent != nil {
		t.Fatalf("text mode error StructuredContent = %#v, want nil", errResult.StructuredContent)
	}
	var decodedErr struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(firstTextContent(t, errResult)), &decodedErr); err != nil {
		t.Fatalf("text mode error Content[0].Text is not valid JSON: %v", err)
	}
	if decodedErr.Code != domain.CodeIssueNotFound {
		t.Fatalf("decodedErr.Code = %q, want %q", decodedErr.Code, domain.CodeIssueNotFound)
	}
}

func TestSuccessReturnsStorageFailureWhenOutputCannotBeMarshaled(t *testing.T) {
	for _, mode := range []domain.ContentMode{domain.ContentModeAuto, domain.ContentModeText} {
		result, _, err := mcpadapter.RenderSuccessForTest(mode, make(chan int), "unmarshalable")
		if err != nil {
			t.Fatalf("RenderSuccessForTest(%s) error = %v", mode, err)
		}
		if !result.IsError {
			t.Fatalf("RenderSuccessForTest(%s) IsError = false, want true", mode)
		}
		var decodedErr struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal([]byte(firstTextContent(t, result)), &decodedErr); err != nil {
			t.Fatalf("RenderSuccessForTest(%s) error text is not valid JSON: %v", mode, err)
		}
		if decodedErr.Code != domain.CodeStorageFailure {
			t.Fatalf("RenderSuccessForTest(%s) code = %q, want %q", mode, decodedErr.Code, domain.CodeStorageFailure)
		}
	}
}

func firstTextContent(t *testing.T, result *sdkmcp.CallToolResult) string {
	t.Helper()
	if result == nil || len(result.Content) == 0 {
		t.Fatalf("result has no Content: %#v", result)
	}
	tc, ok := result.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("result.Content[0] = %T, want *sdkmcp.TextContent", result.Content[0])
	}
	return tc.Text
}
