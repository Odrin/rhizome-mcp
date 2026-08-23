package sqlite_test

import (
	"context"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

// ISSUE-180 AC1: optional claim resources are backward compatible and
// transactionally all-or-nothing with claim.
func TestClaimIssueAcquiresReservationsAtomically(t *testing.T) {
	fixture := newAttemptTestFixture(t, "claim-acquire-atomic")
	defer fixture.close()
	issue := createAttemptIssue(t, fixture, "claim with resources", domain.StatusReady)

	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{
		IssueID: issue.ID,
		Resources: []domain.Resource{
			{Kind: domain.ResourceKindFile, Path: "a.go"},
			{Kind: domain.ResourceKindLogical, Namespace: "docs", Name: "rfc-1"},
		},
	})
	if err != nil {
		t.Fatalf("ClaimIssue with resources: %v", err)
	}
	if claim.Attempt.Kind != domain.AttemptKindWork {
		t.Fatalf("claim kind = %v, want work", claim.Attempt.Kind)
	}
	if len(claim.Reservations) != 2 {
		t.Fatalf("reservations returned = %d, want 2", len(claim.Reservations))
	}
	for _, reservation := range claim.Reservations {
		if reservation.AttemptID != claim.Attempt.ID || reservation.IssueID != issue.ID || reservation.Status != domain.ReservationStatusActive {
			t.Fatalf("reservation = %+v, want owned by %s/%s and active", reservation, issue.ID, claim.Attempt.ID)
		}
	}

	repository, err := sqlite.NewReservationRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	active, err := repository.ListActiveReservations(fixture.ctx, ports.ListActiveReservationsQuery{AttemptID: claim.Attempt.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Fatalf("persisted active reservations = %d, want 2", len(active))
	}
	if got := countAttemptEvents(t, fixture, claim.Attempt.ID, "reservation_reserved"); got != 2 {
		t.Fatalf("reservation_reserved events = %d, want 2", got)
	}
}

// A conflicting resource aborts the whole claim, not just the reservation:
// no work attempt is created either, so the issue stays claimable.
func TestClaimIssueReservationConflictAbortsClaimEntirely(t *testing.T) {
	fixture := newAttemptTestFixture(t, "claim-acquire-conflict")
	defer fixture.close()
	holder := claimReservationFixture(t, fixture, "holder")
	reservationRepository, err := sqlite.NewReservationRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reservationRepository.AcquireReservations(fixture.ctx, acquireCommand(fixture, t, holder.Issue.ID, holder.Attempt.ID,
		domain.Resource{Kind: domain.ResourceKindFile, Path: "contested.go"})); err != nil {
		t.Fatal(err)
	}

	contested := createAttemptIssue(t, fixture, "claim conflict", domain.StatusReady)
	_, err = fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{
		IssueID:   contested.ID,
		Resources: []domain.Resource{{Kind: domain.ResourceKindFile, Path: "contested.go"}},
	})
	assertDomainCode(t, err, domain.CodeResourceReservationConflict)

	var attemptCount int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM work_attempts WHERE issue_id = ?`, contested.ID).Scan(&attemptCount)
	}); err != nil {
		t.Fatal(err)
	}
	if attemptCount != 0 {
		t.Fatalf("work_attempts for the conflicted claim = %d, want 0 (claim must not have happened)", attemptCount)
	}

	// The issue must still be claimable outright (not just claimable
	// without resources) -- the aborted transaction left no partial state.
	retry, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: contested.ID})
	if err != nil {
		t.Fatalf("retry claim after aborted conflict: %v", err)
	}
	if retry.Attempt.IssueID != contested.ID {
		t.Fatalf("retry claim issue = %s, want %s", retry.Attempt.IssueID, contested.ID)
	}
}

// ISSUE-179's locked lifecycle: only work attempts may own reservations. A
// claim that resolves to a review attempt must reject resources outright
// rather than silently dropping them.
func TestClaimReviewIssueRejectsResources(t *testing.T) {
	fixture := newAttemptTestFixture(t, "claim-review-rejects-resources")
	defer fixture.close()
	issue := createAttemptIssue(t, fixture, "review claim with resources", domain.StatusReview)

	_, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{
		IssueID:   issue.ID,
		Resources: []domain.Resource{{Kind: domain.ResourceKindFile, Path: "a.go"}},
	})
	assertDomainCode(t, err, domain.CodeInvalidArgument)

	var attemptCount int
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT count(*) FROM work_attempts WHERE issue_id = ?`, issue.ID).Scan(&attemptCount)
	}); err != nil {
		t.Fatal(err)
	}
	if attemptCount != 0 {
		t.Fatalf("work_attempts after rejected review claim = %d, want 0", attemptCount)
	}
}

