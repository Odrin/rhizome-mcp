//go:build integration

package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/ports"
)

// TestIntegrationBoardCommand builds a mixed-status scenario (open, ready,
// blocked-with-reason, a claimed/leased attempt, and an open review request)
// through the MCP server, then verifies `rhizome-mcp board` in all three
// modes: --format table, --format json, and --output (self-contained HTML).
func TestIntegrationBoardCommand(t *testing.T) {
	t.Parallel()
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
	targetVersion, targetEventID := currentReviewTarget(t, db, reviewIssue.ID)
	reviewCreated, err := reviewRepository.CreateReviewRequest(context.Background(), ports.CreateReviewRequestCommand{
		RequestID: newIntegrationULID(t), TargetID: newIntegrationULID(t),
		Purposes:           []string{"implementation"},
		IssueID:            reviewIssue.ID,
		TargetIssueVersion: targetVersion,
		TargetEventID:      targetEventID,
		OccurredAt:         time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create review request: %v", err)
	}
	var reviewRequest struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	reviewRequest.ID = reviewCreated.Request.ID
	reviewRequest.Status = string(reviewCreated.Request.Status)

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
		Truncation struct {
			BlockedIssues      bool `json:"blocked_issues"`
			ActiveAttempts     bool `json:"active_attempts"`
			ActiveReservations bool `json:"active_reservations"`
			ReviewRequests     bool `json:"review_requests"`
		} `json:"truncation"`
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

	// The truncation object must actually be in the payload -- decoding into a
	// struct alone would leave every flag false if the key were missing -- and
	// every flag must be false on a fixture project well under every bound.
	for _, key := range []string{"truncation", "blocked_issues", "active_attempts", "active_reservations", "review_requests"} {
		if !bytes.Contains(jsonOutput, []byte("\""+key+"\"")) {
			t.Fatalf("board JSON is missing %q:\n%s", key, jsonOutput)
		}
	}
	if board.Truncation.BlockedIssues || board.Truncation.ActiveAttempts || board.Truncation.ActiveReservations || board.Truncation.ReviewRequests {
		t.Fatalf("truncation flags should all be false on small fixture project: %#v", board.Truncation)
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

func TestIntegrationBoardServe(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)
	session := env.connect(t)
	openIssue := mustCreateBoardIssue(t, session, map[string]any{
		"type": "task", "title": "Board serve issue", "status": "open",
	})
	readyIssue := mustCreateBoardIssue(t, session, map[string]any{
		"type": "task", "title": "Board serve claimed issue", "status": "ready",
	})
	relatedIssue := mustCreateBoardIssue(t, session, map[string]any{
		"type": "task", "title": "Board serve related issue", "status": "ready",
	})
	relationResult := callIntegrationTool(t, session, "manage_issue_relation", map[string]any{
		"action":          "add",
		"source_issue_id": relatedIssue.DisplayID,
		"target_issue_id": openIssue.DisplayID,
		"relation_type":   "related_to",
	})
	if relationResult.IsError {
		t.Fatalf("manage_issue_relation result = %#v", relationResult)
	}

	claimed := callIntegrationTool(t, session, "claim_issue", map[string]any{
		"issue_id":      readyIssue.DisplayID,
		"lease_seconds": 600,
	})
	var claim struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
	}
	decodeIntegrationResult(t, claimed, &claim)
	if claimed.IsError || claim.Attempt.ID == "" {
		t.Fatalf("claim_issue result = %#v, decoded = %#v", claimed, claim)
	}

	server := launchIntegrationBoardServer(t, env, "127.0.0.1:0")
	t.Cleanup(func() { stopIntegrationBoardServer(t, server) })

	endpoint := server.waitForEndpoint(t)
	if !strings.HasPrefix(endpoint, "http://127.0.0.1:") || !strings.HasSuffix(endpoint, "/") {
		t.Fatalf("canonical board URL = %q, want loopback URL ending in /", endpoint)
	}

	client := &http.Client{Timeout: integrationTimeout}
	response, err := client.Get(endpoint)
	if err != nil {
		t.Fatalf("get board page: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatalf("read board page body: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("board page status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if !strings.Contains(response.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("board page content type = %q, want html", response.Header.Get("Content-Type"))
	}
	if got := response.Header.Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") {
		t.Fatalf("board page CSP = %q", got)
	}
	if got := response.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("board page nosniff = %q", got)
	}
	if got := response.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("board page referrer policy = %q", got)
	}
	if got := response.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("board page cache control = %q", got)
	}
	if !strings.Contains(string(body), "Rhizome status board") {
		t.Fatalf("board page body missing heading: %s", body)
	}

	activeHref := findBoardAnchorHref(t, string(body), readyIssue.DisplayID)
	activeResponse, err := client.Get(strings.TrimSuffix(endpoint, "/") + activeHref)
	if err != nil {
		t.Fatalf("follow active attempt link: %v", err)
	}
	activeBody, err := io.ReadAll(activeResponse.Body)
	activeResponse.Body.Close()
	if err != nil {
		t.Fatalf("read active attempt link body: %v", err)
	}
	if activeResponse.StatusCode != http.StatusOK {
		t.Fatalf("active attempt link status = %d, want %d", activeResponse.StatusCode, http.StatusOK)
	}
	if !strings.Contains(string(activeBody), readyIssue.DisplayID) {
		t.Fatalf("active attempt link body missing display id %q: %s", readyIssue.DisplayID, activeBody)
	}

	svgHref, svgLabel := findBoardSVGAnchorHref(t, string(body), relatedIssue.DisplayID)
	if svgLabel != relatedIssue.DisplayID {
		t.Fatalf("svg anchor aria-label = %q, want %q", svgLabel, relatedIssue.DisplayID)
	}
	svgResponse, err := client.Get(strings.TrimSuffix(endpoint, "/") + svgHref)
	if err != nil {
		t.Fatalf("follow svg anchor link: %v", err)
	}
	svgBody, err := io.ReadAll(svgResponse.Body)
	svgResponse.Body.Close()
	if err != nil {
		t.Fatalf("read svg anchor link body: %v", err)
	}
	if svgResponse.StatusCode != http.StatusOK {
		t.Fatalf("svg anchor link status = %d, want %d", svgResponse.StatusCode, http.StatusOK)
	}
	if !strings.Contains(string(svgBody), relatedIssue.DisplayID) {
		t.Fatalf("svg anchor link body missing display id %q: %s", relatedIssue.DisplayID, svgBody)
	}

	detailResponse, err := client.Get(strings.TrimSuffix(endpoint, "/") + "/issues/" + openIssue.DisplayID)
	if err != nil {
		t.Fatalf("get issue detail page: %v", err)
	}
	detailBody, err := io.ReadAll(detailResponse.Body)
	detailResponse.Body.Close()
	if err != nil {
		t.Fatalf("read issue detail page body: %v", err)
	}
	if detailResponse.StatusCode != http.StatusOK {
		t.Fatalf("issue detail page status = %d, want %d", detailResponse.StatusCode, http.StatusOK)
	}
	if !strings.Contains(string(detailBody), openIssue.DisplayID) {
		t.Fatalf("issue detail page missing display id: %s", detailBody)
	}

	malformedResponse, err := client.Get(strings.TrimSuffix(endpoint, "/") + "/issues/")
	if err != nil {
		t.Fatalf("get malformed issue path: %v", err)
	}
	malformedBody, err := io.ReadAll(malformedResponse.Body)
	malformedResponse.Body.Close()
	if err != nil {
		t.Fatalf("read malformed issue path body: %v", err)
	}
	if malformedResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed issue path status = %d, want %d", malformedResponse.StatusCode, http.StatusBadRequest)
	}
	if len(strings.TrimSpace(string(malformedBody))) == 0 {
		t.Fatalf("malformed issue path body empty: %s", malformedBody)
	}

	missingResponse, err := client.Get(strings.TrimSuffix(endpoint, "/") + "/issues/ISSUE-999999")
	if err != nil {
		t.Fatalf("get missing issue path: %v", err)
	}
	missingBody, err := io.ReadAll(missingResponse.Body)
	missingResponse.Body.Close()
	if err != nil {
		t.Fatalf("read missing issue path body: %v", err)
	}
	if missingResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("missing issue path status = %d, want %d", missingResponse.StatusCode, http.StatusNotFound)
	}
	if len(strings.TrimSpace(string(missingBody))) == 0 {
		t.Fatalf("missing issue path body empty: %s", missingBody)
	}

	apiResponse, err := client.Get(strings.TrimSuffix(endpoint, "/") + "/api/board")
	if err != nil {
		t.Fatalf("get board api: %v", err)
	}
	apiBody, err := io.ReadAll(apiResponse.Body)
	apiResponse.Body.Close()
	if err != nil {
		t.Fatalf("read board api body: %v", err)
	}
	if apiResponse.StatusCode != http.StatusOK {
		t.Fatalf("board api status = %d, want %d", apiResponse.StatusCode, http.StatusOK)
	}
	var payload struct {
		GeneratedAt  string `json:"generated_at"`
		StatusCounts []struct {
			EffectiveStatus string `json:"effective_status"`
			Count           int    `json:"count"`
		} `json:"status_counts"`
	}
	if err := json.Unmarshal(apiBody, &payload); err != nil {
		t.Fatalf("decode board api response: %v\nbody: %s", err, apiBody)
	}
	if payload.GeneratedAt == "" {
		t.Fatalf("board api payload missing generated_at: %s", apiBody)
	}
	if payload.StatusCounts == nil {
		t.Fatalf("board api payload missing status counts: %s", apiBody)
	}

	postRequest, err := http.NewRequest(http.MethodPost, endpoint, nil)
	if err != nil {
		t.Fatalf("construct POST request: %v", err)
	}
	postResponse, err := client.Do(postRequest)
	if err != nil {
		t.Fatalf("send POST request: %v", err)
	}
	postResponse.Body.Close()
	if postResponse.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want %d", postResponse.StatusCode, http.StatusMethodNotAllowed)
	}
	if got := postResponse.Header.Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("POST allow header = %q, want %q", got, "GET, HEAD")
	}

	hostRequest, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatalf("construct hostile request: %v", err)
	}
	hostRequest.Host = "example.com:8080"
	hostResponse, err := client.Do(hostRequest)
	if err != nil {
		t.Fatalf("send hostile host request: %v", err)
	}
	hostResponse.Body.Close()
	if hostResponse.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("host mismatch status = %d, want %d", hostResponse.StatusCode, http.StatusMisdirectedRequest)
	}

	stopIntegrationBoardServer(t, server)
	if err := server.waitForExit(t); err != nil {
		t.Fatalf("wait for board server exit: %v", err)
	}
}

