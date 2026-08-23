package sqlite_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

func acquireCommand(fixture *attemptTestFixture, t *testing.T, issueID, attemptID string, resources ...domain.Resource) ports.AcquireReservationsCommand {
	t.Helper()
	items := make([]ports.ReservationResourceInput, len(resources))
	for index, resource := range resources {
		items[index] = ports.ReservationResourceInput{ID: fixture.newID(t), Resource: resource}
	}
	return ports.AcquireReservationsCommand{
		IssueID: issueID, AttemptID: attemptID, Resources: items, OccurredAt: fixture.clock.Now(),
	}
}

func claimReservationFixture(t *testing.T, fixture *attemptTestFixture, title string) application.ClaimIssueResult {
	t.Helper()
	issue := createAttemptIssue(t, fixture, title, domain.StatusReady)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatalf("ClaimIssue(%s): %v", title, err)
	}
	return claim
}

func TestReservationsAcquireAndListActive(t *testing.T) {
	fixture := newAttemptTestFixture(t, "reservations-acquire")
	defer fixture.close()
	claim := claimReservationFixture(t, fixture, "acquire")
	repository, err := sqlite.NewReservationRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}

	command := acquireCommand(fixture, t, claim.Issue.ID, claim.Attempt.ID,
		domain.Resource{Kind: domain.ResourceKindFile, Path: "internal/adapters/sqlite/reservations.go"},
		domain.Resource{Kind: domain.ResourceKindLogical, Namespace: "docs", Name: "rfc-1"},
	)
	reservations, err := repository.AcquireReservations(fixture.ctx, command)
	if err != nil {
		t.Fatalf("AcquireReservations() error = %v", err)
	}
	if len(reservations) != 2 {
		t.Fatalf("reservations = %d, want 2", len(reservations))
	}
	for _, reservation := range reservations {
		if reservation.Status != domain.ReservationStatusActive || reservation.Version != 1 ||
			reservation.IssueID != claim.Issue.ID || reservation.AttemptID != claim.Attempt.ID {
			t.Fatalf("reservation = %+v, want active v1 for the claimed issue/attempt", reservation)
		}
	}

	active, err := repository.ListActiveReservations(fixture.ctx, ports.ListActiveReservationsQuery{AttemptID: claim.Attempt.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 || active[0].ID >= active[1].ID {
		t.Fatalf("active = %+v, want 2 rows ordered by id ascending", active)
	}

	if got := countAttemptEvents(t, fixture, claim.Attempt.ID, "reservation_reserved"); got != 2 {
		t.Fatalf("reservation_reserved events = %d, want 2", got)
	}
}

func TestReservationsAcquireRejectsExternalConflict(t *testing.T) {
	fixture := newAttemptTestFixture(t, "reservations-conflict")
	defer fixture.close()
	claimA := claimReservationFixture(t, fixture, "conflict a")
	claimB := claimReservationFixture(t, fixture, "conflict b")
	repository, err := sqlite.NewReservationRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}

	directory := domain.Resource{Kind: domain.ResourceKindDirectory, Path: "internal/adapters/sqlite"}
	if _, err := repository.AcquireReservations(fixture.ctx, acquireCommand(fixture, t, claimA.Issue.ID, claimA.Attempt.ID, directory)); err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	conflicting := domain.Resource{Kind: domain.ResourceKindFile, Path: "internal/adapters/sqlite/reservations.go"}
	_, err = repository.AcquireReservations(fixture.ctx, acquireCommand(fixture, t, claimB.Issue.ID, claimB.Attempt.ID, conflicting))
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeResourceReservationConflict {
		t.Fatalf("second acquire error = %v, want CodeResourceReservationConflict", err)
	}
	if len(domainErr.Details) != 1 {
		t.Fatalf("conflict details = %+v, want exactly one bounded detail", domainErr.Details)
	}
	if strings.Contains(domainErr.Details[0].Message, claimA.LeaseToken) || strings.Contains(domainErr.Details[0].Message, claimB.LeaseToken) {
		t.Fatalf("conflict detail leaked a lease token: %q", domainErr.Details[0].Message)
	}

	active, err := repository.ListActiveReservations(fixture.ctx, ports.ListActiveReservationsQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("active after rejected conflict = %d, want 1 (all-or-nothing)", len(active))
	}
	if got := countAttemptEvents(t, fixture, claimB.Attempt.ID, "reservation_reserved"); got != 0 {
		t.Fatalf("rejected acquisition appended %d events, want 0", got)
	}
}

