//go:build integration

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/config"
	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
	"rhizome-mcp/internal/projectconfig"
	projectruntime "rhizome-mcp/internal/runtime"
)

const integrationTimeout = 10 * time.Second

var integrationBinary string

func TestMain(m *testing.M) {
	tempDir, err := os.MkdirTemp("", "rhizome-mcp-integration-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create integration build directory: %v\n", err)
		os.Exit(1)
	}

	integrationBinary = filepath.Join(tempDir, "rhizome-mcp")
	command := exec.Command("go", "build", "-o", integrationBinary, ".")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build integration server: %v\n%s", err, output.String())
		exitIntegrationTests(tempDir, 1)
	}

	exitIntegrationTests(tempDir, m.Run())
}

func exitIntegrationTests(tempDir string, exitCode int) {
	if err := os.RemoveAll(tempDir); err != nil {
		fmt.Fprintf(os.Stderr, "remove integration build directory: %v\n", err)
		exitCode = 1
	}
	os.Exit(exitCode)
}

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

func TestIntegrationReviewWorkflow(t *testing.T) {
	env := newIntegrationEnvironment(t)
	session := env.connect(t)

	created := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type":                  "bug",
		"title":                 "Review workflow integration",
		"description":           "Exercise review request completion through the MCP transport.",
		"status":                "review",
		"labels":                []string{"integration"},
		"create_missing_labels": true,
	})
	var issue struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeIntegrationResult(t, created, &issue)
	if created.IsError || issue.ID == "" || issue.DisplayID == "" {
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
	var latestEventID int64
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM issue_events`).Scan(&latestEventID)
	}); err != nil {
		t.Fatalf("read latest issue event id: %v", err)
	}
	requested := callIntegrationTool(t, session, "create_review_request", map[string]any{
		"issue_id":             issue.DisplayID,
		"target_issue_version": 1,
		"target_event_id":      latestEventID,
		"artifact_ids":         []string{"artifact-1"},
	})
	var reviewRequest struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	decodeIntegrationResult(t, requested, &reviewRequest)
	if requested.IsError || reviewRequest.ID == "" || reviewRequest.Status != "open" {
		t.Fatalf("create_review_request result = %#v, decoded = %#v", requested, reviewRequest)
	}

	reviewRepository, err := sqlite.NewReviewRepository(db)
	if err != nil {
		t.Fatalf("new review repository: %v", err)
	}
	if _, err := reviewRepository.ClaimReviewRequest(context.Background(), ports.ReviewMutationCommand{
		RequestID:       reviewRequest.ID,
		ExpectedVersion: 1,
		ActiveAttemptID: &claim.Attempt.ID,
		OccurredAt:      time.Now().UTC().Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("claim review request: %v", err)
	}

	finished := callIntegrationTool(t, session, "finish_attempt", map[string]any{
		"attempt_id":     claim.Attempt.ID,
		"lease_token":    claim.LeaseToken,
		"outcome":        "completed",
		"result_summary": "Review workflow integration passed.",
		"review_outcome": "approved",
		"verification":   []string{"go test -tags=integration ."},
	})
	var completion struct {
		Attempt struct {
			Status string `json:"status"`
		} `json:"attempt"`
		Issue struct {
			Status string `json:"status"`
		} `json:"issue"`
	}
	decodeIntegrationResult(t, finished, &completion)
	if finished.IsError || completion.Attempt.Status != "completed" || completion.Issue.Status != "done" {
		t.Fatalf("finish_attempt result = %#v, decoded = %#v", finished, completion)
	}

	var requestStatus string
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT status FROM review_requests WHERE id = ?`, reviewRequest.ID).Scan(&requestStatus)
	}); err != nil {
		t.Fatalf("read review request status: %v", err)
	}
	if requestStatus != string(domain.ReviewRequestStatusApproved) {
		t.Fatalf("review request status = %q, want approved", requestStatus)
	}
}

