package sqlite_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/migrations"
	"rhizome-mcp/internal/ports"
)

func TestReviewRepositoryLifecycleCreatesEventsAndOutcome(t *testing.T) {
	fixture := newReviewFixture(t, "review-lifecycle")
	defer fixture.close()

	issueID := fixture.insertIssue(t, "review issue")
	attemptID := fixture.insertReviewAttempt(t, issueID)

	created, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		Purposes:           []string{"implementation"},
		RequestID:          fixture.newID(t),
		TargetID:           fixture.newID(t),
		IssueID:            issueID,
		TargetIssueVersion: 1,
		TargetEventID:      7,
		ArtifactIDs:        []string{"artifact-1", "artifact-2"},
		OccurredAt:         time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("CreateReviewRequest() error = %v", err)
	}
	if created.Request.Status != domain.ReviewRequestStatusOpen {
		t.Fatalf("created status = %q, want open", created.Request.Status)
	}

	claimed, err := fixture.repository.ClaimReviewRequest(fixture.ctx, ports.ReviewMutationCommand{
		RequestID:       created.Request.ID,
		ExpectedVersion: created.Request.Version,
		ActiveAttemptID: &attemptID,
		OccurredAt:      time.Date(2026, 7, 17, 12, 1, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ClaimReviewRequest() error = %v", err)
	}
	if claimed.Request.Status != domain.ReviewRequestStatusClaimed || claimed.Request.ActiveAttemptID == nil || *claimed.Request.ActiveAttemptID != attemptID {
		t.Fatalf("claimed request = %+v", claimed.Request)
	}

	resolved, err := fixture.repository.ResolveReviewRequest(fixture.ctx, ports.ResolveReviewRequestCommand{
		OutcomeID:       fixture.newID(t),
		RequestID:       claimed.Request.ID,
		ExpectedVersion: claimed.Request.Version,
		AttemptID:       attemptID,
		Outcome:         domain.ReviewOutcomeApproved,
		OccurredAt:      time.Date(2026, 7, 17, 12, 2, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ResolveReviewRequest() error = %v", err)
	}
	if resolved.Request.Status != domain.ReviewRequestStatusApproved {
		t.Fatalf("resolved status = %q, want approved", resolved.Request.Status)
	}
	if resolved.Outcome.Outcome != domain.ReviewOutcomeApproved {
		t.Fatalf("outcome = %+v, want approved", resolved.Outcome)
	}

	var count int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events WHERE source = 'review' AND json_extract(payload, '$.request_id') = ?`, created.Request.ID).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("review event count = %d, want 3", count)
	}
}

func TestReviewRepositorySupportsChangesRequestedFollowUpAndReReview(t *testing.T) {
	fixture := newReviewFixture(t, "review-follow-up")
	defer fixture.close()

	issueID := fixture.insertIssue(t, "follow-up review")
	attemptID := fixture.insertReviewAttempt(t, issueID)
	created, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		Purposes:           []string{"implementation"},
		RequestID:          fixture.newID(t),
		TargetID:           fixture.newID(t),
		IssueID:            issueID,
		TargetIssueVersion: 1,
		TargetEventID:      0,
		ArtifactIDs:        []string{"artifact-1"},
		OccurredAt:         time.Date(2026, 7, 17, 17, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := fixture.repository.ClaimReviewRequest(fixture.ctx, ports.ReviewMutationCommand{
		RequestID:       created.Request.ID,
		ExpectedVersion: created.Request.Version,
		ActiveAttemptID: &attemptID,
		OccurredAt:      time.Date(2026, 7, 17, 17, 1, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := fixture.repository.ResolveReviewRequest(fixture.ctx, ports.ResolveReviewRequestCommand{
		OutcomeID:       fixture.newID(t),
		RequestID:       claimed.Request.ID,
		ExpectedVersion: claimed.Request.Version,
		AttemptID:       attemptID,
		Outcome:         domain.ReviewOutcomeChangesRequested,
		OccurredAt:      time.Date(2026, 7, 17, 17, 2, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Request.Status != domain.ReviewRequestStatusChangesRequested {
		t.Fatalf("changes requested status = %q, want changes_requested", resolved.Request.Status)
	}
	if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO review_follow_ups(id, request_id, attempt_id, outcome, reason, version, created_at) VALUES (?, ?, ?, 'changes_requested', ?, 1, ?)`, "00000000000000000000000003", resolved.Request.ID, attemptID, "needs follow-up", sqlite.FormatStorageTime(time.Date(2026, 7, 17, 17, 3, 0, 0, time.UTC)))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	reviewed, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		Purposes:           []string{"implementation"},
		RequestID:          fixture.newID(t),
		TargetID:           fixture.newID(t),
		IssueID:            issueID,
		TargetIssueVersion: 2,
		TargetEventID:      5,
		ArtifactIDs:        []string{"artifact-2"},
		OccurredAt:         time.Date(2026, 7, 17, 17, 4, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if reviewed.Request.TargetIssueVersion != 2 || reviewed.Request.Status != domain.ReviewRequestStatusOpen {
		t.Fatalf("re-review request = %+v", reviewed.Request)
	}

	var followUpCount int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM review_follow_ups WHERE request_id = ?`, resolved.Request.ID).Scan(&followUpCount)
	}); err != nil {
		t.Fatal(err)
	}
	if followUpCount != 1 {
		t.Fatalf("follow-up count = %d, want 1", followUpCount)
	}
}

func TestReviewRepositoryBlockedOutcomeKeepsReason(t *testing.T) {
	fixture := newReviewFixture(t, "review-blocked")
	defer fixture.close()

	issueID := fixture.insertIssue(t, "blocked review")
	attemptID := fixture.insertReviewAttempt(t, issueID)
	created, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		Purposes:           []string{"implementation"},
		RequestID:          fixture.newID(t),
		TargetID:           fixture.newID(t),
		IssueID:            issueID,
		TargetIssueVersion: 1,
		TargetEventID:      0,
		ArtifactIDs:        []string{"artifact-1"},
		OccurredAt:         time.Date(2026, 7, 17, 18, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := fixture.repository.ClaimReviewRequest(fixture.ctx, ports.ReviewMutationCommand{
		RequestID:       created.Request.ID,
		ExpectedVersion: created.Request.Version,
		ActiveAttemptID: &attemptID,
		OccurredAt:      time.Date(2026, 7, 17, 18, 1, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := fixture.repository.ResolveReviewRequest(fixture.ctx, ports.ResolveReviewRequestCommand{
		OutcomeID:       fixture.newID(t),
		RequestID:       claimed.Request.ID,
		ExpectedVersion: claimed.Request.Version,
		AttemptID:       attemptID,
		Outcome:         domain.ReviewOutcomeBlocked,
		Reason:          reviewStringPtr("needs design"),
		OccurredAt:      time.Date(2026, 7, 17, 18, 2, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Request.Status != domain.ReviewRequestStatusBlocked {
		t.Fatalf("blocked status = %q, want blocked", resolved.Request.Status)
	}
	if resolved.Outcome.Reason == nil || *resolved.Outcome.Reason != "needs design" {
		t.Fatalf("blocked reason = %+v", resolved.Outcome.Reason)
	}
}

func TestReviewRepositoryCreateIsIdempotentForConcurrentDuplicates(t *testing.T) {
	fixture := newReviewFixture(t, "review-duplicates")
	defer fixture.close()

	issueID := fixture.insertIssue(t, "duplicate review")

	command := ports.CreateReviewRequestCommand{
		Purposes:           []string{"implementation"},
		RequestID:          fixture.newID(t),
		TargetID:           fixture.newID(t),
		IssueID:            issueID,
		TargetIssueVersion: 1,
		TargetEventID:      4,
		ArtifactIDs:        []string{"same-artifact"},
		OccurredAt:         time.Date(2026, 7, 17, 13, 0, 0, 0, time.UTC),
	}

	start := make(chan struct{})
	results := make(chan struct {
		result ports.CreateReviewRequestResult
		err    error
	}, 2)
	var group sync.WaitGroup
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, err := fixture.repository.CreateReviewRequest(context.Background(), command)
			results <- struct {
				result ports.CreateReviewRequestResult
				err    error
			}{result: result, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(results)

	var successes int
	var firstID string
	for outcome := range results {
		if outcome.err != nil {
			t.Fatalf("concurrent create error = %v", outcome.err)
		}
		successes++
		if firstID == "" {
			firstID = outcome.result.Request.ID
		} else if outcome.result.Request.ID != firstID {
			t.Fatalf("concurrent create produced mismatched request IDs %q and %q", firstID, outcome.result.Request.ID)
		}
	}
	if successes != 2 {
		t.Fatalf("concurrent create success count = %d, want 2", successes)
	}

	conflicting, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		Purposes:           []string{"implementation"},
		RequestID:          fixture.newID(t),
		TargetID:           fixture.newID(t),
		IssueID:            issueID,
		TargetIssueVersion: 1,
		TargetEventID:      4,
		ArtifactIDs:        []string{"different-artifact"},
		OccurredAt:         time.Date(2026, 7, 17, 13, 10, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatalf("conflicting create unexpectedly succeeded: %+v", conflicting)
	}
	if !errors.Is(err, &domain.Error{Code: domain.CodeReviewAlreadyExists}) {
		t.Fatalf("conflicting create error = %v, want REVIEW_ALREADY_EXISTS", err)
	}
}

func TestReviewRepositoryConcurrentClaimsHaveOneWinner(t *testing.T) {
	fixture := newReviewFixture(t, "review-claim-concurrency")
	defer fixture.close()

	issueID := fixture.insertIssue(t, "concurrent review claim")
	attemptID := fixture.insertReviewAttempt(t, issueID)
	created, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		Purposes:           []string{"implementation"},
		RequestID:          fixture.newID(t),
		TargetID:           fixture.newID(t),
		IssueID:            issueID,
		TargetIssueVersion: 1,
		TargetEventID:      0,
		ArtifactIDs:        []string{"artifact"},
		OccurredAt:         time.Date(2026, 7, 17, 15, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, err := fixture.repository.ClaimReviewRequest(fixture.ctx, ports.ReviewMutationCommand{
				RequestID:       created.Request.ID,
				ExpectedVersion: created.Request.Version,
				ActiveAttemptID: &attemptID,
				OccurredAt:      time.Date(2026, 7, 17, 15, 1, 0, 0, time.UTC),
			})
			results <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)

	var success, versionConflicts int
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, &domain.Error{Code: domain.CodeVersionConflict}):
			versionConflicts++
		default:
			t.Fatalf("concurrent claim error = %v", err)
		}
	}
	if success != 1 || versionConflicts != 1 {
		t.Fatalf("concurrent claim outcomes = success %d version_conflicts %d", success, versionConflicts)
	}
}

func TestReviewRepositoryVersionConflictRollsBackMutations(t *testing.T) {
	fixture := newReviewFixture(t, "review-version-conflict")
	defer fixture.close()

	issueID := fixture.insertIssue(t, "version conflict")
	attemptID := fixture.insertReviewAttempt(t, issueID)

	created, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		Purposes:           []string{"implementation"},
		RequestID:          fixture.newID(t),
		TargetID:           fixture.newID(t),
		IssueID:            issueID,
		TargetIssueVersion: 2,
		TargetEventID:      9,
		ArtifactIDs:        []string{"one"},
		OccurredAt:         time.Date(2026, 7, 17, 14, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.ClaimReviewRequest(fixture.ctx, ports.ReviewMutationCommand{
		RequestID:       created.Request.ID,
		ExpectedVersion: created.Request.Version,
		ActiveAttemptID: &attemptID,
		OccurredAt:      time.Date(2026, 7, 17, 14, 1, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.ClaimReviewRequest(fixture.ctx, ports.ReviewMutationCommand{
		RequestID:       created.Request.ID,
		ExpectedVersion: created.Request.Version,
		ActiveAttemptID: &attemptID,
		OccurredAt:      time.Date(2026, 7, 17, 14, 2, 0, 0, time.UTC),
	}); !errors.Is(err, &domain.Error{Code: domain.CodeVersionConflict}) {
		t.Fatalf("stale claim error = %v, want VERSION_CONFLICT", err)
	}

	var count int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events WHERE source = 'review' AND event_type = 'review_claimed' AND json_extract(payload, '$.request_id') = ?`, created.Request.ID).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("claim event count = %d, want 1", count)
	}
}

func TestReviewRepositoryReplaceSupersedesPredecessorAndCreatesSuccessor(t *testing.T) {
	fixture := newReviewFixture(t, "review-replace-success")
	defer fixture.close()

	issueID := fixture.insertIssue(t, "replace target issue")
	created, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		Purposes:  []string{"implementation"},
		RequestID: fixture.newID(t),
		TargetID:  fixture.newID(t),
		IssueID:   issueID, TargetIssueVersion: 1, TargetEventID: 3, ArtifactIDs: []string{"artifact-1"},
		OccurredAt: time.Date(2026, 7, 24, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}

	replaced, err := fixture.repository.ReplaceReviewRequest(fixture.ctx, ports.ReplaceReviewRequestCommand{
		SuccessorID:          fixture.newID(t),
		SuccessorTargetID:    fixture.newID(t),
		PredecessorRequestID: created.Request.ID, PredecessorExpectedVersion: created.Request.Version,
		TargetIssueVersion: 2, TargetEventID: 9, ArtifactIDs: []string{"artifact-2"},
		OccurredAt:     time.Date(2026, 7, 24, 9, 1, 0, 0, time.UTC),
		IdempotencyKey: "replace-once", RequestHash: []byte("hash-1"),
	})
	if err != nil {
		t.Fatalf("ReplaceReviewRequest() error = %v", err)
	}
	if replaced.Predecessor.Status != domain.ReviewRequestStatusSuperseded {
		t.Fatalf("predecessor status = %q, want superseded", replaced.Predecessor.Status)
	}
	if replaced.Successor.Status != domain.ReviewRequestStatusOpen || replaced.Successor.TargetIssueVersion != 2 ||
		replaced.Successor.SupersedesID == nil || *replaced.Successor.SupersedesID != created.Request.ID {
		t.Fatalf("successor = %+v", replaced.Successor)
	}
	if replaced.Successor.ID == created.Request.ID {
		t.Fatalf("successor reused predecessor ID")
	}

	// Predecessor is truly closed, not left claimable.
	predecessor, err := fixture.repository.GetReviewRequest(fixture.ctx, created.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if predecessor.Request.Status != domain.ReviewRequestStatusSuperseded {
		t.Fatalf("stored predecessor status = %q, want superseded", predecessor.Request.Status)
	}

	// One review_requested event from the original create, plus one
	// review_superseded (predecessor) and one review_requested (successor)
	// from the replace itself.
	var eventCount int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events WHERE source = 'review' AND json_extract(payload, '$.request_id') IN (?, ?)`, created.Request.ID, replaced.Successor.ID).Scan(&eventCount)
	}); err != nil {
		t.Fatal(err)
	}
	if eventCount != 3 {
		t.Fatalf("review event count = %d, want 3", eventCount)
	}

	// Repeating the same idempotency key replays the original result with no
	// new writes.
	replayed, err := fixture.repository.ReplaceReviewRequest(fixture.ctx, ports.ReplaceReviewRequestCommand{
		SuccessorID:          fixture.newID(t),
		SuccessorTargetID:    fixture.newID(t),
		PredecessorRequestID: created.Request.ID, PredecessorExpectedVersion: created.Request.Version,
		TargetIssueVersion: 2, TargetEventID: 9, ArtifactIDs: []string{"artifact-2"},
		OccurredAt:     time.Date(2026, 7, 24, 9, 2, 0, 0, time.UTC),
		IdempotencyKey: "replace-once", RequestHash: []byte("hash-1"),
	})
	if err != nil {
		t.Fatalf("replayed ReplaceReviewRequest() error = %v", err)
	}
	if replayed.Successor.ID != replaced.Successor.ID {
		t.Fatalf("replay produced a different successor: %+v", replayed.Successor)
	}
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events WHERE source = 'review' AND json_extract(payload, '$.request_id') IN (?, ?)`, created.Request.ID, replaced.Successor.ID).Scan(&eventCount)
	}); err != nil {
		t.Fatal(err)
	}
	if eventCount != 3 {
		t.Fatalf("review event count after replay = %d, want 3 (unchanged)", eventCount)
	}

	// A different request under the same key is a stable conflict, not silent overwrite.
	if _, err := fixture.repository.ReplaceReviewRequest(fixture.ctx, ports.ReplaceReviewRequestCommand{
		SuccessorID:          fixture.newID(t),
		SuccessorTargetID:    fixture.newID(t),
		PredecessorRequestID: created.Request.ID, PredecessorExpectedVersion: created.Request.Version,
		TargetIssueVersion: 2, TargetEventID: 9, ArtifactIDs: []string{"different-artifact"},
		OccurredAt:     time.Date(2026, 7, 24, 9, 3, 0, 0, time.UTC),
		IdempotencyKey: "replace-once", RequestHash: []byte("hash-2"),
	}); !errors.Is(err, &domain.Error{Code: domain.CodeIdempotencyConflict}) {
		t.Fatalf("conflicting idempotency key error = %v, want IDEMPOTENCY_CONFLICT", err)
	}
}