func TestIntegrationBoardServeSearch(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)
	session := env.connect(t)

	issue := mustCreateBoardIssue(t, session, map[string]any{
		"type": "task", "title": "Board search issue", "status": "ready",
	})
	commentResult := callIntegrationTool(t, session, "add_comment", map[string]any{
		"issue_id": issue.DisplayID,
		"content":  "board_search_comment_token",
	})
	if commentResult.IsError {
		t.Fatalf("add_comment result = %#v", commentResult)
	}
	decisionResult := callIntegrationTool(t, session, "record_decision", map[string]any{
		"issue_id": issue.DisplayID,
		"title":    "board_search_decision_token",
		"summary":  "board search decision summary",
		"content":  "board_search_decision_content",
		"status":   "active",
	})
	if decisionResult.IsError {
		t.Fatalf("record_decision result = %#v", decisionResult)
	}

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
	targetVersion, targetEventID := currentReviewTarget(t, db, issue.ID)
	reviewCreated, err := reviewRepository.CreateReviewRequest(context.Background(), ports.CreateReviewRequestCommand{
		RequestID: newIntegrationULID(t), TargetID: newIntegrationULID(t),
		Purposes:           []string{"implementation"},
		IssueID:            issue.ID,
		TargetIssueVersion: targetVersion,
		TargetEventID:      targetEventID,
		ArtifactIDs:        []string{"board_search_review_token"},
		OccurredAt:         time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create review request: %v", err)
	}
	var review struct {
		ID string `json:"id"`
	}
	review.ID = reviewCreated.Request.ID
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
		t.Fatalf("claim_issue result = %#v, decoded = %#v", claimResult, claim)
	}
	noteResult := callIntegrationTool(t, session, "save_attempt_note", map[string]any{
		"attempt_id":  claim.Attempt.ID,
		"lease_token": claim.LeaseToken,
		"kind":        "checkpoint",
		"content":     "board_search_attempt_note_token",
	})
	if noteResult.IsError {
		t.Fatalf("save_attempt_note result = %#v", noteResult)
	}

	server := launchIntegrationBoardServer(t, env, "127.0.0.1:0")
	t.Cleanup(func() { stopIntegrationBoardServer(t, server) })
	endpoint := server.waitForEndpoint(t)
	client := &http.Client{Timeout: integrationTimeout}

	searchURL := strings.TrimSuffix(endpoint, "/") + "/api/search?q=board_search_comment_token&entity_type=comment"
	searchResponse, err := client.Get(searchURL)
	if err != nil {
		t.Fatalf("get comment search api: %v", err)
	}
	searchBody, err := io.ReadAll(searchResponse.Body)
	searchResponse.Body.Close()
	if err != nil {
		t.Fatalf("read comment search api body: %v", err)
	}
	if searchResponse.StatusCode != http.StatusOK {
		t.Fatalf("comment search api status = %d, want %d, body: %s", searchResponse.StatusCode, http.StatusOK, string(searchBody))
	}
	var searchPayload struct {
		Results []struct {
			EntityType string  `json:"entity_type"`
			EntityID   string  `json:"entity_id"`
			IssueID    *string `json:"issue_id"`
			Title      string  `json:"title"`
		} `json:"results"`
	}
	if err := json.Unmarshal(searchBody, &searchPayload); err != nil {
		t.Fatalf("decode comment search api response: %v\nbody: %s", err, string(searchBody))
	}
	if len(searchPayload.Results) == 0 || searchPayload.Results[0].EntityType != "comment" {
		t.Fatalf("comment search payload = %#v", searchPayload)
	}

	pageURL := strings.TrimSuffix(endpoint, "/") + "/search?q=board_search_decision_token"
	pageResponse, err := client.Get(pageURL)
	if err != nil {
		t.Fatalf("get search page: %v", err)
	}
	pageBody, err := io.ReadAll(pageResponse.Body)
	pageResponse.Body.Close()
	if err != nil {
		t.Fatalf("read search page body: %v", err)
	}
	if pageResponse.StatusCode != http.StatusOK {
		t.Fatalf("search page status = %d, want %d", pageResponse.StatusCode, http.StatusOK)
	}
	if !strings.Contains(string(pageBody), `href="/issues/`+issue.DisplayID+`"`) {
		t.Fatalf("search page missing issue link for %q: %s", issue.DisplayID, string(pageBody))
	}
	if !strings.Contains(string(pageBody), "board_search_decision_token") {
		t.Fatalf("search page missing decision token output: %s", string(pageBody))
	}

	invalidSearchResponse, err := client.Get(strings.TrimSuffix(endpoint, "/") + "/api/search?q=multi-project")
	if err != nil {
		t.Fatalf("get invalid search api: %v", err)
	}
	invalidBody, err := io.ReadAll(invalidSearchResponse.Body)
	invalidSearchResponse.Body.Close()
	if err != nil {
		t.Fatalf("read invalid search api body: %v", err)
	}
	if invalidSearchResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid search api status = %d, want %d, body: %s", invalidSearchResponse.StatusCode, http.StatusBadRequest, string(invalidBody))
	}

	issue2 := mustCreateBoardIssue(t, session, map[string]any{
		"type": "task", "title": "Board search issue 2", "status": "ready",
	})
	issue3 := mustCreateBoardIssue(t, session, map[string]any{
		"type": "task", "title": "Board search issue 3", "status": "ready",
	})
	for _, issueRef := range []boardIssueRef{issue2, issue3} {
		if result := callIntegrationTool(t, session, "add_comment", map[string]any{"issue_id": issueRef.DisplayID, "content": "board_search_cursor_token"}); result.IsError {
			t.Fatalf("add_comment for cursor token failed: %#v", result)
		}
	}

	cursorSearchURL := strings.TrimSuffix(endpoint, "/") + "/api/search?q=board_search_cursor_token&entity_type=comment&limit=1"
	cursorResponse, err := client.Get(cursorSearchURL)
	if err != nil {
		t.Fatalf("get cursor search api: %v", err)
	}
	cursorBody, err := io.ReadAll(cursorResponse.Body)
	cursorResponse.Body.Close()
	if err != nil {
		t.Fatalf("read cursor search api body: %v", err)
	}
	if cursorResponse.StatusCode != http.StatusOK {
		t.Fatalf("cursor search api status = %d, want %d, body: %s", cursorResponse.StatusCode, http.StatusOK, string(cursorBody))
	}
	var cursorPayload struct {
		Results    []map[string]any `json:"results"`
		NextCursor *string          `json:"next_cursor"`
		HasMore    bool             `json:"has_more"`
	}
	if err := json.Unmarshal(cursorBody, &cursorPayload); err != nil {
		t.Fatalf("decode cursor search api response: %v\nbody: %s", err, string(cursorBody))
	}
	if len(cursorPayload.Results) == 0 || cursorPayload.NextCursor == nil || !cursorPayload.HasMore {
		t.Fatalf("cursor search payload = %#v", cursorPayload)
	}
	cursorFollowURL := strings.TrimSuffix(endpoint, "/") + "/api/search?q=board_search_cursor_token&entity_type=comment&limit=1&cursor=" + url.QueryEscape(*cursorPayload.NextCursor)
	cursorFollowResponse, err := client.Get(cursorFollowURL)
	if err != nil {
		t.Fatalf("get cursor follow-up search api: %v", err)
	}
	cursorFollowBody, err := io.ReadAll(cursorFollowResponse.Body)
	cursorFollowResponse.Body.Close()
	if err != nil {
		t.Fatalf("read cursor follow-up search api body: %v", err)
	}
	if cursorFollowResponse.StatusCode != http.StatusOK {
		t.Fatalf("cursor follow-up search api status = %d, want %d, body: %s", cursorFollowResponse.StatusCode, http.StatusOK, string(cursorFollowBody))
	}

	newCommentToken := fmt.Sprintf("board_search_live_%d", time.Now().UnixNano())
	newCommentResult := callIntegrationTool(t, session, "add_comment", map[string]any{"issue_id": issue.DisplayID, "content": newCommentToken})
	if newCommentResult.IsError {
		t.Fatalf("add_comment for live token failed: %#v", newCommentResult)
	}
	liveSearchResponse, err := client.Get(strings.TrimSuffix(endpoint, "/") + "/api/search?q=" + url.QueryEscape(newCommentToken) + "&entity_type=comment")
	if err != nil {
		t.Fatalf("get live search api: %v", err)
	}
	liveSearchBody, err := io.ReadAll(liveSearchResponse.Body)
	liveSearchResponse.Body.Close()
	if err != nil {
		t.Fatalf("read live search api body: %v", err)
	}
	if liveSearchResponse.StatusCode != http.StatusOK {
		t.Fatalf("live search status = %d, want %d, body: %s", liveSearchResponse.StatusCode, http.StatusOK, string(liveSearchBody))
	}
	var livePayload struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(liveSearchBody, &livePayload); err != nil || len(livePayload.Results) == 0 {
		t.Fatalf("live search payload = %s, decode error = %v", string(liveSearchBody), err)
	}
}