func TestIntegrationReviewWorkflowReReview(t *testing.T) {
	env := newIntegrationEnvironment(t)
	session := env.connect(t)

	created := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type":                  "bug",
		"title":                 "Review re-review integration",
		"description":           "Exercise re-review completion through the MCP transport.",
		"status":                "review",
		"labels":                []string{"integration"},
		"create_missing_labels": true,
	})
	var issue struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeIntegrationResult(t, created, &issue)
	if created.IsError || issue.ID == "" || issue.DisplayID == "" {
		t.Fatalf("create_issue result = %#v, decoded = %#v", created, issue)
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

	claim := callIntegrationTool(t, session, "claim_issue", map[string]any{"issue_id": issue.DisplayID, "lease_seconds": 60})
	var firstClaim struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeIntegrationResult(t, claim, &firstClaim)
	if claim.IsError || firstClaim.Attempt.ID == "" || firstClaim.LeaseToken == "" {
		t.Fatalf("claim_issue result = %#v, decoded = %#v", claim, firstClaim)
	}

	var latestEventID int64
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM issue_events`).Scan(&latestEventID)
	}); err != nil {
		t.Fatalf("read latest issue event id: %v", err)
	}

	requested := callIntegrationTool(t, session, "create_review_request", map[string]any{
		"issue_id":             issue.DisplayID,
		"target_issue_version": 1,
		"target_event_id":      latestEventID,
		"artifact_ids":         []string{"artifact-1"},
	})
	var initialRequest struct {
		ID string `json:"id"`
	}
	decodeIntegrationResult(t, requested, &initialRequest)
	if requested.IsError || initialRequest.ID == "" {
		t.Fatalf("create_review_request result = %#v, decoded = %#v", requested, initialRequest)
	}

	reviewRepository, err := sqlite.NewReviewRepository(db)
	if err != nil {
		t.Fatalf("new review repository: %v", err)
	}
	if _, err := reviewRepository.ClaimReviewRequest(context.Background(), ports.ReviewMutationCommand{
		RequestID:       initialRequest.ID,
		ExpectedVersion: 1,
		ActiveAttemptID: &firstClaim.Attempt.ID,
		OccurredAt:      time.Now().UTC().Add(-time.Second),
	}); err != nil {
		t.Fatalf("claim review request: %v", err)
	}

	finished := callIntegrationTool(t, session, "finish_attempt", map[string]any{
		"attempt_id":     firstClaim.Attempt.ID,
		"lease_token":    firstClaim.LeaseToken,
		"outcome":        "completed",
		"result_summary": "Review requested follow-up changes.",
		"review_outcome": "changes_requested",
		"verification":   []string{"go test -tags=integration ."},
	})
	var firstCompletion struct {
		Issue struct {
			Status string `json:"status"`
		} `json:"issue"`
	}
	decodeIntegrationResult(t, finished, &firstCompletion)
	if finished.IsError || firstCompletion.Issue.Status != "ready" {
		t.Fatalf("finish_attempt changes requested result = %#v, decoded = %#v", finished, firstCompletion)
	}

	var issueVersion int64
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT version FROM issues WHERE id = ?`, issue.ID).Scan(&issueVersion)
	}); err != nil {
		t.Fatalf("read issue version before re-review: %v", err)
	}
	updated := callIntegrationTool(t, session, "update_issue", map[string]any{
		"issue_id":         issue.DisplayID,
		"expected_version": issueVersion,
		"changes":          map[string]any{"status": "review"},
	})
	var updatedIssue struct {
		Issue struct {
			Version int64 `json:"version"`
		} `json:"issue"`
	}
	decodeIntegrationResult(t, updated, &updatedIssue)
	if updated.IsError || updatedIssue.Issue.Version == 0 {
		t.Fatalf("update_issue result = %#v, decoded = %#v", updated, updatedIssue)
	}

	secondClaim := callIntegrationTool(t, session, "claim_issue", map[string]any{"issue_id": issue.DisplayID, "lease_seconds": 60})
	var secondClaimOutput struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeIntegrationResult(t, secondClaim, &secondClaimOutput)
	if secondClaim.IsError || secondClaimOutput.Attempt.ID == "" || secondClaimOutput.LeaseToken == "" {
		t.Fatalf("second claim_issue result = %#v, decoded = %#v", secondClaim, secondClaimOutput)
	}

	var latestEventIDAfterSecondClaim int64
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM issue_events`).Scan(&latestEventIDAfterSecondClaim)
	}); err != nil {
		t.Fatalf("read latest issue event id after second claim: %v", err)
	}

	requestedAgain := callIntegrationTool(t, session, "create_review_request", map[string]any{
		"issue_id":             issue.DisplayID,
		"target_issue_version": updatedIssue.Issue.Version,
		"target_event_id":      latestEventIDAfterSecondClaim,
		"artifact_ids":         []string{"artifact-2"},
	})
	var secondRequest struct {
		ID string `json:"id"`
	}
	decodeIntegrationResult(t, requestedAgain, &secondRequest)
	if requestedAgain.IsError || secondRequest.ID == "" {
		t.Fatalf("create second review request result = %#v, decoded = %#v", requestedAgain, secondRequest)
	}

	if _, err := reviewRepository.ClaimReviewRequest(context.Background(), ports.ReviewMutationCommand{
		RequestID:       secondRequest.ID,
		ExpectedVersion: 1,
		ActiveAttemptID: &secondClaimOutput.Attempt.ID,
		OccurredAt:      time.Now().UTC().Add(-time.Second),
	}); err != nil {
		t.Fatalf("claim second review request: %v", err)
	}

	completed := callIntegrationTool(t, session, "finish_attempt", map[string]any{
		"attempt_id":     secondClaimOutput.Attempt.ID,
		"lease_token":    secondClaimOutput.LeaseToken,
		"outcome":        "completed",
		"result_summary": "Review approved after re-review.",
		"review_outcome": "approved",
		"verification":   []string{"go test -tags=integration ."},
	})
	var secondCompletion struct {
		Issue struct {
			Status string `json:"status"`
		} `json:"issue"`
	}
	decodeIntegrationResult(t, completed, &secondCompletion)
	if completed.IsError || secondCompletion.Issue.Status != "done" {
		t.Fatalf("finish_attempt approved result = %#v, decoded = %#v", completed, secondCompletion)
	}

	var requestStatus string
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT status FROM review_requests WHERE id = ?`, secondRequest.ID).Scan(&requestStatus)
	}); err != nil {
		t.Fatalf("read second review request status: %v", err)
	}
	if requestStatus != string(domain.ReviewRequestStatusApproved) {
		t.Fatalf("second review request status = %q, want approved", requestStatus)
	}
}

