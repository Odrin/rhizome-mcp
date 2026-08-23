package sqlite_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/migrations"
	"rhizome-mcp/internal/ports"
)

// newActivityReservationFixture wires up the same sqlite-backed services as
// activity_gate_evidence_test.go, plus a reservation repository/service, so
// ISSUE-182's reservation activity entries can be exercised end to end
// (claim -> reservation -> get_issue_activity).
func newActivityReservationFixture(t *testing.T, now time.Time) (*application.IssueService, *application.AttemptService, *sqlite.ActivityRepository, *clock.FakeClock, context.Context) {
	t.Helper()
	ctx := context.Background()
	db := openTestDB(t, filepath.Join(t.TempDir(), "activity-reservation.db"), true)
	fakeClock := clock.NewFakeClock(now)
	if _, err := migrations.Migrate(ctx, db, fakeClock); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id, next_issue_number, created_at, updated_at) VALUES (?, 1, ?, ?)`,
			activityTestProjectID, sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(now))
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	generator, err := ids.NewGenerator(fakeClock, rand.Reader)
	if err != nil {
		t.Fatalf("NewGenerator() error = %v", err)
	}
	issueRepository, err := sqlite.NewIssueRepository(db)
	if err != nil {
		t.Fatalf("NewIssueRepository() error = %v", err)
	}
	issues, err := application.NewIssueService(issueRepository, fakeClock, generator)
	if err != nil {
		t.Fatalf("NewIssueService() error = %v", err)
	}
	attemptRepository, err := sqlite.NewAttemptRepository(db)
	if err != nil {
		t.Fatalf("NewAttemptRepository() error = %v", err)
	}
	attempts, err := application.NewAttemptService(attemptRepository, fakeClock, generator)
	if err != nil {
		t.Fatalf("NewAttemptService() error = %v", err)
	}
	activityRepository, err := sqlite.NewActivityRepository(db)
	if err != nil {
		t.Fatalf("NewActivityRepository() error = %v", err)
	}
	return issues, attempts, activityRepository, fakeClock, ctx
}

// TestActivityRepositoryIncludesReservation proves a claimed reservation
// surfaces in the unfiltered get_issue_activity feed under the "reservation"
// entity type, wrapping a bounded domain.ReservationSummary whose JSON
// carries no comparison_value, normalized_json, or version -- the internal
// fields SummarizeReservation strips (ISSUE-181/ISSUE-182).
func TestActivityRepositoryIncludesReservation(t *testing.T) {
	now := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)
	issues, attempts, activityRepository, _, ctx := newActivityReservationFixture(t, now)

	issue, err := issues.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "reservation activity", Status: domain.StatusReady})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}
	claim, err := attempts.ClaimIssue(ctx, domain.ClaimIssueInput{
		IssueID:   issue.ID,
		Resources: []domain.Resource{{Kind: domain.ResourceKindFile, Path: "reservation_activity.go"}},
	})
	if err != nil {
		t.Fatalf("ClaimIssue() error = %v", err)
	}
	if len(claim.Reservations) != 1 {
		t.Fatalf("reservations = %d, want 1", len(claim.Reservations))
	}
	reservationID := claim.Reservations[0].ID

	unfiltered, err := activityRepository.GetIssueActivity(ctx, ports.GetIssueActivityCommand{
		Input: domain.GetIssueActivityInput{IssueID: issue.ID, Limit: 20},
	})
	if err != nil {
		t.Fatalf("GetIssueActivity() error = %v", err)
	}
	var found *domain.ActivityItem
	for index := range unfiltered.Items {
		if unfiltered.Items[index].EntityType == domain.ActivityEntityTypeReservation {
			found = &unfiltered.Items[index]
		}
	}
	if found == nil {
		t.Fatalf("unfiltered activity feed = %#v, want a reservation entry included by default", unfiltered.Items)
	}
	if found.Reservation == nil || found.Reservation.ID != reservationID || found.Reservation.DisplayValue != "reservation_activity.go" {
		t.Fatalf("reservation activity item = %#v, want it to wrap the claimed reservation", found)
	}

	encoded, err := json.Marshal(found)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	payload := string(encoded)
	if !strings.Contains(payload, `"reservation"`) {
		t.Fatalf("activity item JSON = %s, want a reservation payload", payload)
	}
	for _, forbidden := range []string{"comparison_value", "normalized_json", "\"version\""} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("activity item JSON = %s, must not contain %q", payload, forbidden)
		}
	}
}

// TestActivityRepositoryFiltersToReservationsCategory proves categories:
// ["reservations"] returns exactly the reservation items, excluding the
// attempt entry the same claim also produces.
func TestActivityRepositoryFiltersToReservationsCategory(t *testing.T) {
	now := time.Date(2026, 8, 23, 9, 30, 0, 0, time.UTC)
	issues, attempts, activityRepository, _, ctx := newActivityReservationFixture(t, now)

	issue, err := issues.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "reservation category filter", Status: domain.StatusReady})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}
	claim, err := attempts.ClaimIssue(ctx, domain.ClaimIssueInput{
		IssueID: issue.ID,
		Resources: []domain.Resource{
			{Kind: domain.ResourceKindFile, Path: "filter_a.go"},
			{Kind: domain.ResourceKindFile, Path: "filter_b.go"},
		},
	})
	if err != nil {
		t.Fatalf("ClaimIssue() error = %v", err)
	}
	if len(claim.Reservations) != 2 {
		t.Fatalf("reservations = %d, want 2", len(claim.Reservations))
	}

	filtered, err := activityRepository.GetIssueActivity(ctx, ports.GetIssueActivityCommand{
		Input: domain.GetIssueActivityInput{IssueID: issue.ID, Types: []domain.ActivityCategory{domain.ActivityCategoryReservations}, Limit: 20},
	})
	if err != nil {
		t.Fatalf("GetIssueActivity() filtered error = %v", err)
	}
	if len(filtered.Items) != 2 {
		t.Fatalf("filtered activity feed = %#v, want exactly the two reservation entries", filtered.Items)
	}
	for _, item := range filtered.Items {
		if item.EntityType != domain.ActivityEntityTypeReservation || item.Reservation == nil {
			t.Fatalf("filtered activity item = %#v, want only reservation entries", item)
		}
	}
}

// TestActivityRepositoryReservationSortsByReleasedAt proves occurred_at for
// a released reservation is COALESCE(released_at, created_at): both
// reservations here share the same created_at, so only using released_at
// for the released one separates their ordering.
func TestActivityRepositoryReservationSortsByReleasedAt(t *testing.T) {
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	issues, attempts, activityRepository, fakeClock, ctx := newActivityReservationFixture(t, now)

	issue, err := issues.CreateIssue(ctx, domain.CreateIssueInput{Type: domain.TypeTask, Title: "reservation release ordering", Status: domain.StatusReady})
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}
	claim, err := attempts.ClaimIssue(ctx, domain.ClaimIssueInput{
		IssueID: issue.ID,
		Resources: []domain.Resource{
			{Kind: domain.ResourceKindFile, Path: "released.go"},
			{Kind: domain.ResourceKindFile, Path: "still_active.go"},
		},
	})
	if err != nil {
		t.Fatalf("ClaimIssue() error = %v", err)
	}
	if len(claim.Reservations) != 2 {
		t.Fatalf("reservations = %d, want 2", len(claim.Reservations))
	}
	var releasedID, activeID string
	for _, reservation := range claim.Reservations {
		if reservation.DisplayValue == "released.go" {
			releasedID = reservation.ID
		} else {
			activeID = reservation.ID
		}
	}
	if releasedID == "" || activeID == "" {
		t.Fatalf("reservations = %#v, want one for each resource", claim.Reservations)
	}

	releasedAt := now.Add(5 * time.Minute)
	fakeClock.Advance(5 * time.Minute)
	if _, err := attempts.ReleaseResources(ctx, domain.ReleaseResourcesInput{
		AttemptID: claim.Attempt.ID, LeaseToken: claim.LeaseToken, ReservationIDs: []string{releasedID},
	}); err != nil {
		t.Fatalf("ReleaseResources() error = %v", err)
	}

	activity, err := activityRepository.GetIssueActivity(ctx, ports.GetIssueActivityCommand{
		Input: domain.GetIssueActivityInput{IssueID: issue.ID, Types: []domain.ActivityCategory{domain.ActivityCategoryReservations}, Limit: 20},
	})
	if err != nil {
		t.Fatalf("GetIssueActivity() error = %v", err)
	}
	if len(activity.Items) != 2 {
		t.Fatalf("reservation activity items = %#v, want 2", activity.Items)
	}
	// Newest-first: the released reservation's occurred_at must be its
	// released_at (now+5m), ahead of the still-active reservation whose
	// occurred_at is still its shared created_at (now).
	first, second := activity.Items[0], activity.Items[1]
	if first.EntityID != releasedID || !first.OccurredAt.Equal(releasedAt) {
		t.Fatalf("first activity item = %#v, want the released reservation at released_at %v", first, releasedAt)
	}
	if second.EntityID != activeID || !second.OccurredAt.Equal(now) {
		t.Fatalf("second activity item = %#v, want the still-active reservation at created_at %v", second, now)
	}
	if first.Reservation == nil || first.Reservation.Status != domain.ReservationStatusReleased || first.Reservation.ReleasedAt == nil {
		t.Fatalf("released reservation summary = %#v, want status released with a released_at", first.Reservation)
	}
	if second.Reservation == nil || second.Reservation.Status != domain.ReservationStatusActive {
		t.Fatalf("active reservation summary = %#v, want status active", second.Reservation)
	}
}