func TestIntegrationBoardRefresh(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)
	session := env.connect(t)
	issue := mustCreateBoardIssue(t, session, map[string]any{
		"type": "task", "title": "Board refresh issue", "status": "open",
	})

	server := launchIntegrationBoardServer(t, env, "127.0.0.1:0")
	t.Cleanup(func() { stopIntegrationBoardServer(t, server) })
	endpoint := server.waitForEndpoint(t)
	client := &http.Client{Timeout: integrationTimeout}
	boardURL := strings.TrimSuffix(endpoint, "/") + "/api/board"

	initialResponse, err := client.Get(boardURL)
	if err != nil {
		t.Fatalf("get initial board api: %v", err)
	}
	initialBody, err := io.ReadAll(initialResponse.Body)
	initialResponse.Body.Close()
	if err != nil {
		t.Fatalf("read initial board api body: %v", err)
	}
	if initialResponse.StatusCode != http.StatusOK {
		t.Fatalf("initial board api status = %d, want %d", initialResponse.StatusCode, http.StatusOK)
	}
	initialETag := initialResponse.Header.Get("ETag")
	if initialETag == "" {
		t.Fatalf("initial board api missing ETag: %s", initialBody)
	}

	conditionalRequest, err := http.NewRequest(http.MethodGet, boardURL, nil)
	if err != nil {
		t.Fatalf("construct initial conditional board request: %v", err)
	}
	conditionalRequest.Header.Set("If-None-Match", initialETag)
	conditionalResponse, err := client.Do(conditionalRequest)
	if err != nil {
		t.Fatalf("get initial conditional board api: %v", err)
	}
	conditionalBody, err := io.ReadAll(conditionalResponse.Body)
	conditionalResponse.Body.Close()
	if err != nil {
		t.Fatalf("read initial conditional body: %v", err)
	}
	if conditionalResponse.StatusCode != http.StatusNotModified {
		t.Fatalf("initial conditional board api status = %d, want %d", conditionalResponse.StatusCode, http.StatusNotModified)
	}
	if len(conditionalBody) != 0 {
		t.Fatalf("initial conditional board api body length = %d, want 0", len(conditionalBody))
	}

	updateResult := callIntegrationTool(t, session, "update_issue", map[string]any{
		"issue_id":         issue.DisplayID,
		"expected_version": 1,
		"changes":          map[string]any{"title": "Board refresh issue revised"},
	})
	if updateResult.IsError {
		t.Fatalf("update_issue result = %#v", updateResult)
	}

	changedRequest, err := http.NewRequest(http.MethodGet, boardURL, nil)
	if err != nil {
		t.Fatalf("construct changed board request: %v", err)
	}
	changedRequest.Header.Set("If-None-Match", initialETag)
	changedResponse, err := client.Do(changedRequest)
	if err != nil {
		t.Fatalf("get changed board api: %v", err)
	}
	changedBody, err := io.ReadAll(changedResponse.Body)
	changedResponse.Body.Close()
	if err != nil {
		t.Fatalf("read changed board api body: %v", err)
	}
	if changedResponse.StatusCode != http.StatusOK {
		t.Fatalf("changed board api status = %d, want %d", changedResponse.StatusCode, http.StatusOK)
	}
	changedETag := changedResponse.Header.Get("ETag")
	if changedETag == "" || changedETag == initialETag {
		t.Fatalf("changed board api ETag = %q, want changed from %q", changedETag, initialETag)
	}
	if len(changedBody) == 0 {
		t.Fatalf("changed board api body empty")
	}

	conditionalChangedRequest, err := http.NewRequest(http.MethodGet, boardURL, nil)
	if err != nil {
		t.Fatalf("construct conditional changed board request: %v", err)
	}
	conditionalChangedRequest.Header.Set("If-None-Match", changedETag)
	conditionalChangedResponse, err := client.Do(conditionalChangedRequest)
	if err != nil {
		t.Fatalf("get conditional changed board api: %v", err)
	}
	conditionalChangedBody, err := io.ReadAll(conditionalChangedResponse.Body)
	conditionalChangedResponse.Body.Close()
	if err != nil {
		t.Fatalf("read conditional changed board api body: %v", err)
	}
	if conditionalChangedResponse.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional changed board api status = %d, want %d", conditionalChangedResponse.StatusCode, http.StatusNotModified)
	}
	if len(conditionalChangedBody) != 0 {
		t.Fatalf("conditional changed board api body length = %d, want 0", len(conditionalChangedBody))
	}
}