func TestIntegrationLogicalProjectRoundTrip(t *testing.T) {
	sourceEnv := newIntegrationEnvironment(t)
	destEnv := newIntegrationEnvironment(t)
	session := sourceEnv.connect(t)

	createdEpic := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type":                  "epic",
		"title":                 "Round trip epic",
		"description":           "Create a representative logical interchange document.",
		"status":                "ready",
		"priority":              "high",
		"labels":                []string{"integration"},
		"create_missing_labels": true,
	})
	var epic struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeIntegrationResult(t, createdEpic, &epic)
	if createdEpic.IsError || epic.ID == "" || epic.DisplayID == "" {
		t.Fatalf("create_issue epic result = %#v, decoded = %#v", createdEpic, epic)
	}

	createdTask := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type":                  "task",
		"title":                 "Round trip task",
		"description":           "Exercise logical export/import around a terminal attempt.",
		"status":                "ready",
		"priority":              "medium",
		"parent_issue_id":       epic.DisplayID,
		"labels":                []string{"integration"},
		"create_missing_labels": true,
	})
	var task struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
	}
	decodeIntegrationResult(t, createdTask, &task)
	if createdTask.IsError || task.ID == "" || task.DisplayID == "" {
		t.Fatalf("create_issue task result = %#v, decoded = %#v", createdTask, task)
	}

	if result := callIntegrationTool(t, session, "manage_issue_relation", map[string]any{
		"action":          "add",
		"source_issue_id": epic.DisplayID,
		"target_issue_id": task.DisplayID,
		"relation_type":   "duplicates",
	}); result.IsError {
		t.Fatalf("manage_issue_relation result = %#v", result)
	}
	if result := callIntegrationTool(t, session, "add_comment", map[string]any{
		"issue_id": task.DisplayID,
		"content":  "Round-trip comment for logical interchange.",
	}); result.IsError {
		t.Fatalf("add_comment result = %#v", result)
	}
	if result := callIntegrationTool(t, session, "record_decision", map[string]any{
		"issue_id": task.DisplayID,
		"title":    "Record round-trip decision",
		"summary":  "The logical import/export workflow should preserve durable decisions.",
		"content":  "Round-trip test content.",
		"status":   "active",
	}); result.IsError {
		t.Fatalf("record_decision result = %#v", result)
	}

	claimed := callIntegrationTool(t, session, "claim_issue", map[string]any{
		"issue_id":      task.DisplayID,
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
		"content":     "Round-trip checkpoint with artifact.",
		"artifacts": []map[string]any{{
			"type": "file",
			"uri":  "docs/roundtrip.md",
			"metadata": map[string]any{
				"kind": "roundtrip",
			},
		}},
	})
	if note.IsError {
		t.Fatalf("save_attempt_note result = %#v", note)
	}

	finished := callIntegrationTool(t, session, "finish_attempt", map[string]any{
		"attempt_id":          claim.Attempt.ID,
		"lease_token":         claim.LeaseToken,
		"outcome":             "completed",
		"result_summary":      "The round-trip workflow passed.",
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
	}
	decodeIntegrationResult(t, finished, &completion)
	if finished.IsError || completion.Attempt.Status != "completed" || completion.Issue.Status != "done" {
		t.Fatalf("finish_attempt result = %#v, decoded = %#v", finished, completion)
	}

	sourceDocument := mustExportLogicalProjectDocument(t, sourceEnv)
	mustApplyLogicalProjectDocument(t, destEnv, sourceDocument)
	destDocument := mustExportLogicalProjectDocument(t, destEnv)

	sourceCanonical := canonicalizeLogicalProjectDocumentWithMappings(sourceDocument, buildCanonicalIDMappings(sourceDocument))
	destCanonical := canonicalizeLogicalProjectDocumentWithMappings(destDocument, mergeCanonicalIDMappings(buildCanonicalIDMappings(sourceDocument), buildCanonicalIDMappings(destDocument)))
	sourceCanonicalJSON := mustMarshalDocument(t, sourceCanonical)
	destCanonicalJSON := mustMarshalDocument(t, destCanonical)
	if sourceCanonicalJSON != destCanonicalJSON {
		t.Fatalf("round-trip logical content mismatch\nsource=%s\ndest=%s\nsource-canonical=%s\ndest-canonical=%s", mustMarshalDocument(t, sourceDocument), mustMarshalDocument(t, destDocument), sourceCanonicalJSON, destCanonicalJSON)
	}
}

func TestIntegrationHTTPTransportIsolatesSessions(t *testing.T) {
	env := newIntegrationEnvironment(t)
	ctx := context.Background()
	pathInputs := projectconfig.PathInputs{GOOS: runtime.GOOS, HomeDir: t.TempDir(), XDGDataHome: t.TempDir()}
	bundle, project, err := composeServices(ctx, env.repository, pathInputs, env.dataRoot)
	if err != nil {
		t.Fatalf("compose services: %v", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
		defer cancel()
		if closeErr := project.Close(closeCtx); closeErr != nil {
			t.Errorf("close project: %v", closeErr)
		}
	}()

	handler, err := newHTTPHandler(&config.Config{ServerName: "rhizome-http-test", Version: "test"}, bundle)
	if err != nil {
		t.Fatalf("create HTTP handler: %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	results := make(chan struct {
		clientName string
		result     map[string]any
		sessionID  string
		toolName   string
		err        error
	}, 2)
	var wg sync.WaitGroup
	for _, clientName := range []string{"client-a", "client-b"} {
		clientName := clientName
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, sessionID, err := communicateThroughHTTP(t, server.URL+"/mcp", clientName)
			if err != nil {
				results <- struct {
					clientName string
					result     map[string]any
					sessionID  string
					toolName   string
					err        error
				}{clientName: clientName, err: err}
				return
			}
			results <- struct {
				clientName string
				result     map[string]any
				sessionID  string
				toolName   string
				err        error
			}{clientName: clientName, result: result, sessionID: sessionID}
		}()
	}
	wg.Wait()
	close(results)

	var seen []struct {
		clientName string
		result     map[string]any
		sessionID  string
		toolName   string
		err        error
	}
	for item := range results {
		seen = append(seen, item)
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 client results, got %d", len(seen))
	}
	for _, item := range seen {
		if item.err != nil {
			t.Fatalf("client %s failed: %v", item.clientName, item.err)
		}
		if item.sessionID == "" {
			t.Fatalf("client %s did not receive a session ID", item.clientName)
		}
		if _, ok := item.result["project"]; !ok {
			t.Fatalf("client %s get_project result missing project payload: %#v", item.clientName, item.result)
		}
	}

	if _, _, err := communicateThroughHTTP(t, server.URL+"/mcp", "client-c"); err != nil {
		t.Fatalf("later HTTP connection failed: %v", err)
	}

	if err := assertDistinctHTTPAgentSessions(t, env.repository, env.dataRoot, 3); err != nil {
		t.Fatalf("assert HTTP agent sessions: %v", err)
	}
}

