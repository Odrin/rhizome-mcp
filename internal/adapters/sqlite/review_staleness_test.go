package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

// ISSUE-188: staleness used to be detected only at review completion, so a
// request could be born stale, be advertised as claimable, consume a
// reviewer's attempt, and only then fail. These tests pin the three earlier
// enforcement points -- create/replace, claim, and the release path -- plus
// the claimability projection they feed.

func TestCreateReviewRequestRejectsTargetThatDoesNotMatchTheIssue(t *testing.T) {
	fixture := newReviewFixture(t, "review-create-stale")
	defer fixture.close()

	issueID := fixture.insertIssue(t, "create stale target")

	for _, testCase := range []struct {
		name          string
		targetVersion int64
		mutate        func()
	}{
		{name: "wrong_target_issue_version", targetVersion: 2},
		{
			name:          "work_changed_after_target_event",
			targetVersion: 1,
			mutate: func() {
				recordReviewedIssueEvent(t, fixture.ctx, fixture.db, issueID, time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC))
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.mutate != nil {
				testCase.mutate()
			}
			_, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
				Purposes:           []string{"implementation"},
				RequestID:          fixture.newID(t),
				TargetID:           fixture.newID(t),
				IssueID:            issueID,
				TargetIssueVersion: testCase.targetVersion,
				TargetEventID:      0,
				OccurredAt:         time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
			})
			if !errors.Is(err, &domain.Error{Code: domain.CodeReviewTargetStale}) {
				t.Fatalf("CreateReviewRequest() error = %v, want STALE_REVIEW_TARGET", err)
			}
			assertReviewRequestCount(t, fixture.ctx, fixture.db, issueID, 0)
		})
	}
}

