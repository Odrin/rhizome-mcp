package sqlite_test

import (
	"context"
	"errors"
	"testing"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

// ISSUE-173: a review request must cover every purpose an active
// review_approval policy currently requires for its target, resolved and
// frozen at creation time (docs/02 §17.5/§17.6).
func TestCreateReviewRequestRejectsMissingRequiredPurpose(t *testing.T) {
	fixture := newAttemptTestFixture(t, "review-purpose-create-missing")
	defer fixture.close()
	createWorkflowPolicy(t, fixture, allTasksSelector(), []domain.PolicyRequirementInput{
		{Key: "security", Kind: domain.RequirementKindReviewApproval, Purpose: "security"},
	})
	issue := createAttemptIssue(t, fixture, "needs security purpose", domain.StatusReady)
	workClaim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	toReview := finishInput(workClaim, domain.AttemptOutcomeCompleted)
	toReview.TargetIssueStatus = statusPointer(domain.StatusReview)
	finished, err := fixture.attempts.FinishAttempt(fixture.ctx, toReview)
	if err != nil {
		t.Fatal(err)
	}

	reviewRepository, err := sqlite.NewReviewRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reviewRepository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		RequestID: fixture.newID(t), TargetID: fixture.newID(t),
		IssueID: issue.ID, TargetIssueVersion: finished.Issue.Version, TargetEventID: finished.LatestEventID,
		Purposes: []string{"implementation"}, OccurredAt: fixture.clock.Now(),
	})
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeReviewPurposeRequired {
		t.Fatalf("error = %v, want CodeReviewPurposeRequired", err)
	}
	if len(domainErr.Details) != 1 || domainErr.Details[0].Field != "purposes" {
		t.Fatalf("details = %+v, want one purposes detail naming security", domainErr.Details)
	}
}

