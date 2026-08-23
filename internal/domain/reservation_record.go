package domain

import "time"

const (
	// DefaultReservationHistoryLimit bounds ListReservationHistory when the
	// caller does not specify one.
	DefaultReservationHistoryLimit = 20
	// MaxReservationHistoryLimit bounds ListReservationHistory's largest
	// accepted explicit limit.
	MaxReservationHistoryLimit = 100
)

// ReservationStatus is the persisted lifecycle state of one reservation.
type ReservationStatus string

const (
	ReservationStatusActive   ReservationStatus = "active"
	ReservationStatusReleased ReservationStatus = "released"
)

// ReservationReleaseReason explains why an active reservation stopped being
// active. Set only on released reservations.
type ReservationReleaseReason string

const (
	ReservationReleaseReasonCompleted     ReservationReleaseReason = "completed"
	ReservationReleaseReasonFailed        ReservationReleaseReason = "failed"
	ReservationReleaseReasonInterrupted   ReservationReleaseReason = "interrupted"
	ReservationReleaseReasonExpired       ReservationReleaseReason = "expired"
	ReservationReleaseReasonForceReleased ReservationReleaseReason = "force_released"
	ReservationReleaseReasonExplicit      ReservationReleaseReason = "explicit"
)

// Valid reports whether reason is one of the six locked release reasons.
func (reason ReservationReleaseReason) Valid() bool {
	switch reason {
	case ReservationReleaseReasonCompleted, ReservationReleaseReasonFailed, ReservationReleaseReasonInterrupted,
		ReservationReleaseReasonExpired, ReservationReleaseReasonForceReleased, ReservationReleaseReasonExplicit:
		return true
	default:
		return false
	}
}

// Reservation is one persisted resource_reservations row: an exclusive claim
// on a normalized resource, held by one work attempt for as long as that
// attempt stays active. See docs/02 §18 for the resource contract this
// builds on, and ISSUE-178 for the persistence contract.
type Reservation struct {
	ID              string
	IssueID         string
	AttemptID       string
	Kind            ResourceKind
	DisplayValue    string
	ComparisonValue string
	Status          ReservationStatus
	Version         int64
	CreatedAt       time.Time
	ReleasedAt      *time.Time
	ReleaseReason   *ReservationReleaseReason
}

// CloneReservation returns a defensive copy of r.
func CloneReservation(r Reservation) Reservation {
	clone := r
	if r.ReleasedAt != nil {
		releasedAt := *r.ReleasedAt
		clone.ReleasedAt = &releasedAt
	}
	if r.ReleaseReason != nil {
		reason := *r.ReleaseReason
		clone.ReleaseReason = &reason
	}
	return clone
}
