//go:build integration

package integration_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestIntegrationBoardCommand builds a mixed-status scenario (open, ready,
// blocked-with-reason, a claimed/leased attempt, and an open review request)
// through the MCP server, then verifies `rhizome-mcp board` in all three
// modes: --format table, --format json, and --output (self-contained HTML).
func TestIntegrationBoardCommand(t *testing.T) {
	env := newIntegrationEnvironment(t)
	session := env.connect(t)

	openIssue := mustCreateBoardIssue(t, session, map[string]any{
		"type": "task", "title": "Board open task", "status": "open",
	})
	readyIssue := mustCreateBoardIssue(t, session, map[string]any{
		"type": "task", "title": "Board ready task (to be leased)", "status": "ready",
	})
	blockedIssue := mustCreateBoardIssue(t, session, map[string]any{
		"type": "task", "title": "Board blocked task", "status": "blocked", "blocked_reason": "waiting on an external dependency",
	})
	reviewIssue := mustCreateBoardIssue(t, session, map[string]any{
		"type": "bug", "title": "Board review bug", "status": "review",
	})

	claimed := callIntegrationTool(t, session, "claim_issue", map[string]any{
		"issue_id":      readyIssue.DisplayID,
		"lease_seconds": 600,
	})
	var claim struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseExpiresAt time.Time `json:"lease_expires_at"`
	}
	decodeIntegrationResult(t, claimed, &claim)
	if claimed.IsError || claim.Attempt.ID == "" {
		t.Fatalf("claim_issue result = %#v, decoded = %#v", claimed, claim)
	}

	reviewRequested := callIntegrationTool(t, session, "create_review_request", map[string]any{
		"issue_id":             reviewIssue.DisplayID,
		"target_issue_version": 1,
		"target_event_id":      0,
		"artifact_ids":         []string{},
	})
	var reviewRequest struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	decodeIntegrationResult(t, reviewRequested, &reviewRequest)
	if reviewRequested.IsError || reviewRequest.ID == "" || reviewRequest.Status != "open" {
		t.Fatalf("create_review_request result = %#v, decoded = %#v", reviewRequested, reviewRequest)
	}

	// --format table (also exercised as the default with no --format flag).
	tableOutput := runIntegrationCommand(t, env, "--data-root", env.dataRoot, "board")
	tableText := string(tableOutput)
	if len(strings.TrimSpace(tableText)) == 0 {
		t.Fatalf("board table output is empty")
	}
	for _, want := range []string{"blocked", readyIssue.DisplayID, blockedIssue.DisplayID, claim.Attempt.ID, reviewRequest.ID} {
		if !strings.Contains(tableText, want) {
			t.Fatalf("board table output missing %q; output:\n%s", want, tableText)
		}
	}

	explicitTableOutput := runIntegrationCommand(t, env, "--data-root", env.dataRoot, "board", "--format", "table")
	if len(strings.TrimSpace(string(explicitTableOutput))) == 0 {
		t.Fatalf("board --format table output is empty")
	}

	// --format json.
	jsonOutput := runIntegrationCommand(t, env, "--data-root", env.dataRoot, "board", "--format", "json")
	var board struct {
		StatusCounts []struct {
			EffectiveStatus string `json:"effective_status"`
			Count           int64  `json:"count"`
		} `json:"status_counts"`
		ActiveAttempts []struct {
			AttemptID      string `json:"attempt_id"`
			IssueDisplayID string `json:"issue_display_id"`
		} `json:"active_attempts"`
		BlockedIssues []struct {
			DisplayID     string `json:"display_id"`
			BlockedReason string `json:"blocked_reason"`
		} `json:"blocked_issues"`
		ReviewRequests []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"review_requests"`
		PlanningGraph struct {
			Nodes []struct {
				DisplayID string `json:"display_id"`
			} `json:"nodes"`
		} `json:"planning_graph"`
	}
	if err := json.Unmarshal(jsonOutput, &board); err != nil {
		t.Fatalf("decode board --format json output: %v\noutput:\n%s", err, jsonOutput)
	}

	foundOpenStatus, foundBlockedStatus := false, false
	for _, count := range board.StatusCounts {
		switch count.EffectiveStatus {
		case "open":
			foundOpenStatus = count.Count >= 1
		case "blocked":
			foundBlockedStatus = count.Count >= 1
		}
	}
	if !foundOpenStatus || !foundBlockedStatus {
		t.Fatalf("board status_counts missing expected open/blocked entries: %#v", board.StatusCounts)
	}

	attemptFound := false
	for _, attempt := range board.ActiveAttempts {
		if attempt.AttemptID == claim.Attempt.ID && attempt.IssueDisplayID == readyIssue.DisplayID {
			attemptFound = true
		}
	}
	if !attemptFound {
		t.Fatalf("board active_attempts missing claimed attempt %s for issue %s: %#v", claim.Attempt.ID, readyIssue.DisplayID, board.ActiveAttempts)
	}

	blockedFound := false
	for _, issue := range board.BlockedIssues {
		if issue.DisplayID == blockedIssue.DisplayID && issue.BlockedReason == "waiting on an external dependency" {
			blockedFound = true
		}
	}
	if !blockedFound {
		t.Fatalf("board blocked_issues missing %s with its reason: %#v", blockedIssue.DisplayID, board.BlockedIssues)
	}

	reviewFound := false
	for _, request := range board.ReviewRequests {
		if request.ID == reviewRequest.ID && request.Status == "open" {
			reviewFound = true
		}
	}
	if !reviewFound {
		t.Fatalf("board review_requests missing %s: %#v", reviewRequest.ID, board.ReviewRequests)
	}

	graphHasReadyIssue := false
	for _, node := range board.PlanningGraph.Nodes {
		if node.DisplayID == readyIssue.DisplayID {
			graphHasReadyIssue = true
		}
	}
	if !graphHasReadyIssue {
		t.Fatalf("board planning_graph nodes missing %s: %#v", readyIssue.DisplayID, board.PlanningGraph.Nodes)
	}

	// --output writes a fully self-contained HTML board.
	htmlPath := filepath.Join(t.TempDir(), "board.html")
	runIntegrationCommand(t, env, "--data-root", env.dataRoot, "board", "--output", htmlPath)
	htmlBytes, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("read board HTML output: %v", err)
	}
	htmlText := string(htmlBytes)
	if len(strings.TrimSpace(htmlText)) == 0 {
		t.Fatalf("board HTML output is empty")
	}
	if strings.Contains(htmlText, "<script src=") {
		t.Fatalf("board HTML unexpectedly references an external script:\n%s", htmlText)
	}
	if strings.Contains(htmlText, `<link rel="stylesheet" href=`) {
		t.Fatalf("board HTML unexpectedly references an external stylesheet:\n%s", htmlText)
	}
	if strings.Contains(htmlText, "http://") || strings.Contains(htmlText, "https://") {
		t.Fatalf("board HTML unexpectedly contains a network URL:\n%s", htmlText)
	}
	if !strings.Contains(htmlText, "<svg") {
		t.Fatalf("board HTML missing an inline <svg> planning graph:\n%s", htmlText)
	}
	for _, want := range []string{openIssue.DisplayID, readyIssue.DisplayID, blockedIssue.DisplayID, reviewIssue.DisplayID, claim.Attempt.ID, reviewRequest.ID} {
		if !strings.Contains(htmlText, want) {
			t.Fatalf("board HTML missing identifier %q", want)
		}
	}
}

type boardIssueRef struct {
	ID        string
	DisplayID string
}

func mustCreateBoardIssue(t *testing.T, session *mcp.ClientSession, arguments map[string]any) boardIssueRef {
	t.Helper()
	created := callIntegrationTool(t, session, "create_issue", arguments)
	var issue struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeIntegrationResult(t, created, &issue)
	if created.IsError || issue.ID == "" || issue.DisplayID == "" {
		t.Fatalf("create_issue result = %#v, decoded = %#v", created, issue)
	}
	return boardIssueRef{ID: issue.ID, DisplayID: issue.DisplayID}
}
