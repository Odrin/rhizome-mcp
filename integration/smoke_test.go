//go:build integration

package integration_test

import (
	"context"
	"testing"
)

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
