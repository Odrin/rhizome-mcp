//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const integrationMaxContextPackBytes = 16 * 1024

func latestEventIDViaTool(t *testing.T, session *mcp.ClientSession, projectRef string) int64 {
	t.Helper()
	args := map[string]any{"since_event_id": 0, "limit": 1}
	if projectRef != "" {
		args["project_ref"] = projectRef
	}
	res := callIntegrationTool(t, session, "get_changes", args)
	if res.IsError {
		t.Fatalf("get_changes(%q) failed: %#v", projectRef, res)
	}
	var out struct {
		LatestEventID int64 `json:"latest_event_id"`
	}
	decodeIntegrationResult(t, res, &out)
	return out.LatestEventID
}

func assertIntegrationJSONRPCError(t *testing.T, err error, wantCode int64, wantSubstrings ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected JSON-RPC resource error, got nil")
	}
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("error = %T (%v), want *jsonrpc.Error", err, err)
	}
	if rpcErr.Code != wantCode {
		t.Fatalf("JSON-RPC error code = %d, want %d (msg=%q)", rpcErr.Code, wantCode, rpcErr.Message)
	}
	lowerMsg := strings.ToLower(rpcErr.Message)
	for _, substr := range wantSubstrings {
		if !strings.Contains(lowerMsg, strings.ToLower(substr)) {
			t.Fatalf("JSON-RPC error message %q missing %q", rpcErr.Message, substr)
		}
	}
}

