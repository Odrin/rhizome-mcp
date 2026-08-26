//go:build integration

package integration_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestIntegrationProjectRoutingSharedStdioServer(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	repoA := filepath.Join(tempDir, "repo-a")
	repoB := filepath.Join(tempDir, "repo-b")
	dataRoot := filepath.Join(tempDir, "data")
	startupDir := filepath.Join(tempDir, "outside")
	for _, dir := range []string{repoA, repoB, startupDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}

	for _, repo := range []string{repoA, repoB} {
		env := integrationEnvironment{repository: repo, dataRoot: dataRoot}
		runIntegrationCommand(t, env, "--data-root", dataRoot, "init")
	}

	session := connectBareServeFromDirectory(t, dataRoot, startupDir)

	missingRef := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type":        "task",
		"title":       "should be rejected without a project",
		"description": "This request should fail before any project is opened.",
		"status":      "ready",
	})
	if !missingRef.IsError {
		t.Fatalf("create_issue without project_ref unexpectedly succeeded: %#v", missingRef)
	}
	if len(missingRef.Content) == 0 {
		t.Fatal("create_issue without project_ref returned no content")
	}
	text := missingRef.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "project_ref is required") {
		t.Fatalf("create_issue without project_ref text = %q, want a project-ref requirement message", text)
	}

	openA := callIntegrationTool(t, session, "open_project", map[string]any{"project_root": repoA})
	if openA.IsError {
		t.Fatalf("open_project for repo A failed: %#v", openA)
	}
	var projectA struct {
		ProjectRef string `json:"project_ref"`
	}
	decodeIntegrationResult(t, openA, &projectA)
	if projectA.ProjectRef == "" {
		t.Fatal("open_project for repo A returned an empty project_ref")
	}

	openB := callIntegrationTool(t, session, "open_project", map[string]any{"project_root": repoB})
	if openB.IsError {
		t.Fatalf("open_project for repo B failed: %#v", openB)
	}
	var projectB struct {
		ProjectRef string `json:"project_ref"`
	}
	decodeIntegrationResult(t, openB, &projectB)
	if projectB.ProjectRef == "" {
		t.Fatal("open_project for repo B returned an empty project_ref")
	}
	if projectA.ProjectRef == projectB.ProjectRef {
		t.Fatalf("open_project returned the same project_ref for both roots: %q", projectA.ProjectRef)
	}

	createdA := callIntegrationTool(t, session, "create_issue", map[string]any{
		"project_ref": projectA.ProjectRef,
		"type":        "task",
		"title":       "alpha issue",
		"status":      "ready",
	})
	if createdA.IsError {
		t.Fatalf("create_issue in project A failed: %#v", createdA)
	}
	createdB := callIntegrationTool(t, session, "create_issue", map[string]any{
		"project_ref": projectB.ProjectRef,
		"type":        "task",
		"title":       "beta issue",
		"status":      "ready",
	})
	if createdB.IsError {
		t.Fatalf("create_issue in project B failed: %#v", createdB)
	}

	listedA := callIntegrationTool(t, session, "list_issues", map[string]any{
		"project_ref": projectA.ProjectRef,
		"limit":       10,
	})
	if listedA.IsError {
		t.Fatalf("list_issues for project A failed: %#v", listedA)
	}
	var pageA struct {
		Items []struct {
			Title string `json:"title"`
		} `json:"items"`
	}
	decodeIntegrationResult(t, listedA, &pageA)
	if len(pageA.Items) != 1 || pageA.Items[0].Title != "alpha issue" {
		t.Fatalf("list_issues for project A = %#v, want one alpha issue", pageA.Items)
	}

	listedB := callIntegrationTool(t, session, "list_issues", map[string]any{
		"project_ref": projectB.ProjectRef,
		"limit":       10,
	})
	if listedB.IsError {
		t.Fatalf("list_issues for project B failed: %#v", listedB)
	}
	var pageB struct {
		Items []struct {
			Title string `json:"title"`
		} `json:"items"`
	}
	decodeIntegrationResult(t, listedB, &pageB)
	if len(pageB.Items) != 1 || pageB.Items[0].Title != "beta issue" {
		t.Fatalf("list_issues for project B = %#v, want one beta issue", pageB.Items)
	}
}

func connectBareServeFromDirectory(t *testing.T, dataRoot, workDir string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	args := []string{"--data-root", dataRoot, "serve"}
	command := exec.Command(integrationBinary, args...)
	command.Dir = workDir
	client := mcp.NewClient(&mcp.Implementation{Name: "rhizome-integration-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{
		Command:           command,
		TerminateDuration: integrationTimeout,
	}, nil)
	if err != nil {
		t.Fatalf("connect to bare stdio server: %v", err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close bare stdio session: %v", err)
		}
	})
	return session
}