func TestReplaceReviewRequestRejectsStaleSuccessorTarget(t *testing.T) {
	fixture := newReviewFixture(t, "review-replace-stale")
	defer fixture.close()

	issueID := fixture.insertIssue(t, "replace stale target")
	created, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		Purposes:           []string{"implementation"},
		RequestID:          fixture.newID(t),
		TargetID:           fixture.newID(t),
		IssueID:            issueID,
		TargetIssueVersion: 1,
		TargetEventID:      0,
		OccurredAt:         time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("CreateReviewRequest() error = %v", err)
	}

	// The successor names a version the issue never reached.
	_, err = fixture.repository.ReplaceReviewRequest(fixture.ctx, ports.ReplaceReviewRequestCommand{
		PredecessorRequestID: created.Request.ID, PredecessorExpectedVersion: created.Request.Version,
		SuccessorID: fixture.newID(t), SuccessorTargetID: fixture.newID(t),
		TargetIssueVersion: 7, TargetEventID: 0,
		OccurredAt: time.Date(2026, 7, 17, 12, 1, 0, 0, time.UTC),
	})
	if !errors.Is(err, &domain.Error{Code: domain.CodeReviewTargetStale}) {
		t.Fatalf("ReplaceReviewRequest() error = %v, want STALE_REVIEW_TARGET", err)
	}

	// The rejected replace wrote nothing: the predecessor is untouched.
	var status string
	var version int64
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT status, version FROM review_requests WHERE id = ?`, created.Request.ID).Scan(&status, &version)
	}); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.ReviewRequestStatusOpen) || version != created.Request.Version {
		t.Fatalf("predecessor after rejected replace = status %q version %d", status, version)
	}
	assertReviewRequestCount(t, fixture.ctx, fixture.db, issueID, 1)
}

func TestClaimReviewRequestSupersedesStaleTarget(t *testing.T) {
	fixture := newReviewFixture(t, "review-claim-stale")
	defer fixture.close()

	issueID := fixture.insertIssue(t, "claim stale target")
	attemptID := fixture.insertReviewAttempt(t, issueID)
	created, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		Purposes:           []string{"implementation"},
		RequestID:          fixture.newID(t),
		TargetID:           fixture.newID(t),
		IssueID:            issueID,
		TargetIssueVersion: 1,
		TargetEventID:      0,
		OccurredAt:         time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("CreateReviewRequest() error = %v", err)
	}

	// The issue is edited after the request was created but before anyone
	// claims it -- the case that used to hand a reviewer doomed work.
	setReviewedIssueVersion(t, fixture.ctx, fixture.db, issueID, 2)

	_, err = fixture.repository.ClaimReviewRequest(fixture.ctx, ports.ReviewMutationCommand{
		RequestID:       created.Request.ID,
		ExpectedVersion: created.Request.Version,
		ActiveAttemptID: &attemptID,
		OccurredAt:      time.Date(2026, 7, 17, 12, 1, 0, 0, time.UTC),
	})
	if !errors.Is(err, &domain.Error{Code: domain.CodeReviewTargetStale}) {
		t.Fatalf("ClaimReviewRequest() error = %v, want STALE_REVIEW_TARGET", err)
	}

	var status string
	var activeAttemptID sql.NullString
	var superseded int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT status, active_attempt_id FROM review_requests WHERE id = ?`,
			created.Request.ID).Scan(&status, &activeAttemptID); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events WHERE issue_id = ? AND event_type = 'review_superseded'`,
			issueID).Scan(&superseded)
	}); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.ReviewRequestStatusSuperseded) || activeAttemptID.Valid {
		t.Fatalf("stale claim left request = status %q active_attempt_id %#v", status, activeAttemptID)
	}
	if superseded != 1 {
		t.Fatalf("review_superseded event count = %d, want 1", superseded)
	}

	// The supersede is durable even though the call reported an error.
	got, err := fixture.repository.GetReviewRequest(fixture.ctx, created.Request.ID)
	if err != nil {
		t.Fatalf("GetReviewRequest() error = %v", err)
	}
	if got.Request.Status != domain.ReviewRequestStatusSuperseded || !got.TargetStale {
		t.Fatalf("request after stale claim = %+v (stale %v)", got.Request, got.TargetStale)
	}
}

func TestGetAndListReportStaleTargetsAsNotClaimable(t *testing.T) {
	fixture := newReviewFixture(t, "review-stale-projection")
	defer fixture.close()

	issueID := fixture.insertIssue(t, "stale projection")
	created, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		Purposes:           []string{"implementation"},
		RequestID:          fixture.newID(t),
		TargetID:           fixture.newID(t),
		IssueID:            issueID,
		TargetIssueVersion: 1,
		TargetEventID:      0,
		OccurredAt:         time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("CreateReviewRequest() error = %v", err)
	}

	fresh, err := fixture.repository.GetReviewRequest(fixture.ctx, created.Request.ID)
	if err != nil {
		t.Fatalf("GetReviewRequest() error = %v", err)
	}
	if fresh.TargetStale {
		t.Fatalf("freshly created request reported a stale target: %+v", fresh.Request)
	}
	freshList, err := fixture.repository.ListReviewRequests(fixture.ctx, ports.ListReviewRequestsQuery{Limit: 20})
	if err != nil {
		t.Fatalf("ListReviewRequests() error = %v", err)
	}
	if freshList.StaleTargets[created.Request.ID] {
		t.Fatalf("freshly created request listed as stale: %+v", freshList.StaleTargets)
	}

	recordReviewedIssueEvent(t, fixture.ctx, fixture.db, issueID, time.Date(2026, 7, 17, 13, 0, 0, 0, time.UTC))

	stale, err := fixture.repository.GetReviewRequest(fixture.ctx, created.Request.ID)
	if err != nil {
		t.Fatalf("GetReviewRequest() error = %v", err)
	}
	if stale.Request.Status != domain.ReviewRequestStatusOpen || !stale.TargetStale {
		t.Fatalf("request after the issue changed = %+v (stale %v)", stale.Request, stale.TargetStale)
	}
	staleList, err := fixture.repository.ListReviewRequests(fixture.ctx, ports.ListReviewRequestsQuery{Limit: 20})
	if err != nil {
		t.Fatalf("ListReviewRequests() error = %v", err)
	}
	if !staleList.StaleTargets[created.Request.ID] {
		t.Fatalf("request after the issue changed not listed as stale: %+v", staleList.StaleTargets)
	}
}

func TestReviewAttemptExpirySupersedesStaleRequestInsteadOfReopening(t *testing.T) {
	fixture := newAttemptTestFixture(t, "review-expiry-stale")
	defer fixture.close()

	issue := createAttemptIssue(t, fixture, "review expiry stale", domain.StatusReview)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}

	reviewRepository, err := sqlite.NewReviewRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	created, err := reviewRepository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		Purposes:           []string{"implementation"},
		RequestID:          fixture.newID(t),
		TargetID:           fixture.newID(t),
		IssueID:            issue.ID,
		TargetIssueVersion: issue.Issue.Version,
		TargetEventID:      captureClientVisibleEventPosition(t, fixture),
		OccurredAt:         fixture.clock.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reviewRepository.ClaimReviewRequest(fixture.ctx, ports.ReviewMutationCommand{
		RequestID:       created.Request.ID,
		ExpectedVersion: created.Request.Version,
		ActiveAttemptID: &claim.Attempt.ID,
		OccurredAt:      fixture.clock.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	// The implementation moves on while the reviewer holds the lease, then
	// the lease lapses: docs/09 says such a request is superseded, not
	// returned to open for the next reviewer to waste an attempt on.
	recordImplementationChange(t, fixture, issue.ID)
	fixture.clock.Advance(2 * time.Hour)
	if _, err := fixture.attempts.ExpireAttempts(fixture.ctx); err != nil {
		t.Fatal(err)
	}

	var status string
	var activeAttemptID sql.NullString
	var resolvedAt sql.NullString
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT status, active_attempt_id, resolved_at FROM review_requests WHERE id = ?`,
			created.Request.ID).Scan(&status, &activeAttemptID, &resolvedAt)
	}); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.ReviewRequestStatusSuperseded) || activeAttemptID.Valid || !resolvedAt.Valid {
		t.Fatalf("expired stale request = status %q active_attempt_id %#v resolved_at %#v", status, activeAttemptID, resolvedAt)
	}
}

