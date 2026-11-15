//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

func TestIntegrationProjectRoutingOpenProjectByMarkerID(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	hostRepo := filepath.Join(tempDir, "host-repo")
	remoteCopyDir := filepath.Join(tempDir, "remote-copy")
	nonExistentHostPath := filepath.Join(tempDir, "non-existent-host-path")
	dataRoot := filepath.Join(tempDir, "data")
	outsideDir := filepath.Join(tempDir, "outside")
	for _, dir := range []string{hostRepo, remoteCopyDir, outsideDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}

	runIntegrationCommand(t, integrationEnvironment{repository: hostRepo, dataRoot: dataRoot}, "--data-root", dataRoot, "init")

	markerBytes, err := os.ReadFile(filepath.Join(hostRepo, ".agent-tracker.json"))
	if err != nil {
		t.Fatalf("read host .agent-tracker.json: %v", err)
	}
	remoteMarkerPath := filepath.Join(remoteCopyDir, ".agent-tracker.json")
	if err := os.WriteFile(remoteMarkerPath, markerBytes, 0o644); err != nil {
		t.Fatalf("copy .agent-tracker.json: %v", err)
	}
	var marker struct {
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(markerBytes, &marker); err != nil {
		t.Fatalf("decode .agent-tracker.json: %v", err)
	}
	if marker.ProjectID == "" {
		t.Fatal("marker project_id is empty")
	}

	session := connectBareServeFromDirectory(t, dataRoot, outsideDir)

	missingPath := callIntegrationTool(t, session, "open_project", map[string]any{
		"project_root": nonExistentHostPath,
	})
	if !missingPath.IsError {
		t.Fatalf("open_project(non-existent host path) succeeded: %#v", missingPath)
	}

	openByID := callIntegrationTool(t, session, "open_project", map[string]any{
		"project_id": marker.ProjectID,
	})
	if openByID.IsError {
		t.Fatalf("open_project(project_id) failed: %#v", openByID)
	}
	var rawByID map[string]any
	decodeIntegrationResult(t, openByID, &rawByID)

	openByRoot := callIntegrationTool(t, session, "open_project", map[string]any{
		"project_root": hostRepo,
	})
	if openByRoot.IsError {
		t.Fatalf("open_project(project_root) failed: %#v", openByRoot)
	}
	var rawByRoot map[string]any
	decodeIntegrationResult(t, openByRoot, &rawByRoot)
	if !reflect.DeepEqual(rawByID, rawByRoot) {
		t.Fatalf("open_project(project_id) = %#v, want identical to open_project(project_root) = %#v", rawByID, rawByRoot)
	}

	var opened struct {
		ProjectRef string `json:"project_ref"`
	}
	decodeIntegrationResult(t, openByID, &opened)
	if opened.ProjectRef != marker.ProjectID {
		t.Fatalf("opened.ProjectRef = %q, want %q", opened.ProjectRef, marker.ProjectID)
	}

	created := callIntegrationTool(t, session, "create_issue", map[string]any{
		"project_ref": opened.ProjectRef,
		"type":        "task",
		"title":       "marker resolved task",
		"description": "Created after resolving project by marker project_id.",
		"status":      "ready",
	})
	if created.IsError {
		t.Fatalf("create_issue after open_project(project_id) failed: %#v", created)
	}

	listed := callIntegrationTool(t, session, "list_issues", map[string]any{
		"project_ref": opened.ProjectRef,
		"limit":       10,
	})
	if listed.IsError {
		t.Fatalf("list_issues after open_project(project_id) failed: %#v", listed)
	}
	var page struct {
		Items []struct {
			Title string `json:"title"`
		} `json:"items"`
	}
	decodeIntegrationResult(t, listed, &page)
	if len(page.Items) != 1 || page.Items[0].Title != "marker resolved task" {
		t.Fatalf("list_issues items = %#v, want one marker resolved task", page.Items)
	}

	beforeSnapshot := snapshotIntegrationTree(t, dataRoot)
	unknownID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	if marker.ProjectID == unknownID {
		unknownID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	}
	unknown := callIntegrationTool(t, session, "open_project", map[string]any{
		"project_id": unknownID,
	})
	if !unknown.IsError {
		t.Fatalf("open_project(unknown project_id) succeeded: %#v", unknown)
	}
	var errPayload struct {
		Code string `json:"code"`
	}
	decodeIntegrationResult(t, unknown, &errPayload)
	if errPayload.Code != "PROJECT_NOT_FOUND" {
		t.Fatalf("unknown project_id error code = %q, want PROJECT_NOT_FOUND", errPayload.Code)
	}
	afterSnapshot := snapshotIntegrationTree(t, dataRoot)
	if !reflect.DeepEqual(beforeSnapshot, afterSnapshot) {
		t.Fatalf("unknown project_id altered dataRoot tree:\nbefore=%v\nafter=%v", beforeSnapshot, afterSnapshot)
	}
}

func snapshotIntegrationTree(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return paths
}