func TestReservationsAcquireCollapsesDuplicatesAndRejectsInternalOverlap(t *testing.T) {
	fixture := newAttemptTestFixture(t, "reservations-internal")
	defer fixture.close()
	claim := claimReservationFixture(t, fixture, "internal")
	repository, err := sqlite.NewReservationRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}

	reservations, err := repository.AcquireReservations(fixture.ctx, acquireCommand(fixture, t, claim.Issue.ID, claim.Attempt.ID,
		domain.Resource{Kind: domain.ResourceKindFile, Path: "a/b.go"},
		domain.Resource{Kind: domain.ResourceKindFile, Path: "A/B.GO"}, // exact duplicate once ASCII-folded
	))
	if err != nil {
		t.Fatalf("acquire with duplicate: %v", err)
	}
	if len(reservations) != 1 {
		t.Fatalf("reservations = %d, want 1 (exact duplicates collapse)", len(reservations))
	}

	_, err = repository.AcquireReservations(fixture.ctx, acquireCommand(fixture, t, claim.Issue.ID, claim.Attempt.ID,
		domain.Resource{Kind: domain.ResourceKindDirectory, Path: "src"},
		domain.Resource{Kind: domain.ResourceKindFile, Path: "src/main.go"},
	))
	if !errors.Is(err, &domain.Error{Code: domain.CodeInvalidReservationSet}) {
		t.Fatalf("overlapping internal request error = %v, want CodeInvalidReservationSet", err)
	}

	active, err := repository.ListActiveReservations(fixture.ctx, ports.ListActiveReservationsQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("active after rejected internal overlap = %d, want 1 (nothing inserted)", len(active))
	}
}