func TestIntegrationResourcesSharedStdioRoutingAndErrors(t *testing.T) {
	dataRoot := filepath.Join(t.TempDir(), "data")
	repoA := t.TempDir()
	repoB := t.TempDir()
	for _, repo := range []string{repoA, repoB} {
		env := integrationEnvironment{repository: repo, dataRoot: dataRoot}
		runIntegrationCommand(t, env, "--data-root", dataRoot, "init")
	}

	session := connectBareServeFromDirectory(t, dataRoot, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	// ListResources must stay strictly the 3 static guides; dynamic packs live in ListResourceTemplates.
	resources, err := session.ListResources(ctx, &mcp.ListResourcesParams{})
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	if len(resources.Resources) != 3 {
		t.Fatalf("ListResources() returned %d resources, want 3 static guides", len(resources.Resources))
	}
	templates, err := session.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{})
	if err != nil {
		t.Fatalf("ListResourceTemplates() error = %v", err)
	}
	if len(templates.ResourceTemplates) != 3 {
		t.Fatalf("ListResourceTemplates() returned %d templates, want 3", len(templates.ResourceTemplates))
	}

	openProject := func(root string) string {
		res := callIntegrationTool(t, session, "open_project", map[string]any{"project_root": root})
		if res.IsError {
			t.Fatalf("open_project(%q) failed: %#v", root, res)
		}
		var out struct {
			ProjectRef string `json:"project_ref"`
		}
		decodeIntegrationResult(t, res, &out)
		return out.ProjectRef
	}
	refA := openProject(repoA)
	refB := openProject(repoB)
	if refA == "" || refB == "" || refA == refB {
		t.Fatalf("unexpected project refs A=%q B=%q", refA, refB)
	}

	// Seed project A: blocker -> target task (with decision), ready task, and an archived issue.
	blockerA := callIntegrationTool(t, session, "create_issue", map[string]any{
		"project_ref": refA,
		"type":        "task",
		"title":       "Project A blocker",
		"status":      "ready",
	})
	var blockerAOut struct {
		ID string `json:"id"`
	}
	decodeIntegrationResult(t, blockerA, &blockerAOut)

	targetA := callIntegrationTool(t, session, "create_issue", map[string]any{
		"project_ref":         refA,
		"type":                "task",
		"title":               "Project A target task",
		"description":         "Cross-project isolation verification in repo A",
		"acceptance_criteria": "Brief contains blocker and decision",
		"status":              "ready",
		"priority":            "high",
	})
	var targetAOut struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeIntegrationResult(t, targetA, &targetAOut)

	if res := callIntegrationTool(t, session, "manage_issue_relation", map[string]any{
		"project_ref":     refA,
		"action":          "add",
		"source_issue_id": blockerAOut.ID,
		"target_issue_id": targetAOut.ID,
		"relation_type":   "blocks",
	}); res.IsError {
		t.Fatalf("manage_issue_relation A failed: %#v", res)
	}
	if res := callIntegrationTool(t, session, "record_decision", map[string]any{
		"project_ref": refA,
		"issue_id":    targetAOut.ID,
		"title":       "Repo A architectural choice",
		"summary":     "Keep context pack bounded",
		"content":     "Details",
	}); res.IsError {
		t.Fatalf("record_decision A failed: %#v", res)
	}

	archivedA := callIntegrationTool(t, session, "create_issue", map[string]any{
		"project_ref": refA,
		"type":        "task",
		"title":       "Archived task A",
	})
	var archivedAOut struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}
	decodeIntegrationResult(t, archivedA, &archivedAOut)
	if res := callIntegrationTool(t, session, "archive_issue", map[string]any{
		"project_ref":      refA,
		"issue_id":         archivedAOut.ID,
		"expected_version": archivedAOut.Version,
	}); res.IsError {
		t.Fatalf("archive_issue A failed: %#v", res)
	}

	// Seed project B independently.
	issueB := callIntegrationTool(t, session, "create_issue", map[string]any{
		"project_ref": refB,
		"type":        "bug",
		"title":       "Project B bug",
		"status":      "ready",
	})
	var issueBOut struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeIntegrationResult(t, issueB, &issueBOut)

	eventsBeforeA := latestEventIDViaTool(t, session, refA)
	eventsBeforeB := latestEventIDViaTool(t, session, refB)

	// Read explicit project-routed issue briefs for both projects.
	readIssueA, err := session.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "rhizome://projects/" + refA + "/issues/" + targetAOut.DisplayID + "/brief",
	})
	if err != nil {
		t.Fatalf("ReadResource(issue A) error = %v", err)
	}
	var briefA struct {
		ProjectRef string `json:"project_ref"`
		Issue      struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"issue"`
		Blockers []struct {
			ID string `json:"id"`
		} `json:"blockers"`
		Decisions []struct {
			Title string `json:"title"`
		} `json:"decisions"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(readIssueA.Contents[0].Text), &briefA); err != nil {
		t.Fatalf("unmarshal issue brief A: %v", err)
	}
	if briefA.ProjectRef != refA || briefA.Issue.ID != targetAOut.ID || len(briefA.Blockers) != 1 || len(briefA.Decisions) != 1 || briefA.Truncated {
		t.Fatalf("unexpected briefA = %+v", briefA)
	}

	readIssueB, err := session.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "rhizome://projects/" + refB + "/issues/" + issueBOut.DisplayID + "/brief",
	})
	if err != nil {
		t.Fatalf("ReadResource(issue B) error = %v", err)
	}
	var briefB struct {
		ProjectRef string `json:"project_ref"`
		Issue      struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"issue"`
	}
	if err := json.Unmarshal([]byte(readIssueB.Contents[0].Text), &briefB); err != nil {
		t.Fatalf("unmarshal issue brief B: %v", err)
	}
	if briefB.ProjectRef != refB || briefB.Issue.ID != issueBOut.ID || briefB.Issue.Title != "Project B bug" {
		t.Fatalf("unexpected briefB = %+v", briefB)
	}

	// Read explicit project briefs for A and B.
	projARead, err := session.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "rhizome://projects/" + refA + "/brief",
	})
	if err != nil {
		t.Fatalf("ReadResource(project A) error = %v", err)
	}
	var projA struct {
		Project struct {
			ProjectRef string `json:"project_ref"`
		} `json:"project"`
		Summary struct {
			BlockedCount int64 `json:"blocked_count"`
		} `json:"summary"`
		ReadyIssues []struct {
			ID string `json:"id"`
		} `json:"ready_issues"`
		BlockedIssues []struct {
			ID string `json:"id"`
		} `json:"blocked_issues"`
	}
	if err := json.Unmarshal([]byte(projARead.Contents[0].Text), &projA); err != nil {
		t.Fatalf("unmarshal project brief A: %v", err)
	}
	if projA.Project.ProjectRef != refA || projA.Summary.BlockedCount != 1 ||
		len(projA.ReadyIssues) != 1 || projA.ReadyIssues[0].ID != blockerAOut.ID ||
		len(projA.BlockedIssues) != 1 || projA.BlockedIssues[0].ID != targetAOut.ID {
		t.Fatalf("unexpected projA = %+v", projA)
	}

	// Default-project fallback fails without a configured default project on bare shared server.
	_, err = session.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "rhizome://issues/" + targetAOut.DisplayID + "/brief",
	})
	assertIntegrationJSONRPCError(t, err, mcp.CodeResourceNotFound, "no default project")

	// Invalid project_ref and archived issue errors.
	_, err = session.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "rhizome://projects/invalid-ref/brief",
	})
	assertIntegrationJSONRPCError(t, err, jsonrpc.CodeInvalidParams, "project_ref")

	_, err = session.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "rhizome://projects/" + refA + "/issues/" + archivedAOut.ID + "/brief",
	})
	assertIntegrationJSONRPCError(t, err, jsonrpc.CodeInvalidParams, "archived", "issue_id")

	// Unknown project ULID -> ResourceNotFoundError.
	unknownRef := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	if unknownRef == refA || unknownRef == refB {
		unknownRef = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	}
	_, err = session.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "rhizome://projects/" + unknownRef + "/brief",
	})
	assertIntegrationJSONRPCError(t, err, mcp.CodeResourceNotFound)

	// Confirm zero durable writes across all resource reads.
	if got := latestEventIDViaTool(t, session, refA); got != eventsBeforeA {
		t.Fatalf("project A latest_event_id changed after resource reads: before=%d after=%d", eventsBeforeA, got)
	}
	if got := latestEventIDViaTool(t, session, refB); got != eventsBeforeB {
		t.Fatalf("project B latest_event_id changed after resource reads: before=%d after=%d", eventsBeforeB, got)
	}
}

func TestIntegrationResourcesDefaultProjectFallbackStdio(t *testing.T) {
	env := newIntegrationEnvironment(t)
	session := env.connect(t)
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	// Oversized description triggers 16 KiB deterministic truncation over real stdio serve.
	bigDesc := strings.Repeat("Multi-byte café 🚀 <detail> ", 900)
	created := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type":                "task",
		"title":               "Default project fallback brief",
		"description":         bigDesc,
		"acceptance_criteria": "Must fit strictly within 16 KiB",
		"status":              "ready",
	})
	var issueOut struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeIntegrationResult(t, created, &issueOut)

	eventsBefore := latestEventIDViaTool(t, session, "")

	readRes, err := session.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "rhizome://issues/" + issueOut.DisplayID + "/brief",
	})
	if err != nil {
		t.Fatalf("ReadResource(default fallback) error = %v", err)
	}
	if len(readRes.Contents) != 1 || readRes.Contents[0].MIMEType != "application/json" {
		t.Fatalf("unexpected resource contents: %#v", readRes.Contents)
	}
	raw := readRes.Contents[0].Text
	if len(raw) > integrationMaxContextPackBytes {
		t.Fatalf("default brief byte length = %d, want <= %d", len(raw), integrationMaxContextPackBytes)
	}
	if !utf8.ValidString(raw) {
		t.Fatal("default brief is not valid UTF-8")
	}
	var doc struct {
		ProjectRef       string  `json:"project_ref"`
		Truncated        bool    `json:"truncated"`
		TruncationReason *string `json:"truncation_reason"`
		Issue            struct {
			ID string `json:"id"`
		} `json:"issue"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("unmarshal default brief: %v", err)
	}
	if doc.ProjectRef == "" || doc.Issue.ID != issueOut.ID {
		t.Fatalf("unexpected default brief metadata: %+v", doc)
	}
	if !doc.Truncated || doc.TruncationReason == nil || *doc.TruncationReason != "byte_budget" {
		t.Fatalf("truncation = (%v, %v), want (true, byte_budget)", doc.Truncated, doc.TruncationReason)
	}

	if got := latestEventIDViaTool(t, session, ""); got != eventsBefore {
		t.Fatalf("default fallback brief read changed latest_event_id: before=%d after=%d", eventsBefore, got)
	}
}