func TestIntegrationHTTPAdversarialRequestsAreRejected(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	defer listener.Close()

	handler := projectruntime.WrapHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), listener.Addr().String(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	server := &http.Server{Handler: handler}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()
	defer func() {
		if closeErr := server.Close(); closeErr != nil && closeErr != http.ErrServerClosed {
			t.Errorf("close loopback listener: %v", closeErr)
		}
		if err := <-serveDone; err != nil && err != http.ErrServerClosed {
			t.Errorf("serve loopback listener: %v", err)
		}
	}()

	client := &http.Client{Timeout: integrationTimeout}

	request, err := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+"/mcp", nil)
	if err != nil {
		t.Fatalf("construct request: %v", err)
	}
	request.Host = "example.com:8080"
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("send hostile host request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("host mismatch status = %d, want %d", response.StatusCode, http.StatusMisdirectedRequest)
	}

	request, err = http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+"/mcp", nil)
	if err != nil {
		t.Fatalf("construct request: %v", err)
	}
	request.Host = listener.Addr().String()
	request.Header.Set("Origin", "http://127.0.0.1:9999")
	response, err = client.Do(request)
	if err != nil {
		t.Fatalf("send hostile origin request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("origin mismatch status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}
}

func TestIntegrationHTTPServeEphemeralPortWorkflow(t *testing.T) {
	env := newIntegrationEnvironment(t)
	server := launchIntegrationHTTPServer(t, env, "127.0.0.1:0")
	t.Cleanup(func() { stopIntegrationHTTPServer(t, server) })

	endpoint := "http://" + server.waitForEndpoint(t) + "/mcp"
	if _, _, err := communicateThroughHTTP(t, endpoint, "http-client"); err != nil {
		t.Fatalf("HTTP workflow failed: %v\nstderr:\n%s", err, server.output.String())
	}
}

func TestIntegrationHTTPServeConcurrentClientsOnEphemeralPort(t *testing.T) {
	env := newIntegrationEnvironment(t)
	server := launchIntegrationHTTPServer(t, env, "127.0.0.1:0")
	t.Cleanup(func() { stopIntegrationHTTPServer(t, server) })

	endpoint := "http://" + server.waitForEndpoint(t) + "/mcp"
	results := make(chan error, 3)
	for _, clientName := range []string{"concurrent-a", "concurrent-b", "concurrent-c"} {
		clientName := clientName
		go func() {
			_, _, err := communicateThroughHTTP(t, endpoint, clientName)
			results <- err
		}()
	}
	for range 3 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent HTTP workflow failed: %v\nstderr:\n%s", err, server.output.String())
		}
	}

	if err := assertDistinctHTTPAgentSessions(t, env.repository, env.dataRoot, 3); err != nil {
		t.Fatalf("assert concurrent HTTP agent sessions: %v", err)
	}
}

func TestIntegrationHTTPServeStopsOnInterrupt(t *testing.T) {
	env := newIntegrationEnvironment(t)
	server := launchIntegrationHTTPServer(t, env, "127.0.0.1:0")
	endpoint := "http://" + server.waitForEndpoint(t) + "/mcp"
	if _, _, err := communicateThroughHTTP(t, endpoint, "shutdown-client"); err != nil {
		t.Fatalf("HTTP workflow failed before shutdown: %v\nstderr:\n%s", err, server.output.String())
	}

	stopIntegrationHTTPServer(t, server)

	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(endpoint)
	if err == nil {
		response.Body.Close()
		t.Fatalf("expected HTTP endpoint to be closed after shutdown")
	}
}

func TestIntegrationHTTPServeRejectsHostnameAddress(t *testing.T) {
	env := newIntegrationEnvironment(t)
	server := launchIntegrationHTTPServer(t, env, "localhost:0")
	if err := server.waitForExit(t); err == nil {
		t.Fatalf("expected hostname address to fail startup")
	}
	if stderr := server.output.String(); !strings.Contains(stderr, "invalid http address") {
		t.Fatalf("expected invalid address error in stderr, got %s", stderr)
	}
}

type integrationHTTPServer struct {
	cmd       *exec.Cmd
	output    *capturedOutput
	endpoint  string
	endpointC chan string
	doneC     chan error
}

type capturedOutput struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (output *capturedOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buf.Write(data)
}

func (output *capturedOutput) WriteString(value string) {
	output.mu.Lock()
	defer output.mu.Unlock()
	_, _ = output.buf.WriteString(value)
}

func (output *capturedOutput) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buf.String()
}

func launchIntegrationHTTPServer(t *testing.T, env integrationEnvironment, httpAddress string) *integrationHTTPServer {
	t.Helper()
	cmd := exec.Command(integrationBinary, "--data-root", env.dataRoot, "serve", "--http-address", httpAddress)
	cmd.Dir = env.repository

	stderrReader, stderrWriter := io.Pipe()
	output := &capturedOutput{}
	server := &integrationHTTPServer{
		cmd:       cmd,
		output:    output,
		endpointC: make(chan string, 1),
		doneC:     make(chan error, 1),
	}
	cmd.Stderr = stderrWriter

	if err := cmd.Start(); err != nil {
		t.Fatalf("start integration HTTP server: %v", err)
	}

	go func() {
		scanner := bufio.NewScanner(stderrReader)
		for scanner.Scan() {
			line := scanner.Text()
			output.WriteString(line + "\n")
			if endpoint := parseIntegrationHTTPListenerEndpoint(line); endpoint != "" {
				select {
				case server.endpointC <- endpoint:
				default:
				}
				return
			}
		}
		_ = scanner.Err()
		_ = stderrReader.Close()
	}()
	go func() {
		err := cmd.Wait()
		_ = stderrWriter.Close()
		server.doneC <- err
	}()
	return server
}

