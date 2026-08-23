//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

func TestIntegrationSearchClassifiesParserNoSuchColumnAsInvalidInput(t *testing.T) {
	env := newIntegrationEnvironment(t)
	session := env.connect(t)
	result := callIntegrationTool(t, session, "search", map[string]any{"query": "multi-project"})
	if !result.IsError {
		t.Fatalf("expected invalid search error, got %#v", result)
	}
	var output struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details []struct {
			Code string `json:"code"`
		} `json:"details"`
		Retryable bool `json:"retryable"`
	}
	decodeIntegrationResult(t, result, &output)
	if output.Code != "INVALID_ARGUMENT" || len(output.Details) == 0 || output.Details[0].Code != "INVALID_FTS_QUERY" {
		t.Fatalf("invalid search output = %#v", output)
	}
}

func TestIntegrationSearchFreshnessLiveIndexAndRebuild(t *testing.T) {
	env := newIntegrationEnvironment(t)
	session := env.connect(t)

	// Create searchable content through MCP covering every entity type:
	// issue, comment, decision, review, attempt_note with distinctive tokens.

	// Create an epic to use as parent and for epic_id filtering.
	epicCreated := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type":                  "epic",
		"title":                 "search_epic_containertoken",
		"description":           "epic_descriptiontoken",
		"status":                "ready",
		"labels":                []string{"epic-label"},
		"create_missing_labels": true,
	})
	var epic struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeIntegrationResult(t, epicCreated, &epic)
	if epicCreated.IsError || epic.ID == "" {
		t.Fatalf("create_issue epic failed: %#v", epicCreated)
	}

	// Create an issue with a distinctive title token.
	issueCreated := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type":                  "task",
		"title":                 "search_initialtoken_task",
		"description":           "issue_descriptiontoken content",
		"status":                "ready",
		"parent_issue_id":       epic.DisplayID,
		"labels":                []string{"issue-label"},
		"create_missing_labels": true,
	})
	var issue struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeIntegrationResult(t, issueCreated, &issue)
	if issueCreated.IsError || issue.ID == "" {
		t.Fatalf("create_issue task failed: %#v", issueCreated)
	}

	// Test mutation freshness: UPDATE issue title EARLY so all derived entities see the new title.
	// This tests that updates are reflected in search, and that the rebuild captures the state correctly.
	updateResult := callIntegrationTool(t, session, "update_issue", map[string]any{
		"issue_id":         issue.DisplayID,
		"expected_version": 1,
		"changes": map[string]any{
			"title":       "search_issuetoken_task",
			"description": "new_descriptiontoken",
		},
	})
	if updateResult.IsError {
		t.Fatalf("update_issue failed: %#v", updateResult)
	}

	// Create a comment with distinctive token.
	commentResult := callIntegrationTool(t, session, "add_comment", map[string]any{
		"issue_id": issue.DisplayID,
		"content":  "search_commenttoken content with distinctive marker",
	})
	if commentResult.IsError {
		t.Fatalf("add_comment failed: %#v", commentResult)
	}

	// Create a decision with distinctive tokens.
	decisionResult := callIntegrationTool(t, session, "record_decision", map[string]any{
		"issue_id": issue.DisplayID,
		"title":    "search_decisiontoken title",
		"summary":  "decision_summarytoken for the test",
		"content":  "decision_contenttoken implementation notes",
		"status":   "active",
	})
	if decisionResult.IsError {
		t.Fatalf("record_decision failed: %#v", decisionResult)
	}

	// Create a review request with distinctive token in artifact context.
	// This will have the updated issue title.
	databasePath := mustProjectDatabasePath(t, env)
	db, err := sqlite.Open(context.Background(), databasePath, sqlite.Options{})
	if err != nil {
		t.Fatalf("open project database: %v", err)
	}
	defer func() {
		if closeErr := db.Close(context.Background()); closeErr != nil {
			t.Fatalf("close project database: %v", closeErr)
		}
	}()
	reviewRepository, err := sqlite.NewReviewRepository(db)
	if err != nil {
		t.Fatalf("new review repository: %v", err)
	}
	reviewCreated, err := reviewRepository.CreateReviewRequest(context.Background(), ports.CreateReviewRequestCommand{
		Purposes:           []string{"implementation"},
		IssueID:            issue.ID,
		TargetIssueVersion: 2,
		TargetEventID:      0,
		ArtifactIDs:        []string{"search_reviewtoken_artifact"},
		OccurredAt:         time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create review request: %v", err)
	}
	var review struct {
		ID string `json:"id"`
	}
	review.ID = reviewCreated.Request.ID

	// Claim the issue to create an attempt, then save an attempt note.
	claimResult := callIntegrationTool(t, session, "claim_issue", map[string]any{
		"issue_id":      issue.DisplayID,
		"lease_seconds": 60,
	})
	var claim struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeIntegrationResult(t, claimResult, &claim)
	if claimResult.IsError || claim.Attempt.ID == "" {
		t.Fatalf("claim_issue failed: %#v", claimResult)
	}

	// Create an attempt note with distinctive token.
	noteResult := callIntegrationTool(t, session, "save_attempt_note", map[string]any{
		"attempt_id":  claim.Attempt.ID,
		"lease_token": claim.LeaseToken,
		"kind":        "checkpoint",
		"content":     "search_attempttoken_note checkpoint content",
	})
	if noteResult.IsError {
		t.Fatalf("save_attempt_note failed: %#v", noteResult)
	}

	// Finish the attempt with status change to "done" (increments version to v3).
	finishResult := callIntegrationTool(t, session, "finish_attempt", map[string]any{
		"attempt_id":          claim.Attempt.ID,
		"lease_token":         claim.LeaseToken,
		"outcome":             "completed",
		"result_summary":      "Search test completed",
		"target_issue_status": "done",
	})
	if finishResult.IsError {
		t.Fatalf("finish_attempt failed: %#v", finishResult)
	}

	// Assert search finds each entity type individually.
	searchIssue := performSearch(t, session, map[string]any{
		"query":          "search_issuetoken",
		"entity_types":   []string{"issue"},
		"snippet_length": 50,
	})
	if len(searchIssue.Results) != 1 || searchIssue.Results[0].EntityType != "issue" {
		t.Fatalf("search issue token failed: got %#v", searchIssue)
	}
	if searchIssue.Results[0].Snippet == "" {
		t.Fatalf("search issue result missing snippet")
	}

	searchComment := performSearch(t, session, map[string]any{
		"query":          "search_commenttoken",
		"entity_types":   []string{"comment"},
		"snippet_length": 50,
	})
	if len(searchComment.Results) != 1 || searchComment.Results[0].EntityType != "comment" {
		t.Fatalf("search comment token failed: got %#v", searchComment)
	}

	searchDecision := performSearch(t, session, map[string]any{
		"query":          "search_decisiontoken",
		"entity_types":   []string{"decision"},
		"snippet_length": 50,
	})
	if len(searchDecision.Results) != 1 || searchDecision.Results[0].EntityType != "decision" {
		t.Fatalf("search decision token failed: got %#v", searchDecision)
	}

	searchReview := performSearch(t, session, map[string]any{
		"query":          "search_reviewtoken",
		"entity_types":   []string{"review"},
		"snippet_length": 50,
	})
	if len(searchReview.Results) != 1 || searchReview.Results[0].EntityType != "review" {
		t.Fatalf("search review token failed: got %#v", searchReview)
	}

	searchAttemptNote := performSearch(t, session, map[string]any{
		"query":          "search_attempttoken",
		"entity_types":   []string{"attempt_note"},
		"snippet_length": 50,
	})
	if len(searchAttemptNote.Results) != 1 || searchAttemptNote.Results[0].EntityType != "attempt_note" {
		t.Fatalf("search attempt_note token failed: got %#v", searchAttemptNote)
	}

	// Test mutation freshness: verify old tokens are replaced with new (search for old tokens should fail).
	// We updated the title from "search_initialtoken_task" to "search_issuetoken_task" and
	// description to "new_descriptiontoken" earlier.
	searchOldTitleToken := performSearch(t, session, map[string]any{
		"query":          "search_initialtoken",
		"entity_types":   []string{"issue"},
		"snippet_length": 50,
	})
	if len(searchOldTitleToken.Results) != 0 {
		t.Fatalf("old title token still matches after update: got %#v", searchOldTitleToken)
	}

	// Also verify old description token no longer matches.
	searchOldDescToken := performSearch(t, session, map[string]any{
		"query":          "issue_descriptiontoken",
		"entity_types":   []string{"issue"},
		"snippet_length": 50,
	})
	if len(searchOldDescToken.Results) != 0 {
		t.Fatalf("old description token still matches after update: got %#v", searchOldDescToken)
	}

	// Test ARCHIVE: archive the issue and verify it's excluded by default.
	// After update (v2) and finish_attempt with status change (v3), archive at v3.
	archiveResult := callIntegrationTool(t, session, "archive_issue", map[string]any{
		"issue_id":         issue.DisplayID,
		"expected_version": 3,
	})
	if archiveResult.IsError {
		t.Fatalf("archive_issue failed: %#v", archiveResult)
	}

	// After archiving, searching for the issue should return no results.
	searchArchived := performSearch(t, session, map[string]any{
		"query":          "search_issuetoken",
		"entity_types":   []string{"issue"},
		"snippet_length": 50,
	})
	if len(searchArchived.Results) != 0 {
		t.Fatalf("archived issue still appears in search: got %#v", searchArchived)
	}

	// With include_archived: true, the archived issue should appear.
	searchIncludeArchived := performSearch(t, session, map[string]any{
		"query":            "search_issuetoken",
		"entity_types":     []string{"issue"},
		"include_archived": true,
		"snippet_length":   50,
	})
	if len(searchIncludeArchived.Results) != 1 {
		t.Fatalf("archived issue not found with include_archived: got %#v", searchIncludeArchived)
	}

	// Create a new unarchived issue for further testing filters.
	issue2Created := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type":                  "task",
		"title":                 "search_issue2token",
		"description":           "issue2_descriptiontoken",
		"status":                "blocked",
		"blocked_reason":        "waiting for search test dependency",
		"parent_issue_id":       epic.DisplayID,
		"labels":                []string{"issue-label", "shared-label"},
		"create_missing_labels": true,
	})
	var issue2 struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeIntegrationResult(t, issue2Created, &issue2)
	if issue2Created.IsError {
		t.Fatalf("create_issue 2 failed: %#v", issue2Created)
	}

	// Test issue_id filter: search with specific issue should limit results.
	searchByIssueID := performSearch(t, session, map[string]any{
		"query":            "search",
		"issue_id":         issue2.DisplayID,
		"include_archived": true,
		"snippet_length":   50,
	})
	// Results should only include entities related to issue2.
	// For issue type results, EntityID should match issue2's internal ID.
	// For other entity types (comments, etc.), IssueID should match.
	if len(searchByIssueID.Results) == 0 {
		t.Fatalf("issue_id filter returned no results for %s", issue2.DisplayID)
	}
	for _, result := range searchByIssueID.Results {
		if result.EntityType == "issue" {
			if result.EntityID != issue2.ID {
				t.Errorf("issue_id filter: issue entity has wrong ID: %s (expected %s)", result.EntityID, issue2.ID)
			}
		} else if result.IssueID != nil && *result.IssueID != issue2.ID {
			t.Errorf("issue_id filter: entity %s:%s has IssueID %s (expected %s)", result.EntityType, result.EntityID, *result.IssueID, issue2.ID)
		}
	}

	// Test epic_id filter (parent_issue_id): search for children of the epic.
	searchByEpicID := performSearch(t, session, map[string]any{
		"query":            "search",
		"epic_id":          epic.DisplayID,
		"include_archived": true,
		"snippet_length":   50,
	})
	// Should find results for issues under the epic (issue and issue2).
	if len(searchByEpicID.Results) == 0 {
		t.Fatalf("epic_id filter returned no results for %s", epic.DisplayID)
	}
	// Verify that at least some results have the epic as parent or are issues under the epic.
	foundEpicChild := false
	for _, result := range searchByEpicID.Results {
		// Results should be entities that are issues under the epic, or entities within issues under the epic
		if result.EntityType == "issue" {
			// Issues returned should be children of the epic
			foundEpicChild = true
		}
	}
	if !foundEpicChild {
		t.Fatalf("epic_id filter found %d results but none were issue type", len(searchByEpicID.Results))
	}

	// Test labels filter: should find issues with shared-label.
	searchByLabel := performSearch(t, session, map[string]any{
		"query":            "search",
		"labels":           []string{"shared-label"},
		"include_archived": true,
		"snippet_length":   50,
	})
	if len(searchByLabel.Results) == 0 {
		t.Fatalf("labels filter returned no results for shared-label")
	}
	// Should find at least issue2 which has shared-label.
	foundLabeled := false
	for _, result := range searchByLabel.Results {
		if result.EntityType == "issue" && result.EntityID == issue2.ID {
			foundLabeled = true
		}
	}
	if !foundLabeled {
		t.Errorf("labels filter: expected to find issue2 with shared-label, but got %d results", len(searchByLabel.Results))
	}

	// Test statuses filter: should find issues with blocked status.
	searchByStatus := performSearch(t, session, map[string]any{
		"query":            "search",
		"statuses":         []string{"blocked"},
		"include_archived": true,
		"snippet_length":   50,
	})
	if len(searchByStatus.Results) == 0 {
		t.Fatalf("statuses filter returned no results for blocked status")
	}
	// Should find issue2 which is blocked.
	foundBlocked := false
	for _, result := range searchByStatus.Results {
		if result.EntityType == "issue" && result.EntityID == issue2.ID {
			foundBlocked = true
		}
	}
	if !foundBlocked {
		t.Errorf("statuses filter: expected to find issue2 with blocked status, but got %d results", len(searchByStatus.Results))
	}

	// Test cursor pagination: request with limit 1, then use cursor for next page.
	firstPage := performSearch(t, session, map[string]any{
		"query":            "search",
		"limit":            1,
		"include_archived": true,
		"snippet_length":   50,
	})
	if len(firstPage.Results) == 0 {
		t.Fatalf("first page returned no results")
	}
	if !firstPage.HasMore {
		t.Logf("only one page of results (no pagination needed)")
	} else if firstPage.NextCursor != nil {
		secondPage := performSearch(t, session, map[string]any{
			"query":            "search",
			"cursor":           *firstPage.NextCursor,
			"limit":            1,
			"include_archived": true,
			"snippet_length":   50,
		})
		if len(secondPage.Results) > 0 {
			// Verify no duplicate results between pages.
			firstKey := string(firstPage.Results[0].EntityType) + ":" + firstPage.Results[0].EntityID
			secondKey := string(secondPage.Results[0].EntityType) + ":" + secondPage.Results[0].EntityID
			if firstKey == secondKey {
				t.Fatalf("cursor pagination returned duplicate result: %s", firstKey)
			}
		}
	}

	// Snapshot the full result set for comparison after rebuild.
	// Close the session before running CLI commands.
	if err := session.Close(); err != nil {
		t.Errorf("close MCP session: %v", err)
	}

	// Get the full snapshot before rebuild.
	session = env.connect(t)
	fullSnapshot := performSearch(t, session, map[string]any{
		"query":            "search",
		"include_archived": true,
		"limit":            100,
		"snippet_length":   50,
	})
	snapshotJSON := serializeSearchResults(t, fullSnapshot)

	// Stop the server (close session) and run rebuild-search-index CLI command.
	if err := session.Close(); err != nil {
		t.Errorf("close MCP session before rebuild: %v", err)
	}

	runIntegrationCommand(t, env, "--data-root", env.dataRoot, "maintenance", "rebuild-search-index")

	// Restart the server and verify the result set is identical.
	session = env.connect(t)
	defer func() {
		if err := session.Close(); err != nil {
			t.Errorf("close final MCP session: %v", err)
		}
	}()

	rebuildSnapshot := performSearch(t, session, map[string]any{
		"query":            "search",
		"include_archived": true,
		"limit":            100,
		"snippet_length":   50,
	})

	// Assert the snapshotted result sets are IDENTICAL.
	// Compare entity types and IDs to verify completeness (ignore scores which may vary).
	if len(fullSnapshot.Results) != len(rebuildSnapshot.Results) {
		t.Fatalf("rebuild-search-index produced different number of results: before=%d, after=%d",
			len(fullSnapshot.Results), len(rebuildSnapshot.Results))
	}

	for i := range fullSnapshot.Results {
		before := fullSnapshot.Results[i]
		after := rebuildSnapshot.Results[i]
		if before.EntityType != after.EntityType || before.EntityID != after.EntityID {
			t.Fatalf("rebuild-search-index produced different result at index %d:\nbefore: %s:%s\nafter: %s:%s",
				i, before.EntityType, before.EntityID, after.EntityType, after.EntityID)
		}
		if before.Title != after.Title || before.Snippet != after.Snippet {
			t.Fatalf("rebuild-search-index produced different content at index %d:\nbefore title=%q snippet=%q\nafter title=%q snippet=%q",
				i, before.Title, before.Snippet, after.Title, after.Snippet)
		}
	}

	// Verify byte-level snapshot is identical (strict per acceptance criteria).
	// Any difference indicates that live-write index and rebuild path produce different results,
	// which is exactly the bug class this test is designed to catch.
	rebuildJSON := serializeSearchResults(t, rebuildSnapshot)
	if !bytes.Equal(snapshotJSON, rebuildJSON) {
		t.Fatalf("rebuild-search-index produced byte-different results (indicates live-write and rebuild paths diverge).\nBefore:\n%s\n\nAfter:\n%s",
			snapshotJSON, rebuildJSON)
	}
}