func TestReviewRepositoryReplaceRejectsClaimedPredecessorWithZeroWrites(t *testing.T) {
	fixture := newReviewFixture(t, "review-replace-claimed")
	defer fixture.close()

	issueID := fixture.insertIssue(t, "claimed predecessor issue")
	attemptID := fixture.insertReviewAttempt(t, issueID)
	created, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		Purposes:  []string{"implementation"},
		RequestID: fixture.newID(t),
		TargetID:  fixture.newID(t),
		IssueID:   issueID, TargetIssueVersion: 1, TargetEventID: 0, ArtifactIDs: []string{"artifact-1"},
		OccurredAt: time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := fixture.repository.ClaimReviewRequest(fixture.ctx, ports.ReviewMutationCommand{
		RequestID: created.Request.ID, ExpectedVersion: created.Request.Version, ActiveAttemptID: &attemptID,
		OccurredAt: time.Date(2026, 7, 24, 10, 1, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = fixture.repository.ReplaceReviewRequest(fixture.ctx, ports.ReplaceReviewRequestCommand{
		SuccessorID:          fixture.newID(t),
		SuccessorTargetID:    fixture.newID(t),
		PredecessorRequestID: claimed.Request.ID, PredecessorExpectedVersion: claimed.Request.Version,
		TargetIssueVersion: 2, TargetEventID: 5, ArtifactIDs: []string{"artifact-2"},
		OccurredAt: time.Date(2026, 7, 24, 10, 2, 0, 0, time.UTC),
	})
	if !errors.Is(err, &domain.Error{Code: domain.CodeReviewRequestClaimed}) {
		t.Fatalf("claimed predecessor replace error = %v, want REVIEW_REQUEST_CLAIMED", err)
	}

	reloaded, err := fixture.repository.GetReviewRequest(fixture.ctx, claimed.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Request.Status != domain.ReviewRequestStatusClaimed || reloaded.Request.Version != claimed.Request.Version ||
		reloaded.Request.ActiveAttemptID == nil || *reloaded.Request.ActiveAttemptID != attemptID {
		t.Fatalf("claimed predecessor changed after rejected replace: %+v", reloaded.Request)
	}
	var requestCount int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM review_requests WHERE issue_id = ?`, issueID).Scan(&requestCount)
	}); err != nil {
		t.Fatal(err)
	}
	if requestCount != 1 {
		t.Fatalf("review_requests count after rejected replace = %d, want 1 (no successor written)", requestCount)
	}
}

func TestReviewRepositoryReplaceRejectsTerminalPredecessor(t *testing.T) {
	fixture := newReviewFixture(t, "review-replace-terminal")
	defer fixture.close()

	issueID := fixture.insertIssue(t, "terminal predecessor issue")
	created, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		Purposes:  []string{"implementation"},
		RequestID: fixture.newID(t),
		TargetID:  fixture.newID(t),
		IssueID:   issueID, TargetIssueVersion: 1, TargetEventID: 0, ArtifactIDs: []string{"artifact-1"},
		OccurredAt: time.Date(2026, 7, 24, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := fixture.repository.CancelReviewRequest(fixture.ctx, ports.ReviewMutationCommand{
		RequestID: created.Request.ID, ExpectedVersion: created.Request.Version,
		OccurredAt: time.Date(2026, 7, 24, 11, 1, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = fixture.repository.ReplaceReviewRequest(fixture.ctx, ports.ReplaceReviewRequestCommand{
		SuccessorID:          fixture.newID(t),
		SuccessorTargetID:    fixture.newID(t),
		PredecessorRequestID: cancelled.Request.ID, PredecessorExpectedVersion: cancelled.Request.Version,
		TargetIssueVersion: 2, TargetEventID: 5, ArtifactIDs: []string{"artifact-2"},
		OccurredAt: time.Date(2026, 7, 24, 11, 2, 0, 0, time.UTC),
	})
	if !errors.Is(err, &domain.Error{Code: domain.CodeReviewRequestNotReplaceable}) {
		t.Fatalf("terminal predecessor replace error = %v, want REVIEW_REQUEST_NOT_REPLACEABLE", err)
	}
}

func TestReviewRepositoryReplaceVersionConflictRollsBackAllWrites(t *testing.T) {
	fixture := newReviewFixture(t, "review-replace-version-conflict")
	defer fixture.close()

	issueID := fixture.insertIssue(t, "version conflict predecessor")
	created, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		Purposes:  []string{"implementation"},
		RequestID: fixture.newID(t),
		TargetID:  fixture.newID(t),
		IssueID:   issueID, TargetIssueVersion: 1, TargetEventID: 0, ArtifactIDs: []string{"artifact-1"},
		OccurredAt: time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = fixture.repository.ReplaceReviewRequest(fixture.ctx, ports.ReplaceReviewRequestCommand{
		SuccessorID:          fixture.newID(t),
		SuccessorTargetID:    fixture.newID(t),
		PredecessorRequestID: created.Request.ID, PredecessorExpectedVersion: created.Request.Version + 1,
		TargetIssueVersion: 2, TargetEventID: 5, ArtifactIDs: []string{"artifact-2"},
		OccurredAt: time.Date(2026, 7, 24, 12, 1, 0, 0, time.UTC),
	})
	if !errors.Is(err, &domain.Error{Code: domain.CodeVersionConflict}) {
		t.Fatalf("stale version replace error = %v, want VERSION_CONFLICT", err)
	}

	var requestCount, eventCount int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx, `SELECT count(*) FROM review_requests WHERE issue_id = ?`, issueID).Scan(&requestCount); err != nil {
			return err
		}
		return query.QueryRowContext(ctx, `SELECT count(*) FROM issue_events WHERE source = 'review' AND json_extract(payload, '$.request_id') = ?`, created.Request.ID).Scan(&eventCount)
	}); err != nil {
		t.Fatal(err)
	}
	if requestCount != 1 {
		t.Fatalf("review_requests count after rolled-back replace = %d, want 1", requestCount)
	}
	if eventCount != 1 {
		t.Fatalf("review event count after rolled-back replace = %d, want 1 (only the original review_requested)", eventCount)
	}

	reloaded, err := fixture.repository.GetReviewRequest(fixture.ctx, created.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Request.Status != domain.ReviewRequestStatusOpen || reloaded.Request.Version != created.Request.Version {
		t.Fatalf("predecessor changed after rolled-back replace: %+v", reloaded.Request)
	}
}

func TestReviewRepositoryConcurrentReplaceHaveOneWinner(t *testing.T) {
	fixture := newReviewFixture(t, "review-replace-concurrency")
	defer fixture.close()

	issueID := fixture.insertIssue(t, "concurrent replace issue")
	created, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		Purposes:  []string{"implementation"},
		RequestID: fixture.newID(t),
		TargetID:  fixture.newID(t),
		IssueID:   issueID, TargetIssueVersion: 1, TargetEventID: 0, ArtifactIDs: []string{"artifact-1"},
		OccurredAt: time.Date(2026, 7, 24, 13, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for i := range 2 {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			_, err := fixture.repository.ReplaceReviewRequest(fixture.ctx, ports.ReplaceReviewRequestCommand{
				SuccessorID:          fixture.newID(t),
				SuccessorTargetID:    fixture.newID(t),
				PredecessorRequestID: created.Request.ID, PredecessorExpectedVersion: created.Request.Version,
				TargetIssueVersion: 2, TargetEventID: 5, ArtifactIDs: []string{"artifact-2"},
				OccurredAt:     time.Date(2026, 7, 24, 13, 1, 0, 0, time.UTC),
				IdempotencyKey: "concurrent-replace", RequestHash: []byte("same-hash"),
			})
			_ = index
			results <- err
		}(i)
	}
	close(start)
	group.Wait()
	close(results)

	var success int
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent replace error = %v", err)
		}
		success++
	}
	if success != 2 {
		t.Fatalf("concurrent replace success count = %d, want 2 (second call replays via the shared idempotency key)", success)
	}

	var successorCount int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM review_requests WHERE supersedes_id = ?`, created.Request.ID).Scan(&successorCount)
	}); err != nil {
		t.Fatal(err)
	}
	if successorCount != 1 {
		t.Fatalf("successor count = %d, want exactly 1 active successor despite 2 concurrent callers", successorCount)
	}
}

// TestReviewRepositoryConcurrentReplaceByIndependentCallersHaveOneWinner
// covers two callers who don't know about each other and use distinct
// idempotency keys, unlike TestReviewRepositoryConcurrentReplaceHaveOneWinner
// above where both share one key. The loser must see its own now-stale
// PredecessorExpectedVersion rejected with VERSION_CONFLICT, not a false
// success — mirroring TestReviewRepositoryConcurrentClaimsHaveOneWinner.
func TestReviewRepositoryConcurrentReplaceByIndependentCallersHaveOneWinner(t *testing.T) {
	fixture := newReviewFixture(t, "review-replace-concurrency-independent")
	defer fixture.close()

	issueID := fixture.insertIssue(t, "concurrent independent replace issue")
	created, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
		Purposes:  []string{"implementation"},
		RequestID: fixture.newID(t),
		TargetID:  fixture.newID(t),
		IssueID:   issueID, TargetIssueVersion: 1, TargetEventID: 0, ArtifactIDs: []string{"artifact-1"},
		OccurredAt: time.Date(2026, 7, 24, 14, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for i := range 2 {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			_, err := fixture.repository.ReplaceReviewRequest(fixture.ctx, ports.ReplaceReviewRequestCommand{
				SuccessorID:          fixture.newID(t),
				SuccessorTargetID:    fixture.newID(t),
				PredecessorRequestID: created.Request.ID, PredecessorExpectedVersion: created.Request.Version,
				TargetIssueVersion: 2, TargetEventID: 5, ArtifactIDs: []string{"artifact-2"},
				OccurredAt:     time.Date(2026, 7, 24, 14, 1, 0, 0, time.UTC),
				IdempotencyKey: fmt.Sprintf("independent-caller-%d", index), RequestHash: []byte{byte(index)},
			})
			results <- err
		}(i)
	}
	close(start)
	group.Wait()
	close(results)

	var success, versionConflicts int
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, &domain.Error{Code: domain.CodeVersionConflict}):
			versionConflicts++
		default:
			t.Fatalf("concurrent independent replace error = %v", err)
		}
	}
	if success != 1 || versionConflicts != 1 {
		t.Fatalf("concurrent independent replace outcomes = success %d version_conflicts %d, want 1 and 1", success, versionConflicts)
	}

	var successorCount int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM review_requests WHERE supersedes_id = ?`, created.Request.ID).Scan(&successorCount)
	}); err != nil {
		t.Fatal(err)
	}
	if successorCount != 1 {
		t.Fatalf("successor count = %d, want exactly 1 active successor", successorCount)
	}
}

func reviewStringPtr(value string) *string {
	return &value
}

type reviewRepositoryFixture struct {
	t          *testing.T
	ctx        context.Context
	db         *sqlite.DB
	repository *sqlite.ReviewRepository
	generator  *ids.Generator
}

func newReviewFixture(t *testing.T, name string) *reviewRepositoryFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".db")
	db, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatalf("sqlite.Open(): %v", err)
	}
	fakeClock := clock.NewFakeClock(time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC))
	if _, err := migrations.Migrate(context.Background(), db, fakeClock); err != nil {
		t.Fatalf("migrations.Migrate(): %v", err)
	}
	repository, err := sqlite.NewReviewRepository(db)
	if err != nil {
		t.Fatalf("NewReviewRepository(): %v", err)
	}
	generator, err := ids.NewGenerator(fakeClock, rand.Reader)
	if err != nil {
		t.Fatalf("ids.NewGenerator(): %v", err)
	}
	return &reviewRepositoryFixture{t: t, ctx: context.Background(), db: db, repository: repository, generator: generator}
}

func (fixture *reviewRepositoryFixture) close() {
	if err := fixture.db.Close(fixture.ctx); err != nil {
		fixture.t.Fatalf("Close() error = %v", err)
	}
}

func (fixture *reviewRepositoryFixture) insertIssue(t *testing.T, title string) string {
	t.Helper()
	issueID := "00000000000000000000000001"
	if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO issues(id, sequence_no, type, title, status, priority, version, created_at, updated_at)
            VALUES (?, 1, 'task', ?, 'ready', 'medium', 1, ?, ?)`, issueID, title, sqlite.FormatStorageTime(time.Now().UTC()), sqlite.FormatStorageTime(time.Now().UTC()))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return issueID
}

func (fixture *reviewRepositoryFixture) insertReviewAttempt(t *testing.T, issueID string) string {
	t.Helper()
	attemptID := "00000000000000000000000002"
	if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO work_attempts(
            id, issue_id, kind, status, issue_version_at_start, context_event_id_at_start,
            lease_token_hash, lease_expires_at, started_at, last_heartbeat_at
        ) VALUES (?, ?, 'review', 'active', 1, 0, X'01', ?, ?, ?)`, attemptID, issueID, sqlite.FormatStorageTime(time.Now().UTC().Add(time.Hour)), sqlite.FormatStorageTime(time.Now().UTC()), sqlite.FormatStorageTime(time.Now().UTC()))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return attemptID
}

func (fixture *reviewRepositoryFixture) newID(t *testing.T) string {
	t.Helper()
	id, err := fixture.generator.New()
	if err != nil {
		t.Fatalf("generator.New(): %v", err)
	}
	return id
}