func findBoardAnchorHref(t *testing.T, body, displayID string) string {
	t.Helper()
	re := regexp.MustCompile(`<a[^>]*href="([^"]+)"[^>]*>([^<]*)</a>`)
	for _, match := range re.FindAllStringSubmatch(body, -1) {
		if strings.TrimSpace(match[2]) == displayID {
			return match[1]
		}
	}
	t.Fatalf("missing anchor for display ID %q in HTML body", displayID)
	return ""
}

func findBoardSVGAnchorHref(t *testing.T, body, displayID string) (string, string) {
	t.Helper()
	re := regexp.MustCompile(`<a[^>]*href="([^"]+)"[^>]*aria-label="([^"]+)"[^>]*>.*?</a>`)
	for _, match := range re.FindAllStringSubmatch(body, -1) {
		if match[2] == displayID {
			return match[1], match[2]
		}
	}
	t.Fatalf("missing svg anchor for display ID %q in HTML body", displayID)
	return "", ""
}

type integrationBoardServer struct {
	cmd       *exec.Cmd
	output    *capturedOutput
	endpointC chan string
	doneC     chan error

	exitedMu sync.Mutex
	exited   bool
}

func (server *integrationBoardServer) hasExited() bool {
	server.exitedMu.Lock()
	defer server.exitedMu.Unlock()
	return server.exited
}

