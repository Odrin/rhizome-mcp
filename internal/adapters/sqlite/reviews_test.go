package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/migrations"
	"rhizome-mcp/internal/ports"
)

func TestReviewRepositoryLifecycleCreatesEventsAndOutcome(t *testing.T) {
	fixture := newReviewFixture(t, "review-lifecycle")
	defer fixture.close()

	issueID := fixture.insertIssue(t, "review issue")
	attemptID := fixture.insertReviewAttempt(t, issueID)

	created, err := fixture.repository.CreateReviewRequest(fixture.ctx, ports.CreateReviewRequestCommand{
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
		return query.QueryRowContext(ctx, `SELECT count(*) FROM review_events WHERE request_id = ?`, created.Request.ID).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("review event count = %d, want 3", count)
	}
}

func TestReviewRepositoryCreateIsIdempotentForConcurrentDuplicates(t *testing.T) {
	fixture := newReviewFixture(t, "review-duplicates")
	defer fixture.close()

	issueID := fixture.insertIssue(t, "duplicate review")

	command := ports.CreateReviewRequestCommand{
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
		return query.QueryRowContext(ctx, `SELECT count(*) FROM review_events WHERE request_id = ? AND event_type = 'review_claimed'`, created.Request.ID).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("claim event count = %d, want 1", count)
	}
}

type reviewRepositoryFixture struct {
	t          *testing.T
	ctx        context.Context
	db         *sqlite.DB
	repository *sqlite.ReviewRepository
}

func newReviewFixture(t *testing.T, name string) *reviewRepositoryFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".db")
	db, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatalf("sqlite.Open(): %v", err)
	}
	if _, err := migrations.Migrate(context.Background(), db, clock.NewFakeClock(time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC))); err != nil {
		t.Fatalf("migrations.Migrate(): %v", err)
	}
	repository, err := sqlite.NewReviewRepository(db)
	if err != nil {
		t.Fatalf("NewReviewRepository(): %v", err)
	}
	return &reviewRepositoryFixture{t: t, ctx: context.Background(), db: db, repository: repository}
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
            VALUES (?, 1, 'task', ?, 'ready', 'medium', 1, ?, ?)`, issueID, title, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
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
        ) VALUES (?, ?, 'review', 'active', 1, 0, X'01', ?, ?, ?)`, attemptID, issueID, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return attemptID
}
