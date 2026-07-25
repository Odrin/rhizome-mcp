//go:build integration

package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestIntegrationGetChangesIncrementalSync verifies that get_changes correctly
// tracks and returns delta events when called repeatedly with pagination.
// It validates: no duplicates at page boundaries, monotonic event ordering,
// equivalence between paged and unpaged results, issue_id/event_types filtering,
// and the idle-consumer steady state.
func TestIntegrationGetChangesIncrementalSync(t *testing.T) {
	env := newIntegrationEnvironment(t)
	session := env.connect(t)

	// 1. Record the baseline event position from get_project on a fresh environment.
	baselineProject := callIntegrationTool(t, session, "get_project", map[string]any{})
	if baselineProject.IsError {
		t.Fatalf("get_project failed: %#v", baselineProject)
	}
	var baselineState struct {
		LatestEventID int64 `json:"latest_event_id"`
	}
	decodeIntegrationResult(t, baselineProject, &baselineState)
	baselineSinceEventID := baselineState.LatestEventID

	// 2. Drive a deterministic, ordered sequence of mutations.
	// The sequence will produce roughly 15-20 events.

	// Create first issue.
	issue1Result := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type":                  "task",
		"title":                 "First test issue",
		"description":           "Issue created for get_changes sync test",
		"status":                "ready",
		"labels":                []string{"test-sync"},
		"create_missing_labels": true,
	})
	if issue1Result.IsError {
		t.Fatalf("create_issue 1 failed: %#v", issue1Result)
	}
	var issue1 struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeIntegrationResult(t, issue1Result, &issue1)

	// Create second issue.
	issue2Result := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type":                  "bug",
		"title":                 "Second test issue",
		"description":           "Another issue for sync test",
		"status":                "ready",
		"labels":                []string{"test-sync"},
		"create_missing_labels": true,
	})
	if issue2Result.IsError {
		t.Fatalf("create_issue 2 failed: %#v", issue2Result)
	}
	var issue2 struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeIntegrationResult(t, issue2Result, &issue2)

	// Add comment to first issue.
	commentResult := callIntegrationTool(t, session, "add_comment", map[string]any{
		"issue_id": issue1.DisplayID,
		"content":  "This is a test comment for sync testing",
	})
	if commentResult.IsError {
		t.Fatalf("add_comment failed: %#v", commentResult)
	}

	// Record a decision on first issue.
	decisionResult := callIntegrationTool(t, session, "record_decision", map[string]any{
		"issue_id": issue1.DisplayID,
		"title":    "Test decision",
		"summary":  "Recording a decision for sync test",
		"content":  "This decision documents the approach taken for this task.",
	})
	if decisionResult.IsError {
		t.Fatalf("record_decision failed: %#v", decisionResult)
	}

	// Claim first issue to start an attempt.
	claimResult := callIntegrationTool(t, session, "claim_issue", map[string]any{
		"issue_id":      issue1.DisplayID,
		"lease_seconds": 60,
	})
	if claimResult.IsError {
		t.Fatalf("claim_issue failed: %#v", claimResult)
	}
	var claimData struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeIntegrationResult(t, claimResult, &claimData)

	// Save attempt note.
	noteResult := callIntegrationTool(t, session, "save_attempt_note", map[string]any{
		"attempt_id":  claimData.Attempt.ID,
		"lease_token": claimData.LeaseToken,
		"kind":        "progress",
		"content":     "Progress on the task",
	})
	if noteResult.IsError {
		t.Fatalf("save_attempt_note failed: %#v", noteResult)
	}

	// Finish the attempt.
	finishResult := callIntegrationTool(t, session, "finish_attempt", map[string]any{
		"attempt_id":           claimData.Attempt.ID,
		"lease_token":          claimData.LeaseToken,
		"outcome":              "completed",
		"result_summary":       "Task completed successfully",
		"target_issue_status":  "done",
	})
	if finishResult.IsError {
		t.Fatalf("finish_attempt failed: %#v", finishResult)
	}

	// 3. Poll get_changes from the baseline with small limit (3) to force pagination.
	const smallLimit = 3
	allEventsPaged := drainGetChanges(t, session, baselineSinceEventID, smallLimit, "", nil)
	if len(allEventsPaged) == 0 {
		t.Fatalf("drained events from paged polling: got 0, want > 0")
	}

	// 4. Assert the drained event set:
	// - contains every expected mutation EXACTLY ONCE
	// - has no gaps in event ordering
	// - is monotonically ordered by event id

	eventIDs := make(map[int64]int)
	for _, evt := range allEventsPaged {
		eventIDs[evt.ID]++
	}
	for eventID, count := range eventIDs {
		if count != 1 {
			t.Errorf("event %d appears %d times, want 1", eventID, count)
		}
	}

	for i := 1; i < len(allEventsPaged); i++ {
		if allEventsPaged[i].ID <= allEventsPaged[i-1].ID {
			t.Errorf("events not monotonically ordered: event %d <= event %d",
				allEventsPaged[i].ID, allEventsPaged[i-1].ID)
		}
		// Check for gaps: next event ID should be exactly 1 more than previous
		if allEventsPaged[i].ID != allEventsPaged[i-1].ID+1 {
			t.Errorf("gap in event ordering at events %d and %d",
				allEventsPaged[i-1].ID, allEventsPaged[i].ID)
		}
	}

	// 5. Assert latest_event_id is monotonically non-decreasing across polls
	// and matches the final position reported by get_project.
	finalProject := callIntegrationTool(t, session, "get_project", map[string]any{})
	if finalProject.IsError {
		t.Fatalf("get_project (final) failed: %#v", finalProject)
	}
	var finalState struct {
		LatestEventID int64 `json:"latest_event_id"`
	}
	decodeIntegrationResult(t, finalProject, &finalState)

	if len(allEventsPaged) > 0 {
		lastEvent := allEventsPaged[len(allEventsPaged)-1]
		if lastEvent.ID != finalState.LatestEventID {
			t.Errorf("last drained event ID %d != final project latest_event_id %d",
				lastEvent.ID, finalState.LatestEventID)
		}
	}

	// 6. Assert equivalence: draining with small limit yields the same event
	// sequence as a single call with a limit large enough to return everything.
	const largeLimit = 200
	allEventsUnpaged := drainGetChanges(t, session, baselineSinceEventID, largeLimit, "", nil)

	if len(allEventsPaged) != len(allEventsUnpaged) {
		t.Errorf("paged (%d events) vs unpaged (%d events) mismatch",
			len(allEventsPaged), len(allEventsUnpaged))
	}

	for i := range allEventsPaged {
		if i >= len(allEventsUnpaged) {
			break
		}
		if allEventsPaged[i].ID != allEventsUnpaged[i].ID {
			t.Errorf("paged event %d != unpaged event %d at index %d",
				allEventsPaged[i].ID, allEventsUnpaged[i].ID, i)
		}
		if allEventsPaged[i].EventType != allEventsUnpaged[i].EventType {
			t.Errorf("paged event type %s != unpaged event type %s at ID %d",
				allEventsPaged[i].EventType, allEventsUnpaged[i].EventType, allEventsPaged[i].ID)
		}
	}

	// 7. Assert filtering: issue_id scoped to one issue returns only that
	// issue's events, and event_types filtering returns only the requested types.

	// Count events for issue1
	issue1Events := drainGetChanges(t, session, baselineSinceEventID, largeLimit, issue1.ID, nil)
	for _, evt := range issue1Events {
		if evt.IssueID == nil || *evt.IssueID != issue1.ID {
			t.Errorf("issue_id filter returned event for wrong issue: got %v, want %s",
				evt.IssueID, issue1.ID)
		}
	}

	// Filter by event_types
	eventTypes := []string{"issue_created", "issue_updated", "comment_added"}
	filteredEvents := drainGetChanges(t, session, baselineSinceEventID, largeLimit, "", eventTypes)
	for _, evt := range filteredEvents {
		found := false
		for _, wantType := range eventTypes {
			if evt.EventType == wantType {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("event_types filter returned unexpected event type: %s", evt.EventType)
		}
	}

	// 8. Assert a poll from latest_event_id returns an empty event set
	// with has_more false — the idle-consumer steady state.
	idleResult := callIntegrationTool(t, session, "get_changes", map[string]any{
		"since_event_id": finalState.LatestEventID,
		"limit":          largeLimit,
	})
	if idleResult.IsError {
		t.Fatalf("get_changes (idle) failed: %#v", idleResult)
	}
	var idleState struct {
		Events      []map[string]any `json:"events"`
		HasMore     bool             `json:"has_more"`
		LatestEventID int64            `json:"latest_event_id"`
	}
	decodeIntegrationResult(t, idleResult, &idleState)
	if len(idleState.Events) != 0 {
		t.Errorf("idle poll returned %d events, want 0", len(idleState.Events))
	}
	if idleState.HasMore {
		t.Errorf("idle poll has_more = true, want false")
	}
	if idleState.LatestEventID != finalState.LatestEventID {
		t.Errorf("idle poll latest_event_id %d != final project latest_event_id %d",
			idleState.LatestEventID, finalState.LatestEventID)
	}
}