func launchIntegrationBoardServer(t *testing.T, env integrationEnvironment, httpAddress string) *integrationBoardServer {
	t.Helper()
	args := []string{"--data-root", env.dataRoot, "board", "--serve", "--http-address", httpAddress}
	cmd := exec.Command(integrationBinary, args...)
	cmd.Dir = env.repository

	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()
	output := &capturedOutput{}
	server := &integrationBoardServer{
		cmd:       cmd,
		output:    output,
		endpointC: make(chan string, 1),
		doneC:     make(chan error, 1),
	}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	if err := cmd.Start(); err != nil {
		t.Fatalf("start integration board server: %v", err)
	}

	go func() {
		scanner := bufio.NewScanner(stdoutReader)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			output.WriteString(line + "\n")
			if endpoint := parseIntegrationBoardServeURL(line); endpoint != "" {
				select {
				case server.endpointC <- endpoint:
				default:
				}
			}
		}
		_ = scanner.Err()
		_ = stdoutReader.Close()
	}()
	go func() {
		scanner := bufio.NewScanner(stderrReader)
		for scanner.Scan() {
			output.WriteString(scanner.Text() + "\n")
		}
		_ = scanner.Err()
		_ = stderrReader.Close()
	}()
	go func() {
		err := cmd.Wait()
		_ = stdoutWriter.Close()
		_ = stderrWriter.Close()
		server.exitedMu.Lock()
		server.exited = true
		server.exitedMu.Unlock()
		server.doneC <- err
	}()
	return server
}