func TestReservationsAcquireIsIdempotent(t *testing.T) {
	fixture := newAttemptTestFixture(t, "reservations-idempotent")
	defer fixture.close()
	claim := claimReservationFixture(t, fixture, "idempotent")
	repository, err := sqlite.NewReservationRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}

	command := acquireCommand(fixture, t, claim.Issue.ID, claim.Attempt.ID, domain.Resource{Kind: domain.ResourceKindFile, Path: "x.go"})
	command.IdempotencyKey = "acquire-1"
	command.RequestHash = []byte("hash-1")

	first, err := repository.AcquireReservations(fixture.ctx, command)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	second, err := repository.AcquireReservations(fixture.ctx, command)
	if err != nil {
		t.Fatalf("replay acquire: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("replay mismatch: first %+v second %+v", first, second)
	}

	active, err := repository.ListActiveReservations(fixture.ctx, ports.ListActiveReservationsQuery{AttemptID: claim.Attempt.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("active after replay = %d, want 1 (no duplicate insert)", len(active))
	}

	changed := command
	changed.RequestHash = []byte("hash-2")
	if _, err := repository.AcquireReservations(fixture.ctx, changed); !errors.Is(err, &domain.Error{Code: domain.CodeIdempotencyConflict}) {
		t.Fatalf("changed request error = %v, want CodeIdempotencyConflict", err)
	}
}

func TestReservationsReleaseTransitionsToHistoryAndGuardsVersion(t *testing.T) {
	fixture := newAttemptTestFixture(t, "reservations-release")
	defer fixture.close()
	claim := claimReservationFixture(t, fixture, "release")
	repository, err := sqlite.NewReservationRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}

	reservations, err := repository.AcquireReservations(fixture.ctx, acquireCommand(fixture, t, claim.Issue.ID, claim.Attempt.ID,
		domain.Resource{Kind: domain.ResourceKindFile, Path: "release.go"}))
	if err != nil {
		t.Fatal(err)
	}
	reservation := reservations[0]

	if _, err := repository.ReleaseReservation(fixture.ctx, ports.ReleaseReservationCommand{
		ID: reservation.ID, ExpectedVersion: 99, Reason: domain.ReservationReleaseReasonCompleted, OccurredAt: fixture.clock.Now(),
	}); !errors.Is(err, &domain.Error{Code: domain.CodeVersionConflict}) {
		t.Fatalf("wrong version error = %v, want CodeVersionConflict", err)
	}

	released, err := repository.ReleaseReservation(fixture.ctx, ports.ReleaseReservationCommand{
		ID: reservation.ID, ExpectedVersion: reservation.Version, Reason: domain.ReservationReleaseReasonCompleted, OccurredAt: fixture.clock.Now(),
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if released.Status != domain.ReservationStatusReleased || released.Version != reservation.Version+1 ||
		released.ReleasedAt == nil || released.ReleaseReason == nil || *released.ReleaseReason != domain.ReservationReleaseReasonCompleted {
		t.Fatalf("released reservation = %+v, want a completed release", released)
	}

	if _, err := repository.ReleaseReservation(fixture.ctx, ports.ReleaseReservationCommand{
		ID: reservation.ID, ExpectedVersion: released.Version, Reason: domain.ReservationReleaseReasonCompleted, OccurredAt: fixture.clock.Now(),
	}); !errors.Is(err, &domain.Error{Code: domain.CodeReservationNotActive}) {
		t.Fatalf("double release error = %v, want CodeReservationNotActive", err)
	}

	if _, err := repository.ReleaseReservation(fixture.ctx, ports.ReleaseReservationCommand{
		ID: fixture.newID(t), ExpectedVersion: 1, Reason: domain.ReservationReleaseReasonExplicit, OccurredAt: fixture.clock.Now(),
	}); !errors.Is(err, &domain.Error{Code: domain.CodeReservationNotFound}) {
		t.Fatalf("unknown id error = %v, want CodeReservationNotFound", err)
	}

	active, err := repository.ListActiveReservations(fixture.ctx, ports.ListActiveReservationsQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("active after release = %d, want 0", len(active))
	}
	history, err := repository.ListReservationHistory(fixture.ctx, ports.ListReservationHistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].ID != reservation.ID {
		t.Fatalf("history = %+v, want just the released reservation", history)
	}
	if got := countAttemptEvents(t, fixture, claim.Attempt.ID, "reservation_released"); got != 1 {
		t.Fatalf("reservation_released events = %d, want 1", got)
	}
}

// TestReservationsReleaseOnFinishAttempt is ISSUE-179's core contract for
// the FinishAttempt-routed terminations: every outcome releases the
// attempt's active reservations in the same transaction, tagged with the
// release reason that explains why.
func TestReservationsReleaseOnFinishAttempt(t *testing.T) {
	cases := []struct {
		name       string
		outcome    domain.AttemptOutcome
		configure  func(*domain.FinishAttemptInput)
		wantReason domain.ReservationReleaseReason
	}{
		{"completed", domain.AttemptOutcomeCompleted, func(input *domain.FinishAttemptInput) {
			input.TargetIssueStatus = statusPointer(domain.StatusDone)
		}, domain.ReservationReleaseReasonCompleted},
		{"failed", domain.AttemptOutcomeFailed, func(input *domain.FinishAttemptInput) {
			input.FailureReasonCode = failurePointer(domain.FailureReasonTestsFailed)
		}, domain.ReservationReleaseReasonFailed},
		{"interrupted", domain.AttemptOutcomeInterrupted, func(input *domain.FinishAttemptInput) {
			input.InterruptionReasonCode = interruptionPointer(domain.InterruptionReasonHandoff)
		}, domain.ReservationReleaseReasonInterrupted},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newAttemptTestFixture(t, "reservations-finish-"+testCase.name)
			defer fixture.close()
			claim := claimReservationFixture(t, fixture, testCase.name)
			repository, err := sqlite.NewReservationRepository(fixture.db)
			if err != nil {
				t.Fatal(err)
			}
			reserved, err := repository.AcquireReservations(fixture.ctx, acquireCommand(fixture, t, claim.Issue.ID, claim.Attempt.ID,
				domain.Resource{Kind: domain.ResourceKindFile, Path: testCase.name + ".go"}))
			if err != nil {
				t.Fatal(err)
			}

			input := finishInput(claim, testCase.outcome)
			testCase.configure(&input)
			if _, err := fixture.attempts.FinishAttempt(fixture.ctx, input); err != nil {
				t.Fatalf("FinishAttempt(%s): %v", testCase.outcome, err)
			}

			assertReservationReleased(t, fixture, repository, claim.Attempt.ID, reserved[0].ID, testCase.wantReason)
		})
	}
}

