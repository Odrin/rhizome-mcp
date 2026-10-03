package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
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

func TestReviewListBatchesPriorityOnlyAndSubstantiveFreshness(t *testing.T) {
	fixture := newReviewFixture(t, "review-batch-freshness")
	defer fixture.close()
	issueID := fixture.insertIssue(t, "batch freshness")
	created := sqlite.FormatStorageTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
		for i := 1; i <= 3; i++ {
			targetID, requestID := fmt.Sprintf("%026d", i+10), fmt.Sprintf("%026d", i+20)
			if _, err := tx.ExecContext(ctx, `INSERT INTO review_targets(id, issue_id, issue_version, latest_event_id, artifact_ids_json, purposes_json, version, created_at)
				VALUES (?, ?, ?, ?, '[]', '["implementation"]', 1, ?)`, targetID, issueID, i, i-1, created); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO review_requests(id, target_id, issue_id, target_issue_version, target_event_id, artifact_ids_json, purposes_json, status, version, created_at, resolved_at)
				VALUES (?, ?, ?, ?, ?, '[]', '["implementation"]', 'approved', 1, ?, ?)`,
				requestID, targetID, issueID, i, i-1, created, created); err != nil {
				return err
			}
		}
		for _, payload := range []string{`{"changed_fields":["priority"]}`, `{"changed_fields":["title"]}`} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_type, source, payload, created_at)
				VALUES (?, 'issue_updated', 'issue', ?, ?)`, issueID, payload, created); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE issues SET version=3 WHERE id=?`, issueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	list, err := fixture.repository.ListReviewRequests(fixture.ctx, ports.ListReviewRequestsQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 || list.StaleTargets[fmt.Sprintf("%026d", 23)] ||
		!list.StaleTargets[fmt.Sprintf("%026d", 22)] || len(list.StaleTargets) != 1 {
		t.Fatalf("first batch stale targets = %+v", list.StaleTargets)
	}
	remaining, err := fixture.repository.ListReviewRequests(fixture.ctx, ports.ListReviewRequestsQuery{
		Limit: 2, AfterCreatedAt: created, AfterID: fmt.Sprintf("%026d", 22),
	})
	if err != nil || len(remaining.Items) != 1 || !remaining.StaleTargets[fmt.Sprintf("%026d", 21)] {
		t.Fatalf("remaining batch: %+v, %v", remaining, err)
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

func TestReplaceReviewRequestAfterCommentGetsFreshTarget(t *testing.T) {
	fixture := newReviewFixture(t, "review-comment-replace")
	defer fixture.close()
	issueID := fixture.insertIssue(t, "comment changes review target")
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	created, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		Purposes: []string{"implementation"}, RequestID: fixture.newID(t), TargetID: fixture.newID(t),
		IssueID: issueID, TargetIssueVersion: 1, TargetEventID: currentEventPosition(t, fixture.ctx, fixture.db), OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	comments, err := sqlite.NewCommentRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := comments.AddComment(fixture.ctx, ports.AddCommentCommand{
		ID: fixture.newID(t), Input: domain.AddCommentInput{IssueID: issueID, Content: "Updated implementation evidence"}, OccurredAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	position := currentEventPosition(t, fixture.ctx, fixture.db)
	replaced, err := fixture.repository.ReplaceReviewRequest(fixture.ctx, ports.ReplaceReviewRequestCommand{
		PredecessorRequestID: created.Request.ID, PredecessorExpectedVersion: created.Request.Version,
		SuccessorID: fixture.newID(t), SuccessorTargetID: fixture.newID(t),
		TargetIssueVersion: 1, TargetEventID: position, OccurredAt: now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("replace event-only stale review: %v", err)
	}
	if replaced.Successor.TargetID == created.Target.ID || replaced.Successor.TargetIssueVersion != 1 || replaced.Successor.TargetEventID != position {
		t.Fatalf("successor did not capture a fresh same-version target: %#v", replaced)
	}
	var originalPosition, issueVersion int64
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT latest_event_id FROM review_targets WHERE id = ?`, created.Target.ID).Scan(&originalPosition); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT version FROM issues WHERE id = ?`, issueID).Scan(&issueVersion)
	}); err != nil {
		t.Fatal(err)
	}
	if originalPosition != created.Target.LatestEventID || issueVersion != 1 {
		t.Fatalf("historical target or issue mutated: position %d version %d", originalPosition, issueVersion)
	}
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

func TestCreateReviewRequestFutureEvent(t *testing.T) {
	t.Run("future_project_cursor_is_rejected_and_writes_nothing", func(t *testing.T) {
		fixture := newReviewFixture(t, "review-create-future-event")
		defer fixture.close()

		issueID := fixture.insertIssue(t, "future cursor create")
		current := currentEventPosition(t, fixture.ctx, fixture.db)
		_, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
			Purposes:           []string{"implementation"},
			RequestID:          fixture.newID(t),
			TargetID:           fixture.newID(t),
			IssueID:            issueID,
			TargetIssueVersion: 1,
			TargetEventID:      current + 1,
			OccurredAt:         time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
		})
		if !errors.Is(err, &domain.Error{Code: domain.CodeReviewTargetStale}) {
			t.Fatalf("CreateReviewRequest() error = %v, want STALE_REVIEW_TARGET", err)
		}
		assertReviewRequestCount(t, fixture.ctx, fixture.db, issueID, 0)
	})

	t.Run("current_project_cursor_is_valid_for_other_issue_activity", func(t *testing.T) {
		fixture := newReviewFixture(t, "review-create-global-cursor-other-issue")
		defer fixture.close()

		issueID := fixture.newID(t)
		if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at)
				VALUES (?, 1, 'task', ?, 'ready', 'medium', 1, ?, ?)`, issueID, "review target on current global cursor",
				sqlite.FormatStorageTime(time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)), sqlite.FormatStorageTime(time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		otherIssueID := fixture.newID(t)
		if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at)
				VALUES (?, 2, 'task', ?, 'ready', 'medium', 1, ?, ?)`, otherIssueID, "other issue activity",
				sqlite.FormatStorageTime(time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)), sqlite.FormatStorageTime(time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_type, session_id, attempt_id, payload, created_at)
				VALUES (?, 'issue_updated', NULL, NULL, '{}', ?)`, otherIssueID, sqlite.FormatStorageTime(time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC)))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		current := currentEventPosition(t, fixture.ctx, fixture.db)
		created, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
			Purposes:           []string{"implementation"},
			RequestID:          fixture.newID(t),
			TargetID:           fixture.newID(t),
			IssueID:            issueID,
			TargetIssueVersion: 1,
			TargetEventID:      current,
			OccurredAt:         time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("CreateReviewRequest() error = %v", err)
		}
		if created.Request.TargetEventID != current {
			t.Fatalf("created target_event_id = %d, want %d", created.Request.TargetEventID, current)
		}
	})

	t.Run("current_global_cursor_is_valid_for_excluded_event_types", func(t *testing.T) {
		fixture := newReviewFixture(t, "review-create-global-cursor-excluded-event")
		defer fixture.close()

		issueID := fixture.newID(t)
		if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at)
				VALUES (?, 1, 'task', ?, 'ready', 'medium', 1, ?, ?)`, issueID, "review target on excluded event",
				sqlite.FormatStorageTime(time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)), sqlite.FormatStorageTime(time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_type, session_id, attempt_id, payload, created_at)
				VALUES (?, 'attempt_started', NULL, NULL, '{}', ?)`, issueID, sqlite.FormatStorageTime(time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC)))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		current := currentEventPosition(t, fixture.ctx, fixture.db)
		created, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
			Purposes:           []string{"implementation"},
			RequestID:          fixture.newID(t),
			TargetID:           fixture.newID(t),
			IssueID:            issueID,
			TargetIssueVersion: 1,
			TargetEventID:      current,
			OccurredAt:         time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("CreateReviewRequest() error = %v", err)
		}
		if created.Request.TargetEventID != current {
			t.Fatalf("created target_event_id = %d, want %d", created.Request.TargetEventID, current)
		}
	})
}