func TestClaimIssueDoesNotBindAStaleReviewRequest(t *testing.T) {
	fixture := newAttemptTestFixture(t, "review-bind-stale")
	defer fixture.close()

	issue := createAttemptIssue(t, fixture, "bind stale", domain.StatusReview)
	reviewRepository, err := sqlite.NewReviewRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	created, err := reviewRepository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		Purposes:           []string{"implementation"},
		RequestID:          fixture.newID(t),
		TargetID:           fixture.newID(t),
		IssueID:            issue.ID,
		TargetIssueVersion: issue.Issue.Version,
		TargetEventID:      captureClientVisibleEventPosition(t, fixture),
		OccurredAt:         fixture.clock.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	recordImplementationChange(t, fixture, issue.ID)

	if _, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID}); err != nil {
		t.Fatalf("ClaimIssue() error = %v", err)
	}

	var status string
	var activeAttemptID sql.NullString
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT status, active_attempt_id FROM review_requests WHERE id = ?`,
			created.Request.ID).Scan(&status, &activeAttemptID)
	}); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.ReviewRequestStatusOpen) || activeAttemptID.Valid {
		t.Fatalf("stale request after claim = status %q active_attempt_id %#v, want an untouched open request", status, activeAttemptID)
	}
}

// recordReviewedIssueEvent appends one ordinary issue event -- the kind an
// implementation change produces -- so a target frozen before the call is
// stale afterwards.
func recordReviewedIssueEvent(t *testing.T, ctx context.Context, db *sqlite.DB, issueID string, now time.Time) {
	t.Helper()
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_type, session_id, attempt_id, payload, created_at)
			VALUES (?, 'issue_updated', NULL, NULL, '{}', ?)`, issueID, sqlite.FormatStorageTime(now))
		return err
	}); err != nil {
		t.Fatalf("record reviewed issue event: %v", err)
	}
}

func assertReviewRequestCount(t *testing.T, ctx context.Context, db *sqlite.DB, issueID string, want int) {
	t.Helper()
	var got int
	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM review_requests WHERE issue_id = ?`, issueID).Scan(&got)
	}); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("review request rows for issue %s = %d, want %d", issueID, got, want)
	}
}
