package application_test

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/migrations"
)

// ISSUE-181 AC2/AC4: the board's active-reservations projection is
// sqlite-backed end to end (not the recording-fake unit tests in
// board_service_test.go), proving reservations actually appear while
// active and disappear once released or once their owning attempt's lease
// expires -- the two states AC4 names that a fake repository can't prove.
func newBoardReservationLifecycleFixture(t *testing.T) (*application.BoardService, *application.IssueService, *application.AttemptService, *clock.FakeClock, context.Context) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	source := clock.NewFakeClock(now)
	path := filepath.Join(t.TempDir(), "board-reservation-lifecycle.db")
	db, err := sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(ctx) })
	if _, err := migrations.Migrate(ctx, db, source); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id, next_issue_number, created_at, updated_at) VALUES (?, 1, ?, ?)`,
			"01ARZ3NDEKTSV4RRFFQ69G5FAV", sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(now))
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	generator, err := ids.NewGenerator(source, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issueRepository, err := sqlite.NewIssueRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	attemptRepository, err := sqlite.NewAttemptRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	reservationRepository, err := sqlite.NewReservationRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	reviewRepository, err := sqlite.NewReviewRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	graphRepository, err := sqlite.NewGraphRepository(db)
	if err != nil {
		t.Fatal(err)
	}

	issueService, err := application.NewIssueService(issueRepository, source, generator)
	if err != nil {
		t.Fatal(err)
	}
	attemptService, err := application.NewAttemptService(attemptRepository, source, generator)
	if err != nil {
		t.Fatal(err)
	}
	reservationService, err := application.NewReservationService(reservationRepository)
	if err != nil {
		t.Fatal(err)
	}
	reviewService, err := application.NewReviewService(reviewRepository, issueRepository, source, generator)
	if err != nil {
		t.Fatal(err)
	}
	graphService, err := application.NewGraphService(graphRepository, source)
	if err != nil {
		t.Fatal(err)
	}
	boardService, err := application.NewBoardService(issueService, attemptService, reservationService, reviewService, graphService, source)
	if err != nil {
		t.Fatal(err)
	}
	return boardService, issueService, attemptService, source, ctx
}

func TestBoardReservationLifecycleAppearsWhileActiveAndDisappearsOnRelease(t *testing.T) {
	board, issues, attempts, _, ctx := newBoardReservationLifecycleFixture(t)

	created, err := issues.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "board reservation lifecycle", Status: domain.StatusReady})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}

	claim, err := attempts.ClaimIssue(ctx, domain.ClaimIssueInput{
		IssueID:   created.ID,
		Resources: []domain.Resource{{Kind: domain.ResourceKindFile, Path: "lifecycle.go"}},
	})
	if err != nil {
		t.Fatalf("ClaimIssue() error = %v", err)
	}
	if len(claim.Reservations) != 1 {
		t.Fatalf("reservations = %d, want 1", len(claim.Reservations))
	}

	active, err := board.GetBoard(ctx)
	if err != nil {
		t.Fatalf("GetBoard() error = %v", err)
	}
	if len(active.ActiveReservations) != 1 || active.ActiveReservations[0].DisplayValue != "lifecycle.go" {
		t.Fatalf("active board reservations = %+v, want one lifecycle.go", active.ActiveReservations)
	}
	if active.ActiveReservations[0].AttemptID != claim.Attempt.ID {
		t.Fatalf("reservation attempt id = %q, want %q", active.ActiveReservations[0].AttemptID, claim.Attempt.ID)
	}
	found := false
	for _, attempt := range active.ActiveAttempts {
		if attempt.AttemptID == claim.Attempt.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("active board attempts = %+v, want to include %q so the reservation can be grouped under it", active.ActiveAttempts, claim.Attempt.ID)
	}

	if _, err := attempts.ReleaseResources(ctx, domain.ReleaseResourcesInput{AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken}); err != nil {
		t.Fatalf("ReleaseResources() error = %v", err)
	}

	afterRelease, err := board.GetBoard(ctx)
	if err != nil {
		t.Fatalf("GetBoard() error = %v", err)
	}
	if len(afterRelease.ActiveReservations) != 0 {
		t.Fatalf("board reservations after release = %+v, want empty", afterRelease.ActiveReservations)
	}
}

func TestBoardReservationLifecycleExcludesReservationsOfALeaseExpiredAttempt(t *testing.T) {
	board, issues, attempts, source, ctx := newBoardReservationLifecycleFixture(t)

	created, err := issues.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "board reservation expiry", Status: domain.StatusReady})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}

	leaseSeconds := 60
	claim, err := attempts.ClaimIssue(ctx, domain.ClaimIssueInput{
		IssueID:      created.ID,
		LeaseSeconds: &leaseSeconds,
		Resources:    []domain.Resource{{Kind: domain.ResourceKindFile, Path: "expiring.go"}},
	})
	if err != nil {
		t.Fatalf("ClaimIssue() error = %v", err)
	}

	// Advance past the lease without an explicit release, finish, or the
	// periodic ExpireAttempts sweep -- the attempt is now lease-expired but
	// its rows (and the reservation's 'active' status) are still exactly as
	// claim wrote them, matching a real operator's window between expiry and
	// the next sweep.
	source.Advance(2 * time.Minute)

	afterExpiry, err := board.GetBoard(ctx)
	if err != nil {
		t.Fatalf("GetBoard() error = %v", err)
	}
	for _, attempt := range afterExpiry.ActiveAttempts {
		if attempt.AttemptID == claim.Attempt.ID {
			t.Fatalf("lease-expired attempt %q should not appear in ActiveAttempts: %+v", claim.Attempt.ID, afterExpiry.ActiveAttempts)
		}
	}
	// ListActiveAttempts already excludes lease-expired attempts (an
	// established, pre-existing contract), so a reservation whose owner no
	// longer has a matching ActiveAttempts row cannot be grouped and must
	// not surface as an orphan the view layer can't attribute to anyone.
	for _, reservation := range afterExpiry.ActiveReservations {
		if reservation.AttemptID == claim.Attempt.ID {
			t.Fatalf("reservation owned by lease-expired attempt %q should not appear on the board: %+v", claim.Attempt.ID, afterExpiry.ActiveReservations)
		}
	}
}