// ISSUE-180 AC2: reserve_resources enforces active work ownership, bounds,
// normalization, and idempotency.
func TestReserveResourcesAddsToActiveAttempt(t *testing.T) {
	fixture := newAttemptTestFixture(t, "reserve-resources-basic")
	defer fixture.close()
	claim := claimReservationFixture(t, fixture, "reserve basic")

	result, err := fixture.attempts.ReserveResources(fixture.ctx, domain.ReserveResourcesInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken,
		Resources: []domain.Resource{{Kind: domain.ResourceKindFile, Path: "reserved.go"}},
	})
	if err != nil {
		t.Fatalf("ReserveResources: %v", err)
	}
	if len(result.Reservations) != 1 || result.Reservations[0].DisplayValue != "reserved.go" {
		t.Fatalf("ReserveResources result = %+v", result)
	}

	repository, err := sqlite.NewReservationRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	active, err := repository.ListActiveReservations(fixture.ctx, ports.ListActiveReservationsQuery{AttemptID: claim.Attempt.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("active reservations = %d, want 1", len(active))
	}
}

// A review attempt's lease fails WRONG_KIND at ReserveResources, mirroring
// ClaimIssue's own rejection for the claim-time path.
func TestReserveResourcesRejectsReviewAttempt(t *testing.T) {
	fixture := newAttemptTestFixture(t, "reserve-resources-review-kind")
	defer fixture.close()
	issue := createAttemptIssue(t, fixture, "review reserve", domain.StatusReview)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	if claim.Attempt.Kind != domain.AttemptKindReview {
		t.Fatalf("claim kind = %v, want review", claim.Attempt.Kind)
	}

	_, err = fixture.attempts.ReserveResources(fixture.ctx, domain.ReserveResourcesInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken,
		Resources: []domain.Resource{{Kind: domain.ResourceKindFile, Path: "a.go"}},
	})
	assertDomainCode(t, err, domain.CodeInvalidArgument)
}

func TestReserveResourcesRejectsInvalidLeaseToken(t *testing.T) {
	fixture := newAttemptTestFixture(t, "reserve-resources-bad-token")
	defer fixture.close()
	claim := claimReservationFixture(t, fixture, "bad token")

	_, err := fixture.attempts.ReserveResources(fixture.ctx, domain.ReserveResourcesInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken + "x",
		Resources: []domain.Resource{{Kind: domain.ResourceKindFile, Path: "a.go"}},
	})
	assertDomainCode(t, err, domain.CodeInvalidLeaseToken)
}

func TestReserveResourcesFailsAfterLeaseExpiry(t *testing.T) {
	fixture := newAttemptTestFixture(t, "reserve-resources-expired")
	defer fixture.close()
	issue := createAttemptIssue(t, fixture, "expiry reserve", domain.StatusReady)
	claim, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{IssueID: issue.ID, LeaseSeconds: intPointer(60)})
	if err != nil {
		t.Fatal(err)
	}
	fixture.clock.Advance(2 * time.Minute)

	_, err = fixture.attempts.ReserveResources(fixture.ctx, domain.ReserveResourcesInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken,
		Resources: []domain.Resource{{Kind: domain.ResourceKindFile, Path: "a.go"}},
	})
	assertDomainCode(t, err, domain.CodeLeaseExpired)
}

