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

	reviewRepository, err := sqlite.NewReviewRepository(db)
	if err != nil {
		t.Fatalf("new review repository: %v", err)
	}
	requested, err := reviewRepository.CreateReviewRequest(context.Background(), ports.CreateReviewRequestCommand{
		RequestID: newIntegrationULID(t), TargetID: newIntegrationULID(t),
		Purposes:           []string{"implementation"},
		IssueID:            issue.ID,
		TargetIssueVersion: 1,
		TargetEventID:      latestEventID,
		ArtifactIDs:        []string{"artifact-1"},
		OccurredAt:         time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create review request: %v", err)
	}
	var reviewRequest struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	reviewRequest.ID = requested.Request.ID
	reviewRequest.Status = string(requested.Request.Status)
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

func TestIntegrationReplaceReviewRequestWorkflow(t *testing.T) {
	env := newIntegrationEnvironment(t)
	session := env.connect(t)

	created := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type":                  "bug",
		"title":                 "Replace review request integration",
		"description":           "Exercise atomic review request replacement through the MCP transport.",
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

	reviewRepository, err := sqlite.NewReviewRepository(db)
	if err != nil {
		t.Fatalf("new review repository: %v", err)
	}
	// The target must match the issue's real version and event position: a
	// request whose target is already stale is rejected at creation
	// (ISSUE-188).
	predecessorVersion, predecessorEventID := currentReviewTarget(t, db, issue.ID)
	predecessorCreated, err := reviewRepository.CreateReviewRequest(context.Background(), ports.CreateReviewRequestCommand{
		RequestID: newIntegrationULID(t), TargetID: newIntegrationULID(t),
		Purposes:           []string{"implementation"},
		IssueID:            issue.ID,
		TargetIssueVersion: predecessorVersion,
		TargetEventID:      predecessorEventID,
		ArtifactIDs:        []string{"artifact-1"},
		OccurredAt:         time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create review request: %v", err)
	}
	var predecessor struct {
		ID                 string `json:"id"`
		Version            int64  `json:"version"`
		Status             string `json:"status"`
		TargetIssueVersion int64  `json:"target_issue_version"`
	}
	predecessor.ID = predecessorCreated.Request.ID
	predecessor.Version = predecessorCreated.Request.Version
	predecessor.Status = string(predecessorCreated.Request.Status)
	predecessor.TargetIssueVersion = predecessorCreated.Request.TargetIssueVersion

	successorVersion, successorEventID := advanceReviewedIssue(t, db, issue.ID)
	replaced := callIntegrationTool(t, session, "replace_review_request", map[string]any{
		"predecessor_request_id":       predecessor.ID,
		"predecessor_expected_version": predecessor.Version,
		"target_issue_version":         successorVersion,
		"target_event_id":              successorEventID,
		"artifact_ids":                 []string{"artifact-2"},
		"idempotency_key":              "integration-replace-1",
	})
	var replaceOutput struct {
		Predecessor struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"predecessor"`
		Successor struct {
			ID                 string `json:"id"`
			Status             string `json:"status"`
			TargetIssueVersion int64  `json:"target_issue_version"`
			SupersedesID       string `json:"supersedes_id"`
		} `json:"successor"`
		LatestEventID int64 `json:"latest_event_id"`
	}
	decodeIntegrationResult(t, replaced, &replaceOutput)
	if replaced.IsError || replaceOutput.Predecessor.Status != "superseded" || replaceOutput.Successor.Status != "open" ||
		replaceOutput.Successor.TargetIssueVersion != 2 || replaceOutput.Successor.SupersedesID != predecessor.ID ||
		replaceOutput.Successor.ID == predecessor.ID {
		t.Fatalf("replace_review_request result = %#v, decoded = %#v", replaced, replaceOutput)
	}

	// Repeating the same idempotency key replays the original result rather
	// than creating a second successor.
	replayed := callIntegrationTool(t, session, "replace_review_request", map[string]any{
		"predecessor_request_id":       predecessor.ID,
		"predecessor_expected_version": predecessor.Version,
		"target_issue_version":         successorVersion,
		"target_event_id":              successorEventID,
		"artifact_ids":                 []string{"artifact-2"},
		"idempotency_key":              "integration-replace-1",
	})
	var replayOutput struct {
		Successor struct {
			ID string `json:"id"`
		} `json:"successor"`
	}
	decodeIntegrationResult(t, replayed, &replayOutput)
	if replayed.IsError || replayOutput.Successor.ID != replaceOutput.Successor.ID {
		t.Fatalf("replayed replace_review_request result = %#v, decoded = %#v", replayed, replayOutput)
	}

	var successorCount int
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM review_requests WHERE supersedes_id = ?`, predecessor.ID).Scan(&successorCount)
	}); err != nil {
		t.Fatalf("read successor count: %v", err)
	}
	if successorCount != 1 {
		t.Fatalf("successor count = %d, want 1 (idempotency replay must not create a second successor)", successorCount)
	}

	// A claimed predecessor must be rejected without detaching its attempt.
	// claim_issue auto-binds the issue's own open review request to the new
	// attempt in the same transaction (ISSUE-189), so claiming the issue is
	// what claims the successor here -- a separate ClaimReviewRequest call
	// would race that auto-bind's own version bump (open v1 -> claimed v2).
	claimedIssue := callIntegrationTool(t, session, "claim_issue", map[string]any{"issue_id": issue.DisplayID, "lease_seconds": 60})
	var claim struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeIntegrationResult(t, claimedIssue, &claim)
	if claimedIssue.IsError || claim.Attempt.ID == "" {
		t.Fatalf("claim_issue result = %#v, decoded = %#v", claimedIssue, claim)
	}

	claimedSuccessor, err := reviewRepository.GetReviewRequest(context.Background(), replaceOutput.Successor.ID)
	if err != nil {
		t.Fatalf("read auto-claimed successor: %v", err)
	}
	if claimedSuccessor.Request.Status != domain.ReviewRequestStatusClaimed ||
		claimedSuccessor.Request.ActiveAttemptID == nil || *claimedSuccessor.Request.ActiveAttemptID != claim.Attempt.ID {
		t.Fatalf("successor was not auto-bound to the new attempt: %+v", claimedSuccessor.Request)
	}

	rejectedReplace := callIntegrationTool(t, session, "replace_review_request", map[string]any{
		"predecessor_request_id":       replaceOutput.Successor.ID,
		"predecessor_expected_version": claimedSuccessor.Request.Version,
		"target_issue_version":         3,
		"target_event_id":              0,
		"artifact_ids":                 []string{"artifact-4"},
		"idempotency_key":              "integration-replace-2",
	})
	if !rejectedReplace.IsError {
		t.Fatalf("replace of a claimed predecessor unexpectedly succeeded: %#v", rejectedReplace)
	}
	var rejection struct {
		Code string `json:"code"`
	}
	decodeIntegrationResult(t, rejectedReplace, &rejection)
	if rejection.Code != "REVIEW_REQUEST_CLAIMED" {
		t.Fatalf("rejected replace error code = %q, want REVIEW_REQUEST_CLAIMED (full result: %#v)", rejection.Code, rejectedReplace)
	}

	reloadedSuccessor, err := reviewRepository.GetReviewRequest(context.Background(), replaceOutput.Successor.ID)
	if err != nil {
		t.Fatalf("get successor review request: %v", err)
	}
	if reloadedSuccessor.Request.Status != domain.ReviewRequestStatusClaimed || reloadedSuccessor.Request.ActiveAttemptID == nil ||
		*reloadedSuccessor.Request.ActiveAttemptID != claim.Attempt.ID {
		t.Fatalf("claimed successor changed after rejected replace: %+v", reloadedSuccessor.Request)
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

	reviewRepository, err := sqlite.NewReviewRepository(db)
	if err != nil {
		t.Fatalf("new review repository: %v", err)
	}
	requested, err := reviewRepository.CreateReviewRequest(context.Background(), ports.CreateReviewRequestCommand{
		RequestID: newIntegrationULID(t), TargetID: newIntegrationULID(t),
		Purposes:           []string{"implementation"},
		IssueID:            issue.ID,
		TargetIssueVersion: 1,
		TargetEventID:      latestEventID,
		ArtifactIDs:        []string{"artifact-1"},
		OccurredAt:         time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create review request: %v", err)
	}
	var initialRequest struct {
		ID string `json:"id"`
	}
	initialRequest.ID = requested.Request.ID
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

	// Re-review is reached the same way the first review was (claim_issue /
	// finish_attempt's gated complete_work_to_review path, docs/02 §17.1) --
	// a direct update_issue{status: review} patch has been rejected with
	// INVALID_STATUS_TRANSITION since ISSUE-172 wired workflow gates in.
	toReviewClaim := callIntegrationTool(t, session, "claim_issue", map[string]any{"issue_id": issue.DisplayID, "lease_seconds": 60})
	var toReviewClaimOutput struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeIntegrationResult(t, toReviewClaim, &toReviewClaimOutput)
	if toReviewClaim.IsError || toReviewClaimOutput.Attempt.ID == "" || toReviewClaimOutput.LeaseToken == "" {
		t.Fatalf("claim_issue before re-review result = %#v, decoded = %#v", toReviewClaim, toReviewClaimOutput)
	}
	updated := callIntegrationTool(t, session, "finish_attempt", map[string]any{
		"attempt_id":          toReviewClaimOutput.Attempt.ID,
		"lease_token":         toReviewClaimOutput.LeaseToken,
		"outcome":             "completed",
		"result_summary":      "Ready for re-review.",
		"target_issue_status": "review",
	})
	var updatedIssue struct {
		Issue struct {
			Version int64 `json:"version"`
		} `json:"issue"`
	}
	decodeIntegrationResult(t, updated, &updatedIssue)
	if updated.IsError || updatedIssue.Issue.Version == 0 {
		t.Fatalf("finish_attempt to review result = %#v, decoded = %#v", updated, updatedIssue)
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

	requestedAgainRes, err := reviewRepository.CreateReviewRequest(context.Background(), ports.CreateReviewRequestCommand{
		RequestID: newIntegrationULID(t), TargetID: newIntegrationULID(t),
		Purposes:           []string{"implementation"},
		IssueID:            issue.ID,
		TargetIssueVersion: updatedIssue.Issue.Version,
		TargetEventID:      latestEventIDAfterSecondClaim,
		ArtifactIDs:        []string{"artifact-2"},
		OccurredAt:         time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create second review request: %v", err)
	}
	var secondRequest struct {
		ID string `json:"id"`
	}
	secondRequest.ID = requestedAgainRes.Request.ID

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

// currentReviewTarget reads the issue version and the client-visible event
// position (unfiltered MAX(id)) a caller would freeze into a review target.
func currentReviewTarget(t *testing.T, db *sqlite.DB, issueID string) (int64, int64) {
	t.Helper()
	var version, latestEventID int64
	if err := db.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT version FROM issues WHERE id = ?`, issueID).Scan(&version); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM issue_events`).Scan(&latestEventID)
	}); err != nil {
		t.Fatalf("read review target position: %v", err)
	}
	return version, latestEventID
}

// advanceReviewedIssue applies the state change a real implementation edit
// produces -- a version bump plus one ordinary issue event -- so the next
// review target freezes something genuinely new.
func advanceReviewedIssue(t *testing.T, db *sqlite.DB, issueID string) (int64, int64) {
	t.Helper()
	now := sqlite.FormatStorageTime(time.Now().UTC())
	if err := db.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		if _, err := tx.ExecContext(ctx, `UPDATE issues SET version = version + 1, updated_at = ? WHERE id = ?`, now, issueID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_type, session_id, attempt_id, payload, created_at)
			VALUES (?, 'issue_updated', NULL, NULL, '{}', ?)`, issueID, now)
		return err
	}); err != nil {
		t.Fatalf("advance reviewed issue: %v", err)
	}
	return currentReviewTarget(t, db, issueID)
}