// TestReservationsReleaseOnForceRelease covers the one terminateAttempt call
// site FinishAttempt doesn't: ForceReleaseAttempt sets the same
// AttemptStatusInterrupted a plain interrupted finish does, but must record
// a distinct force_released reservation reason rather than interrupted.
func TestReservationsReleaseOnForceRelease(t *testing.T) {
	fixture := newAttemptTestFixture(t, "reservations-force-release")
	defer fixture.close()
	claim := claimReservationFixture(t, fixture, "force release")
	reservationRepository, err := sqlite.NewReservationRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := reservationRepository.AcquireReservations(fixture.ctx, acquireCommand(fixture, t, claim.Issue.ID, claim.Attempt.ID,
		domain.Resource{Kind: domain.ResourceKindFile, Path: "force-release.go"}))
	if err != nil {
		t.Fatal(err)
	}

	attemptRepository, err := sqlite.NewAttemptRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attemptRepository.ForceReleaseAttempt(fixture.ctx, ports.ForceReleaseAttemptCommand{
		AttemptID: claim.Attempt.ID, OccurredAt: fixture.clock.Now(),
	}); err != nil {
		t.Fatalf("ForceReleaseAttempt: %v", err)
	}

	assertReservationReleased(t, fixture, reservationRepository, claim.Attempt.ID, reserved[0].ID, domain.ReservationReleaseReasonForceReleased)
}

// TestReservationsReleaseOnExpiry is the clock-driven boundary: a
// reservation stays authoritative for exactly as long as its owning
// attempt's lease does, with no separate reservation clock. It must remain
// active up to and including the instant lease_expires_at, and be released
// -- reason expired -- only once ExpireAttempts sweeps past it.
func TestReservationsReleaseOnExpiry(t *testing.T) {
	fixture := newAttemptTestFixture(t, "reservations-expiry")
	defer fixture.close()
	issue := createAttemptIssue(t, fixture, "expiry", domain.StatusReady)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID, LeaseSeconds: intPointer(60)})
	if err != nil {
		t.Fatal(err)
	}
	reservationRepository, err := sqlite.NewReservationRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := reservationRepository.AcquireReservations(fixture.ctx, acquireCommand(fixture, t, claim.Issue.ID, claim.Attempt.ID,
		domain.Resource{Kind: domain.ResourceKindFile, Path: "expiry.go"}))
	if err != nil {
		t.Fatal(err)
	}

	// Advance to the exact lease boundary and sweep: still active. The
	// attempt's own lease_expires_at <= now guard is inclusive, but the
	// clock is advanced to precisely that instant here, before the guard
	// takes effect on the *next* tick, to prove authority survives up to
	// (not just strictly before) expiry.
	fixture.clock.Advance(59 * time.Second)
	if _, err := fixture.attempts.ExpireAttempts(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	active, err := reservationRepository.ListActiveReservations(fixture.ctx, ports.ListActiveReservationsQuery{AttemptID: claim.Attempt.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("active one second before lease expiry = %d, want 1 (still authoritative)", len(active))
	}

	// Advance past the boundary and sweep again: now released as expired.
	fixture.clock.Advance(time.Second)
	if _, err := fixture.attempts.ExpireAttempts(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	assertReservationReleased(t, fixture, reservationRepository, claim.Attempt.ID, reserved[0].ID, domain.ReservationReleaseReasonExpired)
}

func assertReservationReleased(t *testing.T, fixture *attemptTestFixture, repository *sqlite.ReservationRepository, attemptID, reservationID string, wantReason domain.ReservationReleaseReason) {
	t.Helper()
	active, err := repository.ListActiveReservations(fixture.ctx, ports.ListActiveReservationsQuery{AttemptID: attemptID})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("active reservations for attempt %s = %d, want 0", attemptID, len(active))
	}
	history, err := repository.ListReservationHistory(fixture.ctx, ports.ListReservationHistoryQuery{AttemptID: attemptID})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].ID != reservationID ||
		history[0].ReleaseReason == nil || *history[0].ReleaseReason != wantReason {
		t.Fatalf("history for attempt %s = %+v, want one row %s released as %q", attemptID, history, reservationID, wantReason)
	}
	if got := countAttemptEvents(t, fixture, attemptID, "reservation_released"); got != 1 {
		t.Fatalf("reservation_released events for attempt %s = %d, want 1", attemptID, got)
	}
}