func (server *integrationBoardServer) waitForEndpoint(t *testing.T) string {
	t.Helper()
	deadline := time.NewTimer(integrationTimeout)
	defer deadline.Stop()
	for {
		select {
		case endpoint := <-server.endpointC:
			return endpoint
		case err := <-server.doneC:
			t.Fatalf("integration board server exited before listening: %v\noutput:\n%s", err, server.output.String())
		case <-deadline.C:
			t.Fatalf("timed out waiting for integration board server endpoint\noutput:\n%s", server.output.String())
		}
	}
}

func (server *integrationBoardServer) waitForExit(t *testing.T) error {
	t.Helper()
	if server.hasExited() {
		return nil
	}
	select {
	case err := <-server.doneC:
		return err
	case <-time.After(integrationTimeout):
		t.Fatalf("timed out waiting for integration board server exit\noutput:\n%s", server.output.String())
		return nil
	}
}

func stopIntegrationBoardServer(t *testing.T, server *integrationBoardServer) {
	t.Helper()
	if server == nil || server.cmd == nil || server.cmd.Process == nil {
		return
	}
	if server.hasExited() {
		return
	}
	if err := server.cmd.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		_ = server.cmd.Process.Kill()
	}
	select {
	case <-server.doneC:
	case <-time.After(2 * time.Second):
		_ = server.cmd.Process.Kill()
		select {
		case <-server.doneC:
		case <-time.After(integrationTimeout):
		}
	}
}