// performSearch calls the search MCP tool and returns the structured response.
func performSearch(t *testing.T, session *mcp.ClientSession, arguments map[string]any) domain.SearchPage {
	t.Helper()
	result := callIntegrationTool(t, session, "search", arguments)
	if result.IsError {
		t.Fatalf("search tool returned error: %#v", result)
	}
	var response struct {
		Results []struct {
			EntityType string  `json:"entity_type"`
			EntityID   string  `json:"entity_id"`
			IssueID    *string `json:"issue_id"`
			Title      string  `json:"title"`
			Snippet    string  `json:"snippet"`
			Score      float64 `json:"score"`
		} `json:"results"`
		NextCursor *string `json:"next_cursor"`
		HasMore    bool    `json:"has_more"`
	}
	decodeIntegrationResult(t, result, &response)
	results := make([]domain.SearchResult, len(response.Results))
	for i, r := range response.Results {
		results[i] = domain.SearchResult{
			EntityType: domain.SearchEntityType(r.EntityType),
			EntityID:   r.EntityID,
			IssueID:    r.IssueID,
			Title:      r.Title,
			Snippet:    r.Snippet,
			Score:      r.Score,
		}
	}
	return domain.SearchPage{
		Results:    results,
		NextCursor: response.NextCursor,
		HasMore:    response.HasMore,
	}
}

// serializeSearchResults returns the JSON representation of a SearchPage for byte-level comparison.
func serializeSearchResults(t *testing.T, page domain.SearchPage) []byte {
	t.Helper()
	data, err := json.MarshalIndent(page, "", "  ")
	if err != nil {
		t.Fatalf("marshal search results: %v", err)
	}
	return data
}