// The end-to-end promise ISSUE-173 exists for: a review request whose
// purposes cover the target's frozen requirement re-evaluates against that
// target's own snapshot at approve_review (not the reviewing attempt's
// claim-time snapshot), grants one immutable review_approvals row per
// purpose it covers, and that approval later satisfies the SAME
// review_approval requirement at complete_work_to_done for a completely
// different, later work attempt on the same issue -- proving the live,
// issue-scoped lookup (not a snapshot) is what complete_work_to_done reads.
func TestApproveReviewGrantsApprovalUsableLaterAtCompleteWorkToDone(t *testing.T) {
	fixture := newAttemptTestFixture(t, "review-purpose-approve-grants")
	defer fixture.close()
	createWorkflowPolicy(t, fixture, allTasksSelector(), []domain.PolicyRequirementInput{
		{Key: "security", Kind: domain.RequirementKindReviewApproval, Purpose: "security"},
	})
	issue := createAttemptIssue(t, fixture, "security reviewed", domain.StatusReady)
	workClaim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	toReview := finishInput(workClaim, domain.AttemptOutcomeCompleted)
	toReview.TargetIssueStatus = statusPointer(domain.StatusReview)
	finished, err := fixture.attempts.FinishAttempt(fixture.ctx, toReview)
	if err != nil {
		t.Fatal(err)
	}

	reviewRepository, err := sqlite.NewReviewRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	created, err := reviewRepository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		RequestID: fixture.newID(t), TargetID: fixture.newID(t),
		IssueID: issue.ID, TargetIssueVersion: finished.Issue.Version, TargetEventID: finished.LatestEventID,
		Purposes: []string{"security"}, OccurredAt: fixture.clock.Now(),
	})
	if err != nil {
		t.Fatalf("CreateReviewRequest: %v", err)
	}

	reviewClaim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	if reviewClaim.Attempt.Kind != domain.AttemptKindReview {
		t.Fatalf("claimed attempt kind = %q, want review", reviewClaim.Attempt.Kind)
	}
	approved := domain.ReviewOutcomeApproved
	approveInput := finishInput(reviewClaim, domain.AttemptOutcomeCompleted)
	approveInput.ReviewOutcome = &approved
	approvedResult, err := fixture.attempts.FinishAttempt(fixture.ctx, approveInput)
	if err != nil {
		t.Fatalf("approve_review with a covering purpose must succeed via the target snapshot: %v", err)
	}
	if approvedResult.Issue.Status != domain.StatusDone {
		t.Fatalf("issue status = %q, want done", approvedResult.Issue.Status)
	}

	var approvalCount int
	var approvalIssueID, approvalTargetID, approvalRequestID, approvalAttemptID, approvalPurpose string
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM review_approvals WHERE issue_id = ?`, issue.ID).Scan(&approvalCount); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT issue_id, target_id, request_id, attempt_id, purpose FROM review_approvals WHERE issue_id = ?`, issue.ID).
			Scan(&approvalIssueID, &approvalTargetID, &approvalRequestID, &approvalAttemptID, &approvalPurpose)
	}); err != nil {
		t.Fatal(err)
	}
	if approvalCount != 1 {
		t.Fatalf("review_approvals rows = %d, want 1", approvalCount)
	}
	if approvalIssueID != issue.ID || approvalTargetID != created.Target.ID || approvalRequestID != created.Request.ID ||
		approvalAttemptID != reviewClaim.Attempt.ID || approvalPurpose != "security" {
		t.Fatalf("approval = (%q,%q,%q,%q,%q), want (%q,%q,%q,%q,security)",
			approvalIssueID, approvalTargetID, approvalRequestID, approvalAttemptID, approvalPurpose,
			issue.ID, created.Target.ID, created.Request.ID, reviewClaim.Attempt.ID)
	}
	// review_approvals is append-only: attempting to mutate or delete it must fail.
	if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `UPDATE review_approvals SET purpose = 'design' WHERE issue_id = ?`, issue.ID)
		return err
	}); err == nil {
		t.Fatal("UPDATE review_approvals succeeded; the immutability trigger did not fire")
	}

	// Reopen and complete a brand-new, unrelated work attempt straight to
	// done -- no review request this time -- proving complete_work_to_done
	// reads the live, issue-scoped approval, not any snapshot.
	reopened, err := fixture.issues.UpdateIssue(fixture.ctx, domain.UpdateIssueInput{
		IssueID: issue.ID, ExpectedVersion: approvedResult.Issue.Version,
		Changes: domain.IssuePatch{Status: domain.OptionalValue[domain.Status]{Set: true, Value: domain.StatusReady}},
	})
	if err != nil {
		t.Fatalf("reopen to ready: %v", err)
	}
	secondClaim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	secondFinish := finishInput(secondClaim, domain.AttemptOutcomeCompleted)
	secondFinish.TargetIssueStatus = statusPointer(domain.StatusDone)
	secondResult, err := fixture.attempts.FinishAttempt(fixture.ctx, secondFinish)
	if err != nil {
		t.Fatalf("complete_work_to_done must be satisfied by the earlier granted approval: %v", err)
	}
	if secondResult.Issue.Status != domain.StatusDone {
		t.Fatalf("second issue status = %q, want done", secondResult.Issue.Status)
	}
	_ = reopened

	// complete_work_to_done only reads approvals, it never grants them.
	var countAfter int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM review_approvals WHERE issue_id = ?`, issue.ID).Scan(&countAfter)
	}); err != nil {
		t.Fatal(err)
	}
	if countAfter != 1 {
		t.Fatalf("review_approvals rows after complete_work_to_done = %d, want still 1", countAfter)
	}
}

// The negative half of the live lookup: complete_work_to_done must still
// fail when no approval was ever granted for the issue.
func TestCompleteWorkToDoneRejectsReviewApprovalWithoutGrantedApproval(t *testing.T) {
	fixture := newAttemptTestFixture(t, "review-purpose-complete-done-unmet")
	defer fixture.close()
	createWorkflowPolicy(t, fixture, allTasksSelector(), []domain.PolicyRequirementInput{
		{Key: "security", Kind: domain.RequirementKindReviewApproval, Purpose: "security"},
	})
	issue := createAttemptIssue(t, fixture, "no approval yet", domain.StatusReady)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	toDone := finishInput(claim, domain.AttemptOutcomeCompleted)
	toDone.TargetIssueStatus = statusPointer(domain.StatusDone)
	_, err = fixture.attempts.FinishAttempt(fixture.ctx, toDone)
	assertWorkflowGateUnsatisfied(t, err)
}

// A replacement review request inherits the predecessor's purposes when it
// names none of its own; an explicit list on the successor is used instead
// and is still checked for required-purpose coverage.
func TestReplaceReviewRequestPurposeInheritanceAndCoverage(t *testing.T) {
	fixture := newAttemptTestFixture(t, "review-purpose-replace")
	defer fixture.close()
	createWorkflowPolicy(t, fixture, allTasksSelector(), []domain.PolicyRequirementInput{
		{Key: "security", Kind: domain.RequirementKindReviewApproval, Purpose: "security"},
	})
	issue := createAttemptIssue(t, fixture, "replace purposes", domain.StatusReady)
	workClaim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	toReview := finishInput(workClaim, domain.AttemptOutcomeCompleted)
	toReview.TargetIssueStatus = statusPointer(domain.StatusReview)
	finished, err := fixture.attempts.FinishAttempt(fixture.ctx, toReview)
	if err != nil {
		t.Fatal(err)
	}

	reviewRepository, err := sqlite.NewReviewRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	predecessor, err := reviewRepository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		RequestID: fixture.newID(t), TargetID: fixture.newID(t),
		IssueID: issue.ID, TargetIssueVersion: finished.Issue.Version, TargetEventID: finished.LatestEventID,
		Purposes: []string{"implementation", "security"}, OccurredAt: fixture.clock.Now(),
	})
	if err != nil {
		t.Fatalf("CreateReviewRequest: %v", err)
	}

	// Omitting purposes on replace inherits the predecessor's.
	inherited, err := reviewRepository.ReplaceReviewRequest(fixture.ctx, ports.ReplaceReviewRequestCommand{
		PredecessorRequestID: predecessor.Request.ID, PredecessorExpectedVersion: predecessor.Request.Version,
		SuccessorID: fixture.newID(t), SuccessorTargetID: fixture.newID(t),
		TargetIssueVersion: finished.Issue.Version + 1, TargetEventID: finished.LatestEventID + 1,
		OccurredAt: fixture.clock.Now(), IdempotencyKey: "replace-inherit", RequestHash: []byte("hash-inherit"),
	})
	if err != nil {
		t.Fatalf("ReplaceReviewRequest (inherit): %v", err)
	}
	if got := inherited.Successor.Purposes; len(got) != 2 || got[0] != "implementation" || got[1] != "security" {
		t.Fatalf("inherited successor purposes = %v, want [implementation security]", got)
	}

	// An explicit successor purposes list that drops the required purpose fails.
	_, err = reviewRepository.ReplaceReviewRequest(fixture.ctx, ports.ReplaceReviewRequestCommand{
		PredecessorRequestID: inherited.Successor.ID, PredecessorExpectedVersion: inherited.Successor.Version,
		SuccessorID: fixture.newID(t), SuccessorTargetID: fixture.newID(t),
		TargetIssueVersion: finished.Issue.Version + 2, TargetEventID: finished.LatestEventID + 2,
		Purposes:       []string{"implementation"},
		OccurredAt:     fixture.clock.Now(),
		IdempotencyKey: "replace-drop-required",
		RequestHash:    []byte("hash-drop"),
	})
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeReviewPurposeRequired {
		t.Fatalf("error = %v, want CodeReviewPurposeRequired", err)
	}
}
