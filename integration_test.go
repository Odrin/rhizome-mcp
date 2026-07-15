//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const integrationTimeout = 10 * time.Second

var integrationBinary string

func TestMain(m *testing.M) {
	tempDir, err := os.MkdirTemp("", "rhizome-mcp-integration-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create integration build directory: %v\n", err)
		os.Exit(1)
	}

	integrationBinary = filepath.Join(tempDir, "rhizome-mcp")
	command := exec.Command("go", "build", "-o", integrationBinary, ".")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build integration server: %v\n%s", err, output.String())
		exitIntegrationTests(tempDir, 1)
	}

	exitIntegrationTests(tempDir, m.Run())
}

func exitIntegrationTests(tempDir string, exitCode int) {
	if err := os.RemoveAll(tempDir); err != nil {
		fmt.Fprintf(os.Stderr, "remove integration build directory: %v\n", err)
		exitCode = 1
	}
	os.Exit(exitCode)
}

func TestIntegrationSmoke(t *testing.T) {
	env := newIntegrationEnvironment(t)
	runIntegrationCommand(t, env, "--data-root", env.dataRoot, "doctor", "--format", "json")

	session := env.connect(t)
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	if err := session.Ping(ctx, nil); err != nil {
		t.Fatalf("ping server: %v", err)
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	for _, required := range []string{"get_project", "create_issue", "claim_issue", "finish_attempt"} {
		if !hasTool(tools.Tools, required) {
			t.Errorf("server did not expose required tool %q", required)
		}
	}

	result := callIntegrationTool(t, session, "get_project", map[string]any{})
	var project struct {
		AppVersion    string `json:"app_version"`
		ConfigVersion int    `json:"config_version"`
		Project       struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	decodeIntegrationResult(t, result, &project)
	if result.IsError || project.AppVersion == "" || project.ConfigVersion != 1 || project.Project.ID == "" {
		t.Fatalf("get_project result = %#v, decoded = %#v", result, project)
	}
}

func TestIntegrationIssueWorkflow(t *testing.T) {
	env := newIntegrationEnvironment(t)
	session := env.connect(t)

	created := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type":                  "task",
		"title":                 "Complete integration workflow",
		"description":           "Verify the MCP server through its public stdio interface.",
		"status":                "ready",
		"labels":                []string{"integration"},
		"create_missing_labels": true,
	})
	var issue struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
		Status    string `json:"status"`
	}
	decodeIntegrationResult(t, created, &issue)
	if created.IsError || issue.ID == "" || issue.DisplayID == "" || issue.Status != "ready" {
		t.Fatalf("create_issue result = %#v, decoded = %#v", created, issue)
	}

	claimed := callIntegrationTool(t, session, "claim_issue", map[string]any{
		"issue_id":      issue.DisplayID,
		"lease_seconds": 60,
	})
	var claim struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeIntegrationResult(t, claimed, &claim)
	if claimed.IsError || claim.Attempt.ID == "" || claim.LeaseToken == "" {
		t.Fatalf("claim_issue result = %#v, decoded = %#v", claimed, claim)
	}

	note := callIntegrationTool(t, session, "save_attempt_note", map[string]any{
		"attempt_id":  claim.Attempt.ID,
		"lease_token": claim.LeaseToken,
		"kind":        "checkpoint",
		"content":     "Smoke workflow completed through the stdio transport.",
	})
	if note.IsError {
		t.Fatalf("save_attempt_note result = %#v", note)
	}

	renewed := callIntegrationTool(t, session, "renew_attempt", map[string]any{
		"attempt_id":    claim.Attempt.ID,
		"lease_token":   claim.LeaseToken,
		"lease_seconds": 60,
	})
	if renewed.IsError {
		t.Fatalf("renew_attempt result = %#v", renewed)
	}

	finished := callIntegrationTool(t, session, "finish_attempt", map[string]any{
		"attempt_id":          claim.Attempt.ID,
		"lease_token":         claim.LeaseToken,
		"outcome":             "completed",
		"result_summary":      "The integration workflow passed.",
		"target_issue_status": "done",
		"verification":        []string{"go test -tags=integration ."},
	})
	var completion struct {
		Attempt struct {
			Status string `json:"status"`
		} `json:"attempt"`
		Issue struct {
			Status string `json:"status"`
		} `json:"issue"`
		LatestEventID int64 `json:"latest_event_id"`
	}
	decodeIntegrationResult(t, finished, &completion)
	if finished.IsError || completion.Attempt.Status != "completed" || completion.Issue.Status != "done" || completion.LatestEventID == 0 {
		t.Fatalf("finish_attempt result = %#v, decoded = %#v", finished, completion)
	}

	retrieved := callIntegrationTool(t, session, "get_issue", map[string]any{"issue_id": issue.DisplayID})
	var persisted struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	decodeIntegrationResult(t, retrieved, &persisted)
	if retrieved.IsError || persisted.ID != issue.ID || persisted.Status != "done" {
		t.Fatalf("get_issue result = %#v, decoded = %#v", retrieved, persisted)
	}
}

type integrationEnvironment struct {
	repository string
	dataRoot   string
}

func newIntegrationEnvironment(t *testing.T) integrationEnvironment {
	t.Helper()
	tempDir := t.TempDir()
	env := integrationEnvironment{
		repository: filepath.Join(tempDir, "repository"),
		dataRoot:   filepath.Join(tempDir, "data"),
	}
	if err := os.Mkdir(env.repository, 0o755); err != nil {
		t.Fatalf("create test repository: %v", err)
	}
	runIntegrationCommand(t, env, "--data-root", env.dataRoot, "init")
	return env
}

func (env integrationEnvironment) connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	command := exec.Command(integrationBinary, "--data-root", env.dataRoot, "serve")
	command.Dir = env.repository
	client := mcp.NewClient(&mcp.Implementation{Name: "rhizome-integration-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{
		Command:           command,
		TerminateDuration: integrationTimeout,
	}, nil)
	if err != nil {
		t.Fatalf("connect to MCP server: %v", err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close MCP session: %v", err)
		}
	})
	return session
}

func runIntegrationCommand(t *testing.T, env integrationEnvironment, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	command := exec.CommandContext(ctx, integrationBinary, args...)
	command.Dir = env.repository
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("%s failed: %v\nstdout:\n%s\nstderr:\n%s", command.String(), err, stdout.String(), stderr.String())
	}
	return stdout.Bytes()
}

func callIntegrationTool(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s protocol error: %v", name, err)
	}
	return result
}

func decodeIntegrationResult(t *testing.T, result *mcp.CallToolResult, destination any) {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured result: %v", err)
	}
	if err := json.Unmarshal(data, destination); err != nil {
		t.Fatalf("decode structured result %s: %v", data, err)
	}
}

func hasTool(tools []*mcp.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}
