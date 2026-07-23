//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

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