func TestReserveResourcesIdempotentReplayDoesNotDoubleAcquire(t *testing.T) {
	fixture := newAttemptTestFixture(t, "reserve-resources-idempotent")
	defer fixture.close()
	claim := claimReservationFixture(t, fixture, "idempotent reserve")
	key := "reserve-key-1"
	input := domain.ReserveResourcesInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken,
		Resources:      []domain.Resource{{Kind: domain.ResourceKindFile, Path: "once.go"}},
		IdempotencyKey: &key,
	}

	first, err := fixture.attempts.ReserveResources(fixture.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.attempts.ReserveResources(fixture.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Reservations) != 1 || len(second.Reservations) != 1 || first.Reservations[0].ID != second.Reservations[0].ID {
		t.Fatalf("replay = %+v, first = %+v, want identical single reservation", second, first)
	}

	repository, err := sqlite.NewReservationRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	active, err := repository.ListActiveReservations(fixture.ctx, ports.ListActiveReservationsQuery{AttemptID: claim.Attempt.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("active reservations after replay = %d, want 1 (no double acquisition)", len(active))
	}
}

// The locked API requires idempotency hashes to include normalized
// resources: two requests naming the same resource with different but
// normalization-equivalent spelling must hash identically and replay
// rather than conflict.
func TestReserveResourcesIdempotencyHashNormalizesResourcePath(t *testing.T) {
	fixture := newAttemptTestFixture(t, "reserve-resources-idempotent-normalized")
	defer fixture.close()
	claim := claimReservationFixture(t, fixture, "idempotent normalized reserve")
	key := "normalized-key-1"

	first, err := fixture.attempts.ReserveResources(fixture.ctx, domain.ReserveResourcesInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken,
		Resources:      []domain.Resource{{Kind: domain.ResourceKindFile, Path: "./src/foo.go"}},
		IdempotencyKey: &key,
	})
	if err != nil {
		t.Fatal(err)
	}

	second, err := fixture.attempts.ReserveResources(fixture.ctx, domain.ReserveResourcesInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken,
		Resources:      []domain.Resource{{Kind: domain.ResourceKindFile, Path: "src/foo.go"}},
		IdempotencyKey: &key,
	})
	if err != nil {
		t.Fatalf("replay with a normalization-equivalent path spelling should succeed, got: %v", err)
	}
	if len(first.Reservations) != 1 || len(second.Reservations) != 1 || first.Reservations[0].ID != second.Reservations[0].ID {
		t.Fatalf("replay = %+v, first = %+v, want identical single reservation", second, first)
	}

	repository, err := sqlite.NewReservationRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	active, err := repository.ListActiveReservations(fixture.ctx, ports.ListActiveReservationsQuery{AttemptID: claim.Attempt.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("active reservations after replay = %d, want 1 (no double acquisition)", len(active))
	}
}

// The same normalization requirement applies to claim_issue's optional
// resources field, matching reserve_resources' hashing convention.
func TestClaimIssueIdempotencyHashNormalizesResourcePath(t *testing.T) {
	fixture := newAttemptTestFixture(t, "claim-idempotent-normalized")
	defer fixture.close()
	issue := createAttemptIssue(t, fixture, "claim idempotent normalized", domain.StatusReady)
	key := "claim-normalized-key-1"

	first, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{
		IssueID:        issue.ID,
		Resources:      []domain.Resource{{Kind: domain.ResourceKindFile, Path: "./src/foo.go"}},
		IdempotencyKey: &key,
	})
	if err != nil {
		t.Fatal(err)
	}

	second, err := fixture.attempts.ClaimIssue(fixture.ctx, domain.ClaimIssueInput{
		IssueID:        issue.ID,
		Resources:      []domain.Resource{{Kind: domain.ResourceKindFile, Path: "src/foo.go"}},
		IdempotencyKey: &key,
	})
	if err != nil {
		t.Fatalf("replay with a normalization-equivalent path spelling should succeed, got: %v", err)
	}
	if first.Attempt.ID != second.Attempt.ID || len(second.Reservations) != 1 || first.Reservations[0].ID != second.Reservations[0].ID {
		t.Fatalf("replay = %+v, first = %+v, want identical claim and reservation", second, first)
	}
}

