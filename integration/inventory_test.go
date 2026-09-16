//go:build integration

package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	"rhizome-mcp/internal/projectconfig"
)

func TestIntegrationProjectsList(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)
	discovered, err := projectconfig.Discover(env.repository)
	if err != nil {
		t.Fatalf("discover initialized project: %v", err)
	}

	jsonOutput := runIntegrationCommand(t, env, "--data-root", env.dataRoot, "projects", "list", "--format", "json")
	var result struct {
		Items []struct {
			ProjectID     string `json:"project_id"`
			ID            string `json:"id"`
			IssueCount    int64  `json:"issue_count"`
			SchemaVersion int    `json:"schema_version"`
			Status        string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(jsonOutput, &result); err != nil {
		t.Fatalf("decode projects list JSON: %v\n%s", err, jsonOutput)
	}
	if len(result.Items) != 1 {
		t.Fatalf("projects list item count = %d, want 1: %#v", len(result.Items), result.Items)
	}
	item := result.Items[0]
	if item.ProjectID != discovered.Identity.ProjectID || item.ID != discovered.Identity.ProjectID || item.Status != "ok" {
		t.Fatalf("projects list item = %#v, want healthy project %s", item, discovered.Identity.ProjectID)
	}
	if item.IssueCount != 0 || item.SchemaVersion == 0 {
		t.Fatalf("projects list counts = %#v, want zero issues and a schema version", item)
	}

	tableOutput := string(runIntegrationCommand(t, env, "--data-root", env.dataRoot, "projects", "list"))
	if !strings.Contains(tableOutput, "project_id\tid\tname") || !strings.Contains(tableOutput, discovered.Identity.ProjectID) {
		t.Fatalf("projects list table output = %q, want header and project %s", tableOutput, discovered.Identity.ProjectID)
	}
}