func (server *integrationHTTPServer) waitForEndpoint(t *testing.T) string {
	t.Helper()
	deadline := time.NewTimer(integrationTimeout)
	defer deadline.Stop()
	for {
		select {
		case endpoint := <-server.endpointC:
			server.endpoint = endpoint
			return endpoint
		case err := <-server.doneC:
			t.Fatalf("integration HTTP server exited before listening: %v\nstderr:\n%s", err, server.output.String())
		case <-deadline.C:
			t.Fatalf("timed out waiting for integration HTTP server endpoint\nstderr:\n%s", server.output.String())
		}
	}
}

func (server *integrationHTTPServer) waitForExit(t *testing.T) error {
	t.Helper()
	select {
	case err := <-server.doneC:
		return err
	case <-time.After(integrationTimeout):
		t.Fatalf("timed out waiting for integration HTTP server exit\nstderr:\n%s", server.output.String())
		return nil
	}
}

func stopIntegrationHTTPServer(t *testing.T, server *integrationHTTPServer) {
	t.Helper()
	if server == nil || server.cmd == nil || server.cmd.Process == nil {
		return
	}
	if server.cmd.ProcessState != nil {
		return
	}
	if err := server.cmd.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		_ = server.cmd.Process.Kill()
	}
	select {
	case err := <-server.doneC:
		_ = err
	case <-time.After(2 * time.Second):
		_ = server.cmd.Process.Kill()
		select {
		case err := <-server.doneC:
			_ = err
		case <-time.After(integrationTimeout):
		}
	}
}

func parseIntegrationHTTPListenerEndpoint(line string) string {
	prefix := "endpoint="
	start := strings.Index(line, prefix)
	if start < 0 {
		return ""
	}
	start += len(prefix)
	if start >= len(line) {
		return ""
	}
	value := line[start:]
	if strings.HasPrefix(value, `"`) {
		value = strings.TrimPrefix(value, `"`)
		end := strings.Index(value, `"`)
		if end < 0 {
			return ""
		}
		return value[:end]
	}
	end := strings.IndexAny(value, " \t")
	if end < 0 {
		return value
	}
	return value[:end]
}

func communicateThroughHTTP(t *testing.T, endpoint, clientName string) (map[string]any, string, error) {
	t.Helper()
	httpClient := &http.Client{Timeout: integrationTimeout}
	var sessionID string

	initializeResult, err := postJSONRPC(httpClient, endpoint, sessionID, 1, "initialize", map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": clientName, "version": "1.0"},
	})
	if err != nil {
		return nil, "", err
	}
	sessionID = initializeResult.sessionID
	if sessionID == "" {
		return nil, "", fmt.Errorf("initialize did not return a session ID")
	}
	if _, err := postNotification(httpClient, endpoint, sessionID, "notifications/initialized", map[string]any{}); err != nil {
		return nil, "", err
	}
	listToolsResult, err := postJSONRPC(httpClient, endpoint, sessionID, 2, "tools/list", map[string]any{})
	if err != nil {
		return nil, "", err
	}
	var toolsResponse struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(listToolsResult.result, &toolsResponse); err != nil {
		return nil, "", err
	}
	if len(toolsResponse.Tools) == 0 {
		return nil, "", fmt.Errorf("list_tools returned no tools")
	}
	getProjectResult, err := postJSONRPC(httpClient, endpoint, sessionID, 3, "tools/call", map[string]any{
		"name":      "get_project",
		"arguments": map[string]any{},
	})
	if err != nil {
		return nil, "", err
	}
	var getProjectPayload struct {
		StructuredContent map[string]any `json:"structuredContent"`
	}
	if err := json.Unmarshal(getProjectResult.result, &getProjectPayload); err != nil {
		return nil, "", err
	}
	if len(getProjectPayload.StructuredContent) == 0 {
		return nil, "", fmt.Errorf("get_project returned no structured content")
	}
	return getProjectPayload.StructuredContent, sessionID, nil
}

type jsonRPCEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	result    json.RawMessage
	sessionID string
}

func postJSONRPC(client *http.Client, endpoint, sessionID string, id any, method string, params any) (*jsonRPCResponse, error) {
	payload := jsonRPCRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}
	var envelope jsonRPCEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("rpc error: %s", envelope.Error.Message)
	}
	return &jsonRPCResponse{result: envelope.Result, sessionID: resp.Header.Get("Mcp-Session-Id")}, nil
}

func postNotification(client *http.Client, endpoint, sessionID, method string, params any) (*jsonRPCResponse, error) {
	payload := jsonRPCRequest{JSONRPC: "2.0", Method: method, Params: params}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}
	return &jsonRPCResponse{result: nil, sessionID: resp.Header.Get("Mcp-Session-Id")}, nil
}

func assertDistinctHTTPAgentSessions(t *testing.T, repositoryPath, dataRoot string, wantActive int) error {
	t.Helper()
	projectDB, err := sqlite.Open(context.Background(), mustProjectDatabasePath(t, integrationEnvironment{repository: repositoryPath, dataRoot: dataRoot}), sqlite.Options{})
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := projectDB.Close(context.Background()); closeErr != nil {
			t.Errorf("close project db: %v", closeErr)
		}
	}()
	var count int
	var distinctNames int
	err = projectDB.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_sessions WHERE ended_at IS NULL").Scan(&count); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, "SELECT COUNT(DISTINCT client_name) FROM agent_sessions WHERE ended_at IS NULL").Scan(&distinctNames)
	})
	if err != nil {
		return err
	}
	if count != wantActive {
		return fmt.Errorf("active agent sessions = %d, want %d", count, wantActive)
	}
	if distinctNames != wantActive {
		return fmt.Errorf("distinct client names = %d, want %d", distinctNames, wantActive)
	}
	return nil
}