func TestReplaceReviewRequestFutureEvent(t *testing.T) {
	t.Run("future_project_cursor_is_rejected_and_leaves_predecessor_open", func(t *testing.T) {
		fixture := newReviewFixture(t, "review-replace-future-event")
		defer fixture.close()

		issueID := fixture.insertIssue(t, "future cursor replace")
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
		current := currentEventPosition(t, fixture.ctx, fixture.db)
		_, err = fixture.repository.ReplaceReviewRequest(fixture.ctx, ports.ReplaceReviewRequestCommand{
			PredecessorRequestID:       created.Request.ID,
			PredecessorExpectedVersion: created.Request.Version,
			SuccessorID:                fixture.newID(t),
			SuccessorTargetID:          fixture.newID(t),
			TargetIssueVersion:         1,
			TargetEventID:              current + 1,
			OccurredAt:                 time.Date(2026, 7, 17, 12, 1, 0, 0, time.UTC),
		})
		if !errors.Is(err, &domain.Error{Code: domain.CodeReviewTargetStale}) {
			t.Fatalf("ReplaceReviewRequest() error = %v, want STALE_REVIEW_TARGET", err)
		}

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
	})

	t.Run("current_project_cursor_is_valid_for_other_issue_activity", func(t *testing.T) {
		fixture := newReviewFixture(t, "review-replace-global-cursor-other-issue")
		defer fixture.close()

		issueID := fixture.newID(t)
		if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at)
				VALUES (?, 1, 'task', ?, 'ready', 'medium', 1, ?, ?)`, issueID, "replace target on current global cursor",
				sqlite.FormatStorageTime(time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)), sqlite.FormatStorageTime(time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		otherIssueID := fixture.newID(t)
		if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at)
				VALUES (?, 2, 'task', ?, 'ready', 'medium', 1, ?, ?)`, otherIssueID, "other issue activity",
				sqlite.FormatStorageTime(time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)), sqlite.FormatStorageTime(time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_type, session_id, attempt_id, payload, created_at)
				VALUES (?, 'issue_updated', NULL, NULL, '{}', ?)`, otherIssueID, sqlite.FormatStorageTime(time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC)))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		current := currentEventPosition(t, fixture.ctx, fixture.db)
		created, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
			Purposes:           []string{"implementation"},
			RequestID:          fixture.newID(t),
			TargetID:           fixture.newID(t),
			IssueID:            issueID,
			TargetIssueVersion: 1,
			TargetEventID:      current,
			ArtifactIDs:        []string{},
			OccurredAt:         time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("CreateReviewRequest() error = %v", err)
		}
		successor, err := fixture.repository.ReplaceReviewRequest(fixture.ctx, ports.ReplaceReviewRequestCommand{
			PredecessorRequestID:       created.Request.ID,
			PredecessorExpectedVersion: created.Request.Version,
			SuccessorID:                fixture.newID(t),
			SuccessorTargetID:          fixture.newID(t),
			TargetIssueVersion:         1,
			TargetEventID:              current,
			ArtifactIDs:                []string{},
			OccurredAt:                 time.Date(2026, 7, 17, 12, 1, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("ReplaceReviewRequest() error = %v", err)
		}
		if successor.Successor.TargetEventID != current {
			t.Fatalf("successor target_event_id = %d, want %d", successor.Successor.TargetEventID, current)
		}
		if current <= 0 {
			t.Fatalf("project current cursor = %d, want > 0", current)
		}
	})

	t.Run("current_global_cursor_is_valid_for_excluded_event_types", func(t *testing.T) {
		fixture := newReviewFixture(t, "review-replace-global-cursor-excluded-event")
		defer fixture.close()

		issueID := fixture.newID(t)
		if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at)
				VALUES (?, 1, 'task', ?, 'ready', 'medium', 1, ?, ?)`, issueID, "replace target on excluded event",
				sqlite.FormatStorageTime(time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)), sqlite.FormatStorageTime(time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_type, session_id, attempt_id, payload, created_at)
				VALUES (?, 'attempt_started', NULL, NULL, '{}', ?)`, issueID, sqlite.FormatStorageTime(time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC)))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		current := currentEventPosition(t, fixture.ctx, fixture.db)
		created, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
			Purposes:           []string{"implementation"},
			RequestID:          fixture.newID(t),
			TargetID:           fixture.newID(t),
			IssueID:            issueID,
			TargetIssueVersion: 1,
			TargetEventID:      current,
			ArtifactIDs:        []string{},
			OccurredAt:         time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("CreateReviewRequest() error = %v", err)
		}
		successor, err := fixture.repository.ReplaceReviewRequest(fixture.ctx, ports.ReplaceReviewRequestCommand{
			PredecessorRequestID:       created.Request.ID,
			PredecessorExpectedVersion: created.Request.Version,
			SuccessorID:                fixture.newID(t),
			SuccessorTargetID:          fixture.newID(t),
			TargetIssueVersion:         1,
			TargetEventID:              current,
			ArtifactIDs:                []string{},
			OccurredAt:                 time.Date(2026, 7, 17, 12, 1, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("ReplaceReviewRequest() error = %v", err)
		}
		if successor.Successor.TargetEventID != current {
			t.Fatalf("successor target_event_id = %d, want %d", successor.Successor.TargetEventID, current)
		}
		if current <= 0 {
			t.Fatalf("project current cursor = %d, want > 0", current)
		}
	})
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

// ISSUE-229: docs/09 promises that a priority-only change does not invalidate
// a review target, but staleness treated every version mismatch as stale --
// and every update, priority included, increments issues.version. Re-ordering
// a queue therefore superseded every review in flight. These tests pin the
// exemption at each lifecycle point that asks the question, with the
// disqualifying control the exemption must not swallow.

// reprioritizeReviewedIssue applies a real priority-only update through the
// issue service, so the event payload's changed_fields is whatever production
// actually writes rather than a hand-built fixture.
func reprioritizeReviewedIssue(t *testing.T, fixture *attemptTestFixture, issueID string, version int64, priority domain.Priority) int64 {
	t.Helper()
	updated, err := fixture.issues.UpdateIssue(fixture.ctx, domain.UpdateIssueInput{
		IssueID: issueID, ExpectedVersion: version,
		Changes: domain.IssuePatch{Priority: domain.OptionalValue[domain.Priority]{Set: true, Value: priority}},
	})
	if err != nil {
		t.Fatalf("priority-only update: %v", err)
	}
	if updated.Issue.Version != version+1 {
		t.Fatalf("priority-only update left version %d, want %d; the fixture must exercise a real version bump", updated.Issue.Version, version+1)
	}
	return updated.Issue.Version
}

func openReviewRequestFor(t *testing.T, fixture *attemptTestFixture, issue application.CreateIssueResult) (*sqlite.ReviewRepository, string) {
	t.Helper()
	reviewRepository, err := sqlite.NewReviewRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	created, err := reviewRepository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		Purposes:  []string{"implementation"},
		RequestID: fixture.newID(t), TargetID: fixture.newID(t),
		IssueID: issue.ID, TargetIssueVersion: issue.Issue.Version,
		TargetEventID: captureClientVisibleEventPosition(t, fixture),
		OccurredAt:    fixture.clock.Now(),
	})
	if err != nil {
		t.Fatalf("CreateReviewRequest() error = %v", err)
	}
	return reviewRepository, created.Request.ID
}

func TestPriorityOnlyUpdateKeepsReviewTargetFreshAtGetAndList(t *testing.T) {
	fixture := newAttemptTestFixture(t, "priority-projection")
	defer fixture.close()

	issue := createAttemptIssue(t, fixture, "priority projection", domain.StatusReview)
	reviewRepository, requestID := openReviewRequestFor(t, fixture, issue)
	reprioritizeReviewedIssue(t, fixture, issue.ID, issue.Issue.Version, domain.PriorityCritical)

	got, err := reviewRepository.GetReviewRequest(fixture.ctx, requestID)
	if err != nil {
		t.Fatalf("GetReviewRequest() error = %v", err)
	}
	if got.TargetStale {
		t.Fatalf("request went stale after a priority-only update: %+v", got.Request)
	}
	listed, err := reviewRepository.ListReviewRequests(fixture.ctx, ports.ListReviewRequestsQuery{Limit: 20})
	if err != nil {
		t.Fatalf("ListReviewRequests() error = %v", err)
	}
	if listed.StaleTargets[requestID] {
		t.Fatalf("request listed as stale after a priority-only update: %+v", listed.StaleTargets)
	}

	// Repeated re-prioritisation stays fresh: the version gap is explained
	// one step per update, not tolerated as a fixed slack of one.
	version := issue.Issue.Version + 1
	for _, priority := range []domain.Priority{domain.PriorityLow, domain.PriorityHigh, domain.PriorityMedium} {
		version = reprioritizeReviewedIssue(t, fixture, issue.ID, version, priority)
	}
	afterMany, err := reviewRepository.GetReviewRequest(fixture.ctx, requestID)
	if err != nil {
		t.Fatalf("GetReviewRequest() error = %v", err)
	}
	if afterMany.TargetStale {
		t.Fatalf("request went stale after four priority-only updates: %+v", afterMany.Request)
	}
}

func TestPriorityOnlyUpdateKeepsReviewTargetFreshAtClaimReleaseAndCompletion(t *testing.T) {
	fixture := newAttemptTestFixture(t, "priority-lifecycle")
	defer fixture.close()

	issue := createAttemptIssue(t, fixture, "priority lifecycle", domain.StatusReview)
	reviewRepository, requestID := openReviewRequestFor(t, fixture, issue)
	reprioritizeReviewedIssue(t, fixture, issue.ID, issue.Issue.Version, domain.PriorityCritical)

	// Claim: an explicit claim of a stale request supersedes it and fails.
	reviewAttempt, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatalf("claim a review attempt: %v", err)
	}
	current, err := reviewRepository.GetReviewRequest(fixture.ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	// claim_issue's own binding already ran the same check; a stale request
	// would have been left open and unbound instead.
	if current.Request.Status != domain.ReviewRequestStatusClaimed {
		t.Fatalf("claim_issue did not bind the request after a priority-only update: %+v", current.Request)
	}

	// Release: an interrupted review attempt returns the request to open
	// rather than superseding it.
	input := finishInput(reviewAttempt, domain.AttemptOutcomeInterrupted)
	input.InterruptionReasonCode = interruptionPointer(domain.InterruptionReasonHandoff)
	if _, err := fixture.attempts.FinishAttempt(fixture.ctx, input); err != nil {
		t.Fatalf("interrupt the review attempt: %v", err)
	}
	released, err := reviewRepository.GetReviewRequest(fixture.ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if released.Request.Status != domain.ReviewRequestStatusOpen || released.TargetStale {
		t.Fatalf("released request = %+v (stale %v), want open and fresh", released.Request, released.TargetStale)
	}

	// Completion: one more priority-only update, then approve.
	issueVersion := issue.Issue.Version + 1
	reprioritizeReviewedIssue(t, fixture, issue.ID, issueVersion, domain.PriorityLow)
	reclaim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatalf("re-claim a review attempt: %v", err)
	}
	approve := finishInput(reclaim, domain.AttemptOutcomeCompleted)
	approve.ReviewOutcome = reviewPointer(domain.ReviewOutcomeApproved)
	if _, err := fixture.attempts.FinishAttempt(fixture.ctx, approve); err != nil {
		t.Fatalf("approve after priority-only updates: %v", err)
	}
	approved, err := reviewRepository.GetReviewRequest(fixture.ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Request.Status != domain.ReviewRequestStatusApproved {
		t.Fatalf("request after approval = %+v, want approved", approved.Request)
	}
}

// The control the exemption must not swallow: a title-only update is in the
// same warning class as priority (docs/02 §16) and still bumps the version by
// one, so nothing but the changed field itself distinguishes it.
func TestNonPriorityUpdateStillStalesReviewTarget(t *testing.T) {
	fixture := newAttemptTestFixture(t, "priority-control")
	defer fixture.close()

	issue := createAttemptIssue(t, fixture, "priority control", domain.StatusReview)
	reviewRepository, requestID := openReviewRequestFor(t, fixture, issue)
	if _, err := fixture.issues.UpdateIssue(fixture.ctx, domain.UpdateIssueInput{
		IssueID: issue.ID, ExpectedVersion: issue.Issue.Version,
		Changes: domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: "priority control, retitled"}},
	}); err != nil {
		t.Fatalf("title-only update: %v", err)
	}

	got, err := reviewRepository.GetReviewRequest(fixture.ctx, requestID)
	if err != nil {
		t.Fatalf("GetReviewRequest() error = %v", err)
	}
	if !got.TargetStale {
		t.Fatalf("request survived a title change: %+v", got.Request)
	}

	// And at completion: claim_issue leaves a stale request unbound, so the
	// approval is refused rather than silently resolving it.
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatalf("claim a review attempt: %v", err)
	}
	approve := finishInput(claim, domain.AttemptOutcomeCompleted)
	approve.ReviewOutcome = reviewPointer(domain.ReviewOutcomeApproved)
	if _, err := fixture.attempts.FinishAttempt(fixture.ctx, approve); err == nil {
		t.Fatal("approving against a title-changed target succeeded")
	}
}

// A priority change bundled with anything else is not a priority-only change:
// update_issue reports both fields, and the exemption must read the whole
// list rather than merely noticing that priority is in it.
func TestPriorityBundledWithAnotherChangeStillStalesReviewTarget(t *testing.T) {
	fixture := newAttemptTestFixture(t, "priority-bundled")
	defer fixture.close()

	issue := createAttemptIssue(t, fixture, "priority bundled", domain.StatusReview)
	reviewRepository, requestID := openReviewRequestFor(t, fixture, issue)
	if _, err := fixture.issues.UpdateIssue(fixture.ctx, domain.UpdateIssueInput{
		IssueID: issue.ID, ExpectedVersion: issue.Issue.Version,
		Changes: domain.IssuePatch{
			Priority:    domain.OptionalValue[domain.Priority]{Set: true, Value: domain.PriorityCritical},
			Description: domain.OptionalString{Set: true, Value: pointer("re-scoped while re-prioritised")},
		},
	}); err != nil {
		t.Fatalf("priority and description update: %v", err)
	}

	got, err := reviewRepository.GetReviewRequest(fixture.ctx, requestID)
	if err != nil {
		t.Fatalf("GetReviewRequest() error = %v", err)
	}
	if !got.TargetStale {
		t.Fatalf("request survived a description change bundled with the priority change: %+v", got.Request)
	}
}
