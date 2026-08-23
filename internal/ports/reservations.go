package ports

import (
	"context"
	"time"

	"rhizome-mcp/internal/domain"
)

// ReservationResourceInput is one requested resource, paired with the
// identifier the caller has already generated for its resulting
// reservation row (ID generation is an application-layer concern, matching
// every other write command in this package).
type ReservationResourceInput struct {
	ID       string
	Resource domain.Resource
}

// AcquireReservationsCommand requests an all-or-nothing acquisition of one
// or more resources on behalf of one active work attempt.
type AcquireReservationsCommand struct {
	IssueID        string
	AttemptID      string
	SessionID      *string
	Resources      []ReservationResourceInput
	OccurredAt     time.Time
	IdempotencyKey string
	RequestHash    []byte
}

// ReleaseReservationCommand releases one active reservation.
type ReleaseReservationCommand struct {
	ID              string
	ExpectedVersion int64
	Reason          domain.ReservationReleaseReason
	OccurredAt      time.Time
}

// ListActiveReservationsQuery filters the active reservation set. An empty
// IssueID or AttemptID means "no filter" on that field.
type ListActiveReservationsQuery struct {
	IssueID   string
	AttemptID string
}

// ListReservationHistoryQuery filters released reservations. An empty
// IssueID or AttemptID means "no filter" on that field; Limit of 0 uses
// domain.DefaultReservationHistoryLimit.
type ListReservationHistoryQuery struct {
	IssueID   string
	AttemptID string
	Limit     int
}

// ListReservationsCommand carries the already-validated filter and
// pagination input for list_resource_reservations (ISSUE-180). Unlike
// ListActiveReservationsQuery/ListReservationHistoryQuery, this covers both
// lifecycle states and cursor pagination in one call.
type ListReservationsCommand struct {
	Input domain.ListResourceReservationsInput
}

// ReservationRepository persists resource reservations and enforces
// conflict-free acquisition transactionally, per ISSUE-178's locked schema.
type ReservationRepository interface {
	// AcquireReservations normalizes, deduplicates, and validates command's
	// resources (domain.PrepareReservationRequest), then -- inside one write
	// transaction -- checks every candidate against every currently active
	// reservation and, only if none overlap, inserts all of them and appends
	// one issue event per reservation. It returns
	// domain.CodeInvalidReservationSet for a malformed internally-overlapping
	// request and domain.CodeResourceReservationConflict for the first
	// external conflict found, in a deterministic order.
	AcquireReservations(context.Context, AcquireReservationsCommand) ([]domain.Reservation, error)
	// LookupAcquireReservations serves an idempotent replay before a caller
	// starts a fresh acquisition transaction. AcquireReservations still
	// repeats this check in its writer transaction to close the lookup/write
	// race.
	LookupAcquireReservations(ctx context.Context, key string, hash []byte) ([]domain.Reservation, bool, error)
	// ReleaseReservation transitions one active reservation to released,
	// guarded by ExpectedVersion.
	ReleaseReservation(context.Context, ReleaseReservationCommand) (domain.Reservation, error)
	// ListActiveReservations returns active reservations ordered by id
	// ascending.
	ListActiveReservations(context.Context, ListActiveReservationsQuery) ([]domain.Reservation, error)
	// ListReservationHistory returns released reservations ordered by
	// released_at descending, then id descending.
	ListReservationHistory(context.Context, ListReservationHistoryQuery) ([]domain.Reservation, error)
	// ListReservations serves list_resource_reservations (ISSUE-180): issue,
	// attempt, kind, and active-state filtering with cursor pagination
	// across both active and released reservations.
	ListReservations(context.Context, ListReservationsCommand) (domain.ReservationList, error)
	// GetReservation loads one reservation, active or released, by id.
	GetReservation(context.Context, string) (domain.Reservation, error)
}