// ISSUE-180 AC2 (release half): release_resources releases named IDs only,
// leaving others owned by the same attempt untouched.
func TestReleaseResourcesNamedIDs(t *testing.T) {
	fixture := newAttemptTestFixture(t, "release-resources-named")
	defer fixture.close()
	claim := claimReservationFixture(t, fixture, "release named")
	reserved, err := fixture.attempts.ReserveResources(fixture.ctx, domain.ReserveResourcesInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken,
		Resources: []domain.Resource{
			{Kind: domain.ResourceKindFile, Path: "keep.go"},
			{Kind: domain.ResourceKindFile, Path: "drop.go"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var dropID string
	for _, reservation := range reserved.Reservations {
		if reservation.DisplayValue == "drop.go" {
			dropID = reservation.ID
		}
	}
	if dropID == "" {
		t.Fatalf("could not find drop.go reservation in %+v", reserved.Reservations)
	}

	released, err := fixture.attempts.ReleaseResources(fixture.ctx, domain.ReleaseResourcesInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken, ReservationIDs: []string{dropID},
	})
	if err != nil {
		t.Fatalf("ReleaseResources: %v", err)
	}
	if len(released.Reservations) != 1 || released.Reservations[0].ID != dropID || released.Reservations[0].Status != domain.ReservationStatusReleased {
		t.Fatalf("released = %+v", released)
	}

	repository, err := sqlite.NewReservationRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	active, err := repository.ListActiveReservations(fixture.ctx, ports.ListActiveReservationsQuery{AttemptID: claim.Attempt.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].DisplayValue != "keep.go" {
		t.Fatalf("remaining active = %+v, want only keep.go", active)
	}
}

// Empty ReservationIDs releases every active reservation the attempt owns.
func TestReleaseResourcesEmptyIDsReleasesAllActive(t *testing.T) {
	fixture := newAttemptTestFixture(t, "release-resources-all")
	defer fixture.close()
	claim := claimReservationFixture(t, fixture, "release all")
	if _, err := fixture.attempts.ReserveResources(fixture.ctx, domain.ReserveResourcesInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken,
		Resources: []domain.Resource{
			{Kind: domain.ResourceKindFile, Path: "a.go"},
			{Kind: domain.ResourceKindFile, Path: "b.go"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	released, err := fixture.attempts.ReleaseResources(fixture.ctx, domain.ReleaseResourcesInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken,
	})
	if err != nil {
		t.Fatalf("ReleaseResources(all): %v", err)
	}
	if len(released.Reservations) != 2 {
		t.Fatalf("released = %d, want 2", len(released.Reservations))
	}

	repository, err := sqlite.NewReservationRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	active, err := repository.ListActiveReservations(fixture.ctx, ports.ListActiveReservationsQuery{AttemptID: claim.Attempt.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("active reservations after release-all = %d, want 0", len(active))
	}

	// Releasing again with no active reservations left is a no-op, not an error.
	again, err := fixture.attempts.ReleaseResources(fixture.ctx, domain.ReleaseResourcesInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken,
	})
	if err != nil {
		t.Fatalf("ReleaseResources(all) on an empty set: %v", err)
	}
	if len(again.Reservations) != 0 {
		t.Fatalf("released on empty set = %d, want 0", len(again.Reservations))
	}
}

// A named reservation ID owned by a different attempt is rejected as not
// found -- release_resources never releases another attempt's reservation.
func TestReleaseResourcesRejectsReservationNotOwned(t *testing.T) {
	fixture := newAttemptTestFixture(t, "release-resources-not-owned")
	defer fixture.close()
	owner := claimReservationFixture(t, fixture, "owner")
	other := claimReservationFixture(t, fixture, "other")
	reserved, err := fixture.attempts.ReserveResources(fixture.ctx, domain.ReserveResourcesInput{
		AttemptID: owner.Attempt.ID, LeaseToken: owner.LeaseToken,
		Resources: []domain.Resource{{Kind: domain.ResourceKindFile, Path: "owned.go"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = fixture.attempts.ReleaseResources(fixture.ctx, domain.ReleaseResourcesInput{
		AttemptID: other.Attempt.ID, LeaseToken: other.LeaseToken, ReservationIDs: []string{reserved.Reservations[0].ID},
	})
	assertDomainCode(t, err, domain.CodeReservationNotFound)

	repository, err := sqlite.NewReservationRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	active, err := repository.ListActiveReservations(fixture.ctx, ports.ListActiveReservationsQuery{AttemptID: owner.Attempt.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("owner's reservation must remain active after a rejected cross-attempt release, got %d", len(active))
	}
}

func TestReleaseResourcesRejectsAlreadyReleased(t *testing.T) {
	fixture := newAttemptTestFixture(t, "release-resources-not-active")
	defer fixture.close()
	claim := claimReservationFixture(t, fixture, "already released")
	reserved, err := fixture.attempts.ReserveResources(fixture.ctx, domain.ReserveResourcesInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken,
		Resources: []domain.Resource{{Kind: domain.ResourceKindFile, Path: "once.go"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.attempts.ReleaseResources(fixture.ctx, domain.ReleaseResourcesInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken, ReservationIDs: []string{reserved.Reservations[0].ID},
	}); err != nil {
		t.Fatal(err)
	}

	_, err = fixture.attempts.ReleaseResources(fixture.ctx, domain.ReleaseResourcesInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken, ReservationIDs: []string{reserved.Reservations[0].ID},
	})
	assertDomainCode(t, err, domain.CodeReservationNotActive)
}

func TestReleaseResourcesIdempotentReplay(t *testing.T) {
	fixture := newAttemptTestFixture(t, "release-resources-idempotent")
	defer fixture.close()
	claim := claimReservationFixture(t, fixture, "idempotent release")
	reserved, err := fixture.attempts.ReserveResources(fixture.ctx, domain.ReserveResourcesInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken,
		Resources: []domain.Resource{{Kind: domain.ResourceKindFile, Path: "once.go"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	key := "release-key-1"
	input := domain.ReleaseResourcesInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken,
		ReservationIDs: []string{reserved.Reservations[0].ID}, IdempotencyKey: &key,
	}

	first, err := fixture.attempts.ReleaseResources(fixture.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.attempts.ReleaseResources(fixture.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Reservations) != 1 || len(second.Reservations) != 1 || first.Reservations[0].ID != second.Reservations[0].ID {
		t.Fatalf("release replay mismatch: first=%+v second=%+v", first, second)
	}
}

// release_resources' reservation_released events must record the caller's
// session, matching acquireReservationsForAttempt's reservation_reserved
// events, so an operator auditing issue_events can attribute an explicit
// release to the session that requested it.
func TestReleaseResourcesRecordsCallerSessionOnEvent(t *testing.T) {
	fixture := newAttemptTestFixture(t, "release-resources-session")
	defer fixture.close()
	claim := claimReservationFixture(t, fixture, "release session")
	reserved, err := fixture.attempts.ReserveResources(fixture.ctx, domain.ReserveResourcesInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken,
		Resources: []domain.Resource{{Kind: domain.ResourceKindFile, Path: "once.go"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	releaseSessionID := fixture.newID(t)
	if err := fixture.db.Write(fixture.ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_sessions(id, client_name, started_at, last_seen_at) VALUES (?, 'release-test', ?, ?)`,
			releaseSessionID, sqlite.FormatStorageTime(fixture.clock.Now()), sqlite.FormatStorageTime(fixture.clock.Now()))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.attempts.ReleaseResources(fixture.ctx, domain.ReleaseResourcesInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken, SessionID: &releaseSessionID,
		ReservationIDs: []string{reserved.Reservations[0].ID},
	}); err != nil {
		t.Fatal(err)
	}

	var storedSessionID *string
	if err := fixture.db.Read(fixture.ctx, func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT session_id FROM issue_events
			WHERE attempt_id = ? AND event_type = 'reservation_released'`, claim.Attempt.ID).Scan(&storedSessionID)
	}); err != nil {
		t.Fatal(err)
	}
	if storedSessionID == nil || *storedSessionID != releaseSessionID {
		t.Fatalf("reservation_released session_id = %v, want %s", storedSessionID, releaseSessionID)
	}
}

// ISSUE-180 AC3: list_resource_reservations filters by issue, attempt, kind,
// and active state, and paginates deterministically.
func TestListReservationsFiltersAndPaginates(t *testing.T) {
	fixture := newAttemptTestFixture(t, "list-reservations")
	defer fixture.close()
	claimA := claimReservationFixture(t, fixture, "list a")
	claimB := claimReservationFixture(t, fixture, "list b")
	repository, err := sqlite.NewReservationRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcquireReservations(fixture.ctx, acquireCommand(fixture, t, claimA.Issue.ID, claimA.Attempt.ID,
		domain.Resource{Kind: domain.ResourceKindFile, Path: "a1.go"})); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcquireReservations(fixture.ctx, acquireCommand(fixture, t, claimA.Issue.ID, claimA.Attempt.ID,
		domain.Resource{Kind: domain.ResourceKindLogical, Namespace: "docs", Name: "rfc"})); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcquireReservations(fixture.ctx, acquireCommand(fixture, t, claimB.Issue.ID, claimB.Attempt.ID,
		domain.Resource{Kind: domain.ResourceKindFile, Path: "b1.go"})); err != nil {
		t.Fatal(err)
	}
	releasedID := claimB.Attempt.ID
	all, err := repository.ListActiveReservations(fixture.ctx, ports.ListActiveReservationsQuery{AttemptID: releasedID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ReleaseReservation(fixture.ctx, ports.ReleaseReservationCommand{
		ID: all[0].ID, ExpectedVersion: all[0].Version, Reason: domain.ReservationReleaseReasonExplicit, OccurredAt: fixture.clock.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	issueID := claimA.Issue.ID
	byIssue, err := repository.ListReservations(fixture.ctx, ports.ListReservationsCommand{
		Input: domain.ListResourceReservationsInput{IssueID: &issueID, Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(byIssue.Items) != 2 {
		t.Fatalf("by issue = %d, want 2", len(byIssue.Items))
	}

	fileKind := domain.ResourceKindFile
	byKind, err := repository.ListReservations(fixture.ctx, ports.ListReservationsCommand{
		Input: domain.ListResourceReservationsInput{IssueID: &issueID, Kind: &fileKind, Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(byKind.Items) != 1 || byKind.Items[0].DisplayValue != "a1.go" {
		t.Fatalf("by kind = %+v, want just a1.go", byKind.Items)
	}

	active := true
	byActive, err := repository.ListReservations(fixture.ctx, ports.ListReservationsCommand{
		Input: domain.ListResourceReservationsInput{Active: &active, Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(byActive.Items) != 2 {
		t.Fatalf("active-only across both issues = %d, want 2 (a1.go, docs:rfc)", len(byActive.Items))
	}
	released := false
	byReleased, err := repository.ListReservations(fixture.ctx, ports.ListReservationsCommand{
		Input: domain.ListResourceReservationsInput{Active: &released, Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(byReleased.Items) != 1 || byReleased.Items[0].DisplayValue != "b1.go" {
		t.Fatalf("released-only = %+v, want just b1.go", byReleased.Items)
	}

	page1, err := repository.ListReservations(fixture.ctx, ports.ListReservationsCommand{
		Input: domain.ListResourceReservationsInput{IssueID: &issueID, Limit: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1.Items) != 1 || !page1.HasMore || page1.NextCursor == nil {
		t.Fatalf("page1 = %+v, want 1 item with more", page1)
	}
	page2, err := repository.ListReservations(fixture.ctx, ports.ListReservationsCommand{
		Input: domain.ListResourceReservationsInput{IssueID: &issueID, Limit: 1, Cursor: *page1.NextCursor},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Items) != 1 || page2.HasMore {
		t.Fatalf("page2 = %+v, want the final 1 item", page2)
	}
	if page1.Items[0].ID == page2.Items[0].ID {
		t.Fatalf("page1 and page2 returned the same reservation %s", page1.Items[0].ID)
	}
}

func TestGetReservationFoundAndNotFound(t *testing.T) {
	fixture := newAttemptTestFixture(t, "get-reservation")
	defer fixture.close()
	claim := claimReservationFixture(t, fixture, "get")
	repository, err := sqlite.NewReservationRepository(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := repository.AcquireReservations(fixture.ctx, acquireCommand(fixture, t, claim.Issue.ID, claim.Attempt.ID,
		domain.Resource{Kind: domain.ResourceKindFile, Path: "get.go"}))
	if err != nil {
		t.Fatal(err)
	}

	got, err := repository.GetReservation(fixture.ctx, acquired[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != acquired[0].ID || got.DisplayValue != "get.go" {
		t.Fatalf("GetReservation = %+v, want %+v", got, acquired[0])
	}

	_, err = repository.GetReservation(fixture.ctx, fixture.newID(t))
	assertDomainCode(t, err, domain.CodeReservationNotFound)
}