func mustProjectDatabasePath(t *testing.T, env integrationEnvironment) string {
	t.Helper()
	project, err := projectconfig.Discover(env.repository)
	if err != nil {
		t.Fatalf("discover project identity: %v", err)
	}
	databasePath, err := projectconfig.ProjectDatabasePath(env.dataRoot, project.Identity.ProjectID)
	if err != nil {
		t.Fatalf("resolve project database path: %v", err)
	}
	return databasePath
}

func mustExportLogicalProjectDocument(t *testing.T, env integrationEnvironment) domain.LogicalProjectDocument {
	t.Helper()
	databasePath := mustProjectDatabasePath(t, env)
	db, err := sqlite.Open(context.Background(), databasePath, sqlite.Options{})
	if err != nil {
		t.Fatalf("open logical project database %s: %v", databasePath, err)
	}
	defer func() {
		if closeErr := db.Close(context.Background()); closeErr != nil {
			t.Errorf("close logical project database %s: %v", databasePath, closeErr)
		}
	}()
	projectRepository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("create project repository: %v", err)
	}
	document, err := projectRepository.ExportLogicalProject(context.Background())
	if err != nil {
		t.Fatalf("export logical project document: %v", err)
	}
	return document
}

func mustApplyLogicalProjectDocument(t *testing.T, env integrationEnvironment, document domain.LogicalProjectDocument) {
	t.Helper()
	databasePath := mustProjectDatabasePath(t, env)
	db, err := sqlite.Open(context.Background(), databasePath, sqlite.Options{})
	if err != nil {
		t.Fatalf("open logical project database %s: %v", databasePath, err)
	}
	defer func() {
		if closeErr := db.Close(context.Background()); closeErr != nil {
			t.Errorf("close logical project database %s: %v", databasePath, closeErr)
		}
	}()
	projectRepository, err := sqlite.NewProjectRepository(db)
	if err != nil {
		t.Fatalf("create project repository: %v", err)
	}
	data, err := domain.MarshalLogicalProjectDocument(document)
	if err != nil {
		t.Fatalf("marshal logical project document: %v", err)
	}
	plan, err := domain.ParseLogicalProjectImportPlan(data)
	if err != nil {
		t.Fatalf("parse logical project import plan: %v", err)
	}
	if _, err := projectRepository.ApplyLogicalProjectImport(context.Background(), plan); err != nil {
		t.Fatalf("apply logical project import: %v", err)
	}
}

func mustMarshalDocument(t *testing.T, document domain.LogicalProjectDocument) string {
	t.Helper()
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatalf("marshal logical project document: %v", err)
	}
	return string(data)
}

type canonicalIDMappings struct {
	issueIDs       map[string]string
	labelIDs       map[string]string
	relationIDs    map[string]string
	commentIDs     map[string]string
	decisionIDs    map[string]string
	attemptIDs     map[string]string
	attemptNoteIDs map[string]string
	artifactIDs    map[string]string
}

func buildCanonicalIDMappings(document domain.LogicalProjectDocument) canonicalIDMappings {
	mappings := canonicalIDMappings{
		issueIDs:       make(map[string]string, len(document.Issues)),
		labelIDs:       make(map[string]string, len(document.Labels)),
		relationIDs:    make(map[string]string, len(document.Relations)),
		commentIDs:     make(map[string]string, len(document.Comments)),
		decisionIDs:    make(map[string]string, len(document.Decisions)),
		attemptIDs:     make(map[string]string, len(document.Attempts)),
		attemptNoteIDs: make(map[string]string, len(document.AttemptNotes)),
		artifactIDs:    make(map[string]string, len(document.Artifacts)),
	}
	for index := range document.Issues {
		placeholder := fmt.Sprintf("issue-%d", index)
		mappings.issueIDs[document.Issues[index].ID] = placeholder
	}
	for index := range document.Labels {
		placeholder := fmt.Sprintf("label-%d", index)
		mappings.labelIDs[document.Labels[index].ID] = placeholder
	}
	for index := range document.Relations {
		placeholder := fmt.Sprintf("relation-%d", index)
		mappings.relationIDs[document.Relations[index].ID] = placeholder
	}
	for index := range document.Comments {
		placeholder := fmt.Sprintf("comment-%d", index)
		mappings.commentIDs[document.Comments[index].ID] = placeholder
	}
	for index := range document.Decisions {
		placeholder := fmt.Sprintf("decision-%d", index)
		mappings.decisionIDs[document.Decisions[index].ID] = placeholder
	}
	for index := range document.Attempts {
		placeholder := fmt.Sprintf("attempt-%d", index)
		mappings.attemptIDs[document.Attempts[index].ID] = placeholder
	}
	for index := range document.AttemptNotes {
		placeholder := fmt.Sprintf("attempt-note-%d", index)
		mappings.attemptNoteIDs[document.AttemptNotes[index].ID] = placeholder
	}
	for index := range document.Artifacts {
		placeholder := fmt.Sprintf("artifact-%d", index)
		mappings.artifactIDs[document.Artifacts[index].ID] = placeholder
	}
	return mappings
}