func parseIntegrationBoardServeURL(line string) string {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return trimmed
	}
	return ""
}

func TestIntegrationGraphProjectionStaysBoundedWithLargeBodies(t *testing.T) {
	t.Parallel()
	const largeGraphFixtureTimeout = 30 * time.Second

	env := newIntegrationEnvironment(t)
	session := env.connect(t)

	root := mustCreateBoardIssueWithTimeout(t, session, map[string]any{"type": "task", "title": "Root graph", "status": "ready"}, largeGraphFixtureTimeout)
	for i := 0; i < 99; i++ {
		issue := mustCreateBoardIssueWithTimeout(t, session, map[string]any{
			"type": "task", "title": "Graph node " + string(rune('A'+i%26)), "status": "ready",
			"description": strings.Repeat("long-body-", 2000),
		}, largeGraphFixtureTimeout)
		result := callIntegrationToolWithTimeout(t, session, "manage_issue_relation", map[string]any{
			"action": "add", "source_issue_id": issue.DisplayID, "target_issue_id": root.DisplayID, "relation_type": "blocks",
		}, largeGraphFixtureTimeout)
		if result.IsError {
			t.Fatalf("manage_issue_relation %d error = %#v", i, result)
		}
	}

	graphResult := callIntegrationToolWithTimeout(t, session, "get_issue_graph", map[string]any{
		"root_issue_id": root.DisplayID, "depth": 1, "max_nodes": 100,
	}, largeGraphFixtureTimeout)
	if graphResult.IsError {
		t.Fatalf("get_issue_graph error = %#v", graphResult)
	}
	data, err := json.Marshal(graphResult.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if len(data) > 96*1024 {
		t.Fatalf("graph payload size = %d bytes, want <= %d", len(data), 96*1024)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode graph payload: %v", err)
	}
	nodes, ok := payload["nodes"].([]any)
	if !ok || len(nodes) != 100 {
		t.Fatalf("graph nodes = %#v", payload["nodes"])
	}
	for index, entry := range nodes {
		node, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("graph node %d = %#v", index, entry)
		}
		for _, field := range []string{"description", "acceptance_criteria", "labels", "parent_issue_id", "blocked_reason", "created_at", "updated_at", "closed_at", "archived_at", "active_attempt_id"} {
			if _, exists := node[field]; exists {
				t.Fatalf("graph node %d unexpectedly includes %q: %#v", index, field, node)
			}
		}
	}
}

type boardIssueRef struct {
	ID        string
	DisplayID string
}

func mustCreateBoardIssue(t *testing.T, session *mcp.ClientSession, arguments map[string]any) boardIssueRef {
	t.Helper()
	return mustCreateBoardIssueWithTimeout(t, session, arguments, integrationTimeout)
}