// issueEventDTO is a local struct for unmarshalling event results.
type issueEventDTO struct {
	ID        int64           `json:"id"`
	IssueID   *string         `json:"issue_id"`
	EventType string          `json:"event_type"`
	SessionID *string         `json:"session_id"`
	AttemptID *string         `json:"attempt_id"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

// drainGetChanges repeatedly calls get_changes, following next_event_id and
// has_more flags, until the entire event stream is consumed. Returns all
// collected events in order.
func drainGetChanges(t *testing.T, session interface{}, sinceEventID int64,
	limit int, issueID string, eventTypes []string) []issueEventDTO {
	t.Helper()

	var allEvents []issueEventDTO
	currentSinceEventID := sinceEventID

	for {
		args := map[string]any{
			"since_event_id": currentSinceEventID,
			"limit":          limit,
		}
		if issueID != "" {
			args["issue_id"] = issueID
		}
		if len(eventTypes) > 0 {
			args["event_types"] = eventTypes
		}

		result := callIntegrationTool(t, session.(*mcp.ClientSession), "get_changes", args)
		if result.IsError {
			t.Fatalf("get_changes failed: %#v", result)
		}

		var page struct {
			Events        []issueEventDTO `json:"events"`
			LatestEventID int64           `json:"latest_event_id"`
			HasMore       bool            `json:"has_more"`
			NextEventID   int64           `json:"next_event_id"`
		}
		decodeIntegrationResult(t, result, &page)

		allEvents = append(allEvents, page.Events...)

		if !page.HasMore {
			break
		}

		currentSinceEventID = page.NextEventID
	}

	return allEvents
}