func normalizeEventPayload(payload json.RawMessage, issueIDs, relationIDs, commentIDs, decisionIDs, attemptIDs, attemptNoteIDs map[string]string) json.RawMessage {
	if len(payload) == 0 {
		return payload
	}
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return payload
	}
	var update func(any)
	update = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				switch key {
				case "parent_id", "source_issue_id", "target_issue_id", "issue_id":
					if str, ok := child.(string); ok {
						if placeholder, ok := issueIDs[str]; ok {
							typed[key] = placeholder
						}
					}
				case "relation_id":
					if str, ok := child.(string); ok {
						if placeholder, ok := relationIDs[str]; ok {
							typed[key] = placeholder
						}
					}
				case "comment_id":
					if str, ok := child.(string); ok {
						if placeholder, ok := commentIDs[str]; ok {
							typed[key] = placeholder
						}
					}
				case "decision_id":
					if str, ok := child.(string); ok {
						if placeholder, ok := decisionIDs[str]; ok {
							typed[key] = placeholder
						}
					}
				case "attempt_id":
					if str, ok := child.(string); ok {
						if placeholder, ok := attemptIDs[str]; ok {
							typed[key] = placeholder
						}
					}
				case "note_id":
					if str, ok := child.(string); ok {
						if placeholder, ok := attemptNoteIDs[str]; ok {
							typed[key] = placeholder
						}
					}
				}
				update(child)
			}
		case []any:
			for _, child := range typed {
				update(child)
			}
		}
	}
	update(decoded)
	data, err := json.Marshal(decoded)
	if err != nil {
		return payload
	}
	return data
}

func canonicalizeLogicalProjectDocument(document domain.LogicalProjectDocument) domain.LogicalProjectDocument {
	return canonicalizeLogicalProjectDocumentWithMappings(document, buildCanonicalIDMappings(document))
}

func mergeCanonicalIDMappings(sourceMappings, destinationMappings canonicalIDMappings) canonicalIDMappings {
	merged := destinationMappings
	for id, placeholder := range sourceMappings.issueIDs {
		merged.issueIDs[id] = placeholder
	}
	for id, placeholder := range sourceMappings.labelIDs {
		merged.labelIDs[id] = placeholder
	}
	for id, placeholder := range sourceMappings.relationIDs {
		merged.relationIDs[id] = placeholder
	}
	for id, placeholder := range sourceMappings.commentIDs {
		merged.commentIDs[id] = placeholder
	}
	for id, placeholder := range sourceMappings.decisionIDs {
		merged.decisionIDs[id] = placeholder
	}
	for id, placeholder := range sourceMappings.attemptIDs {
		merged.attemptIDs[id] = placeholder
	}
	for id, placeholder := range sourceMappings.attemptNoteIDs {
		merged.attemptNoteIDs[id] = placeholder
	}
	for id, placeholder := range sourceMappings.artifactIDs {
		merged.artifactIDs[id] = placeholder
	}
	return merged
}

