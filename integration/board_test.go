//go:build integration

package integration_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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

func TestIntegrationBoardServe(t *testing.T) {
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

func TestIntegrationBoardRefresh(t *testing.T) {
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
	env := newIntegrationEnvironment(t)
	session := env.connect(t)

	root := mustCreateBoardIssue(t, session, map[string]any{"type": "task", "title": "Root graph", "status": "ready"})
	for i := 0; i < 99; i++ {
		issue := mustCreateBoardIssue(t, session, map[string]any{
			"type": "task", "title": "Graph node " + string(rune('A'+i%26)), "status": "ready",
			"description": strings.Repeat("long-body-", 2000),
		})
		result := callIntegrationTool(t, session, "manage_issue_relation", map[string]any{
			"action": "add", "source_issue_id": issue.DisplayID, "target_issue_id": root.DisplayID, "relation_type": "blocks",
		})
		if result.IsError {
			t.Fatalf("manage_issue_relation %d error = %#v", i, result)
		}
	}

	graphResult := callIntegrationTool(t, session, "get_issue_graph", map[string]any{
		"root_issue_id": root.DisplayID, "depth": 1, "max_nodes": 100,
	})
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