// TestIntegrationBoardTruncationReportsTrueEntryPoints verifies that when
// the planning graph is truncated, the board still reports an entry-point
// *count* computed over the whole snapshot rather than over retained nodes,
// and marks the graph truncated. With include_terminal=false the board
// excludes done/cancelled issues from the node budget, so enough non-terminal
// issues are created to exceed the 100-node budget.
//
// ISSUE-225 Q2 bounded the serialized list: the count still covers every
// claimable issue, so truncation never shrinks how much claimable work the
// client is told about, but the emitted list is capped at the same node
// budget so a project with thousands of claimable issues does not ship
// thousands of ULIDs on every board poll. count > len(list) plus the
// truncation reason is what makes the shrink explicit instead of silent.
func TestIntegrationBoardTruncationReportsTrueEntryPoints(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)
	session := env.connect(t)

	// Create 110 ready issues to exceed the 100-node budget (they all count
	// against the budget since include_terminal=false excludes only done/cancelled)
	readyIssues := make([]boardIssueRef, 0)
	for i := 0; i < 110; i++ {
		issue := mustCreateBoardIssue(t, session, map[string]any{
			"type": "task", "title": fmt.Sprintf("Ready task %d", i), "status": "ready",
		})
		readyIssues = append(readyIssues, issue)
	}

	// Also create some done issues that should not consume the budget
	for i := 0; i < 50; i++ {
		mustCreateBoardIssue(t, session, map[string]any{
			"type": "task", "title": fmt.Sprintf("Done task %d", i), "status": "done",
		})
	}

	// Run the board command with JSON output
	jsonOutput := runIntegrationCommand(t, env, "--data-root", env.dataRoot, "board", "--format", "json")
	var board struct {
		PlanningGraph struct {
			Truncated         bool     `json:"truncated"`
			RetainedNodeCount int      `json:"retained_node_count"`
			EntryPoints       []string `json:"entry_points"`
			Summary           struct {
				EntryPointCount int `json:"entry_point_count"`
			} `json:"summary"`
		} `json:"planning_graph"`
	}
	if err := json.Unmarshal(jsonOutput, &board); err != nil {
		t.Fatalf("decode board --format json output: %v\noutput:\n%s", err, jsonOutput)
	}

	// Verify truncation is reported
	if !board.PlanningGraph.Truncated {
		t.Fatal("expected planning_graph.truncated = true when graph exceeds 100-node budget")
	}

	// Verify retained node count is reported
	if board.PlanningGraph.RetainedNodeCount == 0 {
		t.Fatal("expected planning_graph.retained_node_count > 0")
	}
	if board.PlanningGraph.RetainedNodeCount > 100 {
		t.Fatalf("retained_node_count = %d, want at most 100 (the node budget)", board.PlanningGraph.RetainedNodeCount)
	}

	// The count still covers every claimable issue -- this is the ISSUE-219
	// guarantee, and it is what a client reads to learn how much work exists.
	if board.PlanningGraph.Summary.EntryPointCount != 110 {
		t.Fatalf("summary.entry_point_count = %d, want 110 (all ready issues, computed over the full snapshot)", board.PlanningGraph.Summary.EntryPointCount)
	}
	// The serialized list is bounded by the same 100-node budget, and the gap
	// between count and list is the explicit signal that it was bounded.
	if len(board.PlanningGraph.EntryPoints) != 100 {
		t.Fatalf("len(entry_points) = %d, want 100 (capped at the node budget)", len(board.PlanningGraph.EntryPoints))
	}
	if board.PlanningGraph.Summary.EntryPointCount <= len(board.PlanningGraph.EntryPoints) {
		t.Fatal("entry_point_count must exceed the emitted list so the cap is never silent")
	}
	// Every emitted entry point is still a real claimable issue, and the
	// emitted set is a subset of the ready issues rather than an arbitrary mix.
	readyByID := make(map[string]bool, len(readyIssues))
	for _, readyIssue := range readyIssues {
		readyByID[readyIssue.ID] = true
	}
	for _, entryPoint := range board.PlanningGraph.EntryPoints {
		if !readyByID[entryPoint] {
			t.Fatalf("entry_points contains %s, which is not one of the ready issues", entryPoint)
		}
	}
}

func mustCreateBoardIssueWithTimeout(t *testing.T, session *mcp.ClientSession, arguments map[string]any, timeout time.Duration) boardIssueRef {
	t.Helper()
	created := callIntegrationToolWithTimeout(t, session, "create_issue", arguments, timeout)
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