func canonicalizeLogicalProjectDocumentWithMappings(document domain.LogicalProjectDocument, mappings canonicalIDMappings) domain.LogicalProjectDocument {
	normalized := document
	normalized.ExportedAt = ""
	normalized.Project.ID = ""

	issueIDs := make(map[string]string, len(normalized.Issues)+len(mappings.issueIDs))
	for id, placeholder := range mappings.issueIDs {
		issueIDs[id] = placeholder
	}
	for index := range normalized.Issues {
		issue := normalized.Issues[index]
		placeholder := fmt.Sprintf("issue-%d", index)
		if explicit, ok := mappings.issueIDs[issue.ID]; ok {
			placeholder = explicit
		}
		issueIDs[issue.ID] = placeholder
		issue.ID = placeholder
		if issue.ParentID != nil {
			parentPlaceholder := issueIDs[*issue.ParentID]
			issue.ParentID = &parentPlaceholder
		}
		issue.ClosedAt = nil
		normalized.Issues[index] = issue
	}

	labelIDs := make(map[string]string, len(normalized.Labels)+len(mappings.labelIDs))
	for id, placeholder := range mappings.labelIDs {
		labelIDs[id] = placeholder
	}
	for index := range normalized.Labels {
		label := normalized.Labels[index]
		placeholder := fmt.Sprintf("label-%d", index)
		if explicit, ok := mappings.labelIDs[label.ID]; ok {
			placeholder = explicit
		}
		labelIDs[label.ID] = placeholder
		label.ID = placeholder
		normalized.Labels[index] = label
	}

	for index := range normalized.IssueLabels {
		normalized.IssueLabels[index].IssueID = issueIDs[normalized.IssueLabels[index].IssueID]
		normalized.IssueLabels[index].LabelID = labelIDs[normalized.IssueLabels[index].LabelID]
	}
	sort.Slice(normalized.IssueLabels, func(i, j int) bool {
		if normalized.IssueLabels[i].IssueID == normalized.IssueLabels[j].IssueID {
			return normalized.IssueLabels[i].LabelID < normalized.IssueLabels[j].LabelID
		}
		return normalized.IssueLabels[i].IssueID < normalized.IssueLabels[j].IssueID
	})

	relationIDs := make(map[string]string, len(normalized.Relations)+len(mappings.relationIDs))
	for id, placeholder := range mappings.relationIDs {
		relationIDs[id] = placeholder
	}
	for index := range normalized.Relations {
		relation := normalized.Relations[index]
		placeholder := fmt.Sprintf("relation-%d", index)
		if explicit, ok := mappings.relationIDs[relation.ID]; ok {
			placeholder = explicit
		}
		relationIDs[relation.ID] = placeholder
		relation.ID = placeholder
		relation.SourceIssueID = issueIDs[relation.SourceIssueID]
		relation.TargetIssueID = issueIDs[relation.TargetIssueID]
		normalized.Relations[index] = relation
	}

	commentIDs := make(map[string]string, len(normalized.Comments)+len(mappings.commentIDs))
	for id, placeholder := range mappings.commentIDs {
		commentIDs[id] = placeholder
	}
	for index := range normalized.Comments {
		comment := normalized.Comments[index]
		placeholder := fmt.Sprintf("comment-%d", index)
		if explicit, ok := mappings.commentIDs[comment.ID]; ok {
			placeholder = explicit
		}
		commentIDs[comment.ID] = placeholder
		comment.ID = placeholder
		comment.IssueID = issueIDs[comment.IssueID]
		normalized.Comments[index] = comment
	}

	decisionIDs := make(map[string]string, len(normalized.Decisions)+len(mappings.decisionIDs))
	for id, placeholder := range mappings.decisionIDs {
		decisionIDs[id] = placeholder
	}
	for index := range normalized.Decisions {
		decision := normalized.Decisions[index]
		placeholder := fmt.Sprintf("decision-%d", index)
		if explicit, ok := mappings.decisionIDs[decision.ID]; ok {
			placeholder = explicit
		}
		decisionIDs[decision.ID] = placeholder
		decision.ID = placeholder
		if decision.IssueID != nil {
			issuePlaceholder := issueIDs[*decision.IssueID]
			decision.IssueID = &issuePlaceholder
		}
		if decision.SupersedesID != nil {
			supersedesPlaceholder := decisionIDs[*decision.SupersedesID]
			decision.SupersedesID = &supersedesPlaceholder
		}
		normalized.Decisions[index] = decision
	}

	attemptIDs := make(map[string]string, len(normalized.Attempts)+len(mappings.attemptIDs))
	for id, placeholder := range mappings.attemptIDs {
		attemptIDs[id] = placeholder
	}
	for index := range normalized.Attempts {
		attempt := normalized.Attempts[index]
		placeholder := fmt.Sprintf("attempt-%d", index)
		if explicit, ok := mappings.attemptIDs[attempt.ID]; ok {
			placeholder = explicit
		}
		attemptIDs[attempt.ID] = placeholder
		attempt.ID = placeholder
		attempt.IssueID = issueIDs[attempt.IssueID]
		normalized.Attempts[index] = attempt
	}

	attemptNoteIDs := make(map[string]string, len(normalized.AttemptNotes)+len(mappings.attemptNoteIDs))
	for id, placeholder := range mappings.attemptNoteIDs {
		attemptNoteIDs[id] = placeholder
	}
	for index := range normalized.AttemptNotes {
		note := normalized.AttemptNotes[index]
		placeholder := fmt.Sprintf("attempt-note-%d", index)
		if explicit, ok := mappings.attemptNoteIDs[note.ID]; ok {
			placeholder = explicit
		}
		attemptNoteIDs[note.ID] = placeholder
		note.ID = placeholder
		note.AttemptID = attemptIDs[note.AttemptID]
		normalized.AttemptNotes[index] = note
	}

	artifactIDs := make(map[string]string, len(normalized.Artifacts)+len(mappings.artifactIDs))
	for id, placeholder := range mappings.artifactIDs {
		artifactIDs[id] = placeholder
	}
	for index := range normalized.Artifacts {
		artifact := normalized.Artifacts[index]
		placeholder := fmt.Sprintf("artifact-%d", index)
		if explicit, ok := mappings.artifactIDs[artifact.ID]; ok {
			placeholder = explicit
		}
		artifactIDs[artifact.ID] = placeholder
		artifact.ID = placeholder
		artifact.IssueID = issueIDs[artifact.IssueID]
		if artifact.AttemptID != nil {
			attemptPlaceholder := attemptIDs[*artifact.AttemptID]
			artifact.AttemptID = &attemptPlaceholder
		}
		normalized.Artifacts[index] = artifact
	}

	for index := range normalized.Events {
		event := normalized.Events[index]
		event.SourceID = int64(index + 1)
		if event.IssueID != nil {
			issuePlaceholder := issueIDs[*event.IssueID]
			event.IssueID = &issuePlaceholder
		}
		if event.AttemptID != nil {
			attemptPlaceholder := attemptIDs[*event.AttemptID]
			event.AttemptID = &attemptPlaceholder
		}
		event.Payload = nil
		normalized.Events[index] = event
	}

	_ = artifactIDs
	return normalized
}

type integrationEnvironment struct {
	repository string
	dataRoot   string
}

func newIntegrationEnvironment(t *testing.T) integrationEnvironment {
	t.Helper()
	tempDir := t.TempDir()
	env := integrationEnvironment{
		repository: filepath.Join(tempDir, "repository"),
		dataRoot:   filepath.Join(tempDir, "data"),
	}
	if err := os.Mkdir(env.repository, 0o755); err != nil {
		t.Fatalf("create test repository: %v", err)
	}
	runIntegrationCommand(t, env, "--data-root", env.dataRoot, "init")
	return env
}

func (env integrationEnvironment) connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	command := exec.Command(integrationBinary, "--data-root", env.dataRoot, "serve")
	command.Dir = env.repository
	client := mcp.NewClient(&mcp.Implementation{Name: "rhizome-integration-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{
		Command:           command,
		TerminateDuration: integrationTimeout,
	}, nil)
	if err != nil {
		t.Fatalf("connect to MCP server: %v", err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close MCP session: %v", err)
		}
	})
	return session
}

func runIntegrationCommand(t *testing.T, env integrationEnvironment, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	command := exec.CommandContext(ctx, integrationBinary, args...)
	command.Dir = env.repository
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("%s failed: %v\nstdout:\n%s\nstderr:\n%s", command.String(), err, stdout.String(), stderr.String())
	}
	return stdout.Bytes()
}

func callIntegrationTool(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s protocol error: %v", name, err)
	}
	return result
}

func decodeIntegrationResult(t *testing.T, result *mcp.CallToolResult, destination any) {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured result: %v", err)
	}
	if err := json.Unmarshal(data, destination); err != nil {
		t.Fatalf("decode structured result %s: %v", data, err)
	}
}

func hasTool(tools []*mcp.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}