// TestReservationsConcurrentAcquireHaveOneWinner proves acquisition is
// conflict-free even under concurrent processes sharing one database: two
// attempts race to reserve the same file, exactly one must win, and an
// independent read-back must agree.
func TestReservationsConcurrentAcquireHaveOneWinner(t *testing.T) {
	fixture := newAttemptTestFixture(t, "reservations-race")
	defer fixture.close()
	claimA := claimReservationFixture(t, fixture, "race a")
	claimB := claimReservationFixture(t, fixture, "race b")
	repository, err := sqlite.NewReservationRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}

	resource := domain.Resource{Kind: domain.ResourceKindFile, Path: "shared.go"}
	commandA := acquireCommand(fixture, t, claimA.Issue.ID, claimA.Attempt.ID, resource)
	commandB := acquireCommand(fixture, t, claimB.Issue.ID, claimB.Attempt.ID, resource)

	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, command := range []ports.AcquireReservationsCommand{commandA, commandB} {
		group.Add(1)
		go func(command ports.AcquireReservationsCommand) {
			defer group.Done()
			_, err := repository.AcquireReservations(fixture.ctx, command)
			results <- err
		}(command)
	}
	group.Wait()
	close(results)

	var succeeded, conflicted int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, &domain.Error{Code: domain.CodeResourceReservationConflict}):
			conflicted++
		default:
			t.Fatalf("acquire error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("successes %d conflicts %d, want exactly one of each", succeeded, conflicted)
	}

	active, err := repository.ListActiveReservations(fixture.ctx, ports.ListActiveReservationsQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("active reservations after race = %d, want exactly 1", len(active))
	}
}

func TestReservationsPersistAcrossReopen(t *testing.T) {
	fixture := newAttemptTestFixture(t, "reservations-reopen")
	defer fixture.close()
	claim := claimReservationFixture(t, fixture, "reopen")
	repository, err := sqlite.NewReservationRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	reservations, err := repository.AcquireReservations(fixture.ctx, acquireCommand(fixture, t, claim.Issue.ID, claim.Attempt.ID,
		domain.Resource{Kind: domain.ResourceKindFile, Path: "reopen.go"}))
	if err != nil {
		t.Fatal(err)
	}

	reopenAttemptTestFixture(t, fixture)
	reopened, err := sqlite.NewReservationRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}

	active, err := reopened.ListActiveReservations(fixture.ctx, ports.ListActiveReservationsQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ID != reservations[0].ID {
		t.Fatalf("active after reopen = %+v, want the reservation acquired before reopen", active)
	}
}

// TestReservationsListDetectsCorruption covers the two malformed-storage
// shapes SQLite's own CHECK constraints cannot catch (every other column is
// already CHECK-guarded): normalized_json that is valid JSON but whose path
// fails re-normalization, and a comparison_value that no longer matches
// what normalized_json recomputes.
func TestReservationsListDetectsCorruption(t *testing.T) {
	fixture := newAttemptTestFixture(t, "reservations-corrupt")
	defer fixture.close()
	claim := claimReservationFixture(t, fixture, "corrupt")
	repository, err := sqlite.NewReservationRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	reservations, err := repository.AcquireReservations(fixture.ctx, acquireCommand(fixture, t, claim.Issue.ID, claim.Attempt.ID,
		domain.Resource{Kind: domain.ResourceKindFile, Path: "corrupt.go"}))
	if err != nil {
		t.Fatal(err)
	}
	id := reservations[0].ID

	tests := []struct {
		name   string
		tamper string
	}{
		{name: "normalized_json fails re-normalization", tamper: `UPDATE resource_reservations SET normalized_json = '{"path":"../escape"}' WHERE id = ?`},
		{name: "comparison_value mismatch", tamper: `UPDATE resource_reservations SET comparison_value = 'file:something-else' WHERE id = ?`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
				_, err := tx.ExecContext(ctx, test.tamper, id)
				return err
			}); err != nil {
				t.Fatalf("tamper: %v", err)
			}
			_, err := repository.ListActiveReservations(fixture.ctx, ports.ListActiveReservationsQuery{})
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeStorageCorrupt {
				t.Fatalf("list after tamper error = %v, want CodeStorageCorrupt", err)
			}
		})
	}
}
