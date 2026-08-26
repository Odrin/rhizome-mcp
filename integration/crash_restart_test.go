//go:build integration

package integration_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestIntegrationAttemptSurvivesCrashAndRestart is the ISSUE-104 proof test:
// an active attempt with a saved checkpoint must survive the server process
// being killed without a graceful shutdown. Every other integration test
// terminates the server gracefully, so sqlite.DB.Close's passive WAL
// checkpoint (internal/adapters/sqlite/sqlite.go:279) always runs and WAL
// recovery on restart is never exercised. This test kills the server with
// killIntegrationHTTPServer instead, then launches a fresh server process
// against the same data root and asserts the checkpoint, the active lease,
// and the pre-crash lease token all survived.
func TestIntegrationAttemptSurvivesCrashAndRestart(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)

	server := launchIntegrationHTTPServer(t, env, "127.0.0.1:0")
	t.Cleanup(func() { killIntegrationHTTPServer(t, server) })

	endpoint := "http://" + server.waitForEndpoint(t) + "/mcp"
	_, sessionID, err := communicateThroughHTTP(t, endpoint, "crash-restart-before")
	if err != nil {
		t.Fatalf("initialize session before crash: %v\nstderr:\n%s", err, server.output.String())
	}

	httpClient := &http.Client{Timeout: integrationTimeout}

	const wantTitle = "crash restart proof issue"
	createResult, err := postJSONRPC(httpClient, endpoint, sessionID, 10, "tools/call", map[string]any{
		"name": "create_issue",
		"arguments": map[string]any{
			"type":   "task",
			"title":  wantTitle,
			"status": "ready",
		},
	})
	if err != nil {
		t.Fatalf("create_issue: %v", err)
	}
	var created struct {
		StructuredContent struct {
			ID        string `json:"id"`
			DisplayID string `json:"display_id"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(createResult.result, &created); err != nil {
		t.Fatalf("decode create_issue result: %v", err)
	}
	if created.StructuredContent.ID == "" || created.StructuredContent.DisplayID == "" {
		t.Fatalf("create_issue returned no id/display_id: %s", createResult.result)
	}

	claimResult, err := postJSONRPC(httpClient, endpoint, sessionID, 11, "tools/call", map[string]any{
		"name": "claim_issue",
		"arguments": map[string]any{
			"issue_id":      created.StructuredContent.DisplayID,
			"lease_seconds": 60,
		},
	})
	if err != nil {
		t.Fatalf("claim_issue: %v", err)
	}
	var claimed struct {
		StructuredContent struct {
			Attempt struct {
				ID string `json:"id"`
			} `json:"attempt"`
			LeaseToken string `json:"lease_token"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(claimResult.result, &claimed); err != nil {
		t.Fatalf("decode claim_issue result: %v", err)
	}
	attemptID := claimed.StructuredContent.Attempt.ID
	leaseToken := claimed.StructuredContent.LeaseToken
	if attemptID == "" || leaseToken == "" {
		t.Fatalf("claim_issue returned no attempt id/lease token: %s", claimResult.result)
	}

	const checkpointContent = "ISSUE-104 crash-restart checkpoint: repository layer implemented moments before the process was killed."
	noteResult, err := postJSONRPC(httpClient, endpoint, sessionID, 12, "tools/call", map[string]any{
		"name": "save_attempt_note",
		"arguments": map[string]any{
			"attempt_id":  attemptID,
			"lease_token": leaseToken,
			"kind":        "checkpoint",
			"content":     checkpointContent,
			"next_steps":  []string{"Resume from this checkpoint after restart"},
			"important":   true,
			"artifacts": []map[string]any{
				{"type": "file", "uri": "notes/crash-restart-checkpoint.md", "title": "Checkpoint note"},
			},
		},
	})
	if err != nil {
		t.Fatalf("save_attempt_note before crash: %v", err)
	}
	var savedNote struct {
		StructuredContent struct {
			AttemptNote struct {
				ID      string `json:"id"`
				Content string `json:"content"`
			} `json:"attempt_note"`
			Artifacts []struct {
				ID  string `json:"id"`
				URI string `json:"uri"`
			} `json:"artifacts"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(noteResult.result, &savedNote); err != nil {
		t.Fatalf("decode save_attempt_note result: %v", err)
	}
	if savedNote.StructuredContent.AttemptNote.ID == "" || len(savedNote.StructuredContent.Artifacts) != 1 {
		t.Fatalf("save_attempt_note before crash missing note id or artifact: %s", noteResult.result)
	}

	// Kill the server before it can run any graceful shutdown handler,
	// including sqlite.DB.Close's passive WAL checkpoint, then bring up a
	// fresh server process attached to the same data root.
	killIntegrationHTTPServer(t, server)

	restarted := launchIntegrationHTTPServer(t, env.attach(), "127.0.0.1:0")
	t.Cleanup(func() { stopIntegrationHTTPServer(t, restarted) })
	restartedEndpoint := "http://" + restarted.waitForEndpoint(t) + "/mcp"
	_, restartedSessionID, err := communicateThroughHTTP(t, restartedEndpoint, "crash-restart-after")
	if err != nil {
		t.Fatalf("initialize session after restart: %v\nstderr:\n%s", err, restarted.output.String())
	}

	// The checkpoint content written just before the crash must be readable
	// through the restarted server via get_issue_activity.
	activityResult, err := postJSONRPC(httpClient, restartedEndpoint, restartedSessionID, 20, "tools/call", map[string]any{
		"name": "get_issue_activity",
		"arguments": map[string]any{
			"issue_id": created.StructuredContent.DisplayID,
			"types":    []string{"attempt_notes"},
		},
	})
	if err != nil {
		t.Fatalf("get_issue_activity after restart: %v", err)
	}
	var activity struct {
		StructuredContent struct {
			Items []struct {
				EntityType  string `json:"entity_type"`
				AttemptNote *struct {
					Content string `json:"content"`
				} `json:"attempt_note"`
			} `json:"items"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(activityResult.result, &activity); err != nil {
		t.Fatalf("decode get_issue_activity result: %v", err)
	}
	foundCheckpoint := false
	for _, item := range activity.StructuredContent.Items {
		if item.AttemptNote != nil && item.AttemptNote.Content == checkpointContent {
			foundCheckpoint = true
			break
		}
	}
	if !foundCheckpoint {
		t.Fatalf("checkpoint note did not survive unclean crash and restart; get_issue_activity items = %s", activityResult.result)
	}

	// The attempt must still be active: effective status is derived from an
	// active, unexpired lease and is never written directly, so checking it
	// after restart proves the lease itself survived the crash.
	workContextResult, err := postJSONRPC(httpClient, restartedEndpoint, restartedSessionID, 21, "tools/call", map[string]any{
		"name":      "get_work_context",
		"arguments": map[string]any{"issue_id": created.StructuredContent.DisplayID},
	})
	if err != nil {
		t.Fatalf("get_work_context after restart: %v", err)
	}
	var workContext struct {
		StructuredContent struct {
			Issue struct {
				EffectiveStatus string `json:"effective_status"`
			} `json:"issue"`
			Checkpoint *struct {
				Content string `json:"content"`
			} `json:"checkpoint"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(workContextResult.result, &workContext); err != nil {
		t.Fatalf("decode get_work_context result: %v", err)
	}
	if workContext.StructuredContent.Issue.EffectiveStatus != "in_progress" {
		t.Fatalf("issue effective_status after restart = %q, want %q", workContext.StructuredContent.Issue.EffectiveStatus, "in_progress")
	}
	if workContext.StructuredContent.Checkpoint == nil || workContext.StructuredContent.Checkpoint.Content != checkpointContent {
		t.Fatalf("get_work_context checkpoint after restart = %#v, want content %q", workContext.StructuredContent.Checkpoint, checkpointContent)
	}

	// The issue must not be spuriously claimable: a fresh claim against the
	// still-active lease must be rejected.
	reclaimResult, err := postJSONRPC(httpClient, restartedEndpoint, restartedSessionID, 22, "tools/call", map[string]any{
		"name": "claim_issue",
		"arguments": map[string]any{
			"issue_id":      created.StructuredContent.DisplayID,
			"lease_seconds": 60,
		},
	})
	if err != nil {
		t.Fatalf("reclaim attempt after restart: %v", err)
	}
	var reclaimEnvelope struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(reclaimResult.result, &reclaimEnvelope); err != nil {
		t.Fatalf("decode reclaim attempt result: %v", err)
	}
	if !reclaimEnvelope.IsError {
		t.Fatalf("claim_issue on an already-leased issue after restart unexpectedly succeeded: %s", reclaimResult.result)
	}

	// The pre-crash lease token must still authorize work: a further
	// checkpoint and then finish_attempt with that exact token must succeed.
	secondNoteResult, err := postJSONRPC(httpClient, restartedEndpoint, restartedSessionID, 23, "tools/call", map[string]any{
		"name": "save_attempt_note",
		"arguments": map[string]any{
			"attempt_id":  attemptID,
			"lease_token": leaseToken,
			"kind":        "progress",
			"content":     "Resumed after an unclean crash and restart using the pre-crash lease token.",
		},
	})
	if err != nil {
		t.Fatalf("save_attempt_note after restart with pre-crash lease token: %v", err)
	}
	var secondNoteEnvelope struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(secondNoteResult.result, &secondNoteEnvelope); err != nil {
		t.Fatalf("decode save_attempt_note (post-restart) result: %v", err)
	}
	if secondNoteEnvelope.IsError {
		t.Fatalf("save_attempt_note with pre-crash lease token failed after restart: %s", secondNoteResult.result)
	}

	finishResult, err := postJSONRPC(httpClient, restartedEndpoint, restartedSessionID, 24, "tools/call", map[string]any{
		"name": "finish_attempt",
		"arguments": map[string]any{
			"attempt_id":          attemptID,
			"lease_token":         leaseToken,
			"outcome":             "completed",
			"result_summary":      "Resumed and completed after an unclean crash and restart.",
			"target_issue_status": "done",
		},
	})
	if err != nil {
		t.Fatalf("finish_attempt with pre-crash lease token after restart: %v", err)
	}
	var finished struct {
		StructuredContent struct {
			Attempt struct {
				Status string `json:"status"`
			} `json:"attempt"`
			Issue struct {
				Status string `json:"status"`
			} `json:"issue"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(finishResult.result, &finished); err != nil {
		t.Fatalf("decode finish_attempt result: %v", err)
	}
	if finished.StructuredContent.Attempt.Status != "completed" || finished.StructuredContent.Issue.Status != "done" {
		t.Fatalf("finish_attempt after restart result = %s, want attempt completed and issue done", finishResult.result)
	}
}
