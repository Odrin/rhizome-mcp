package domain

import (
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

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

// ReservationSummary is Reservation without its internal-only normalized
// comparison key and optimistic-concurrency version -- neither useful
// outside a full single-item lookup, matching the MCP compact reservation
// shape's same exclusion. Surfaces (like the served issue-detail JSON API)
// that marshal a domain projection directly, with no per-field DTO
// conversion, must use this type rather than Reservation so a comparison
// key never reaches an external response (ISSUE-181's "MCP/board
// projections exclude comparison keys" requirement).
type ReservationSummary struct {
	ID            string
	IssueID       string
	AttemptID     string
	Kind          ResourceKind
	DisplayValue  string
	Status        ReservationStatus
	CreatedAt     time.Time
	ReleasedAt    *time.Time
	ReleaseReason *ReservationReleaseReason
}

// SummarizeReservation drops Reservation's internal-only fields.
func SummarizeReservation(reservation Reservation) ReservationSummary {
	return ReservationSummary{
		ID: reservation.ID, IssueID: reservation.IssueID, AttemptID: reservation.AttemptID,
		Kind: reservation.Kind, DisplayValue: reservation.DisplayValue, Status: reservation.Status,
		CreatedAt: reservation.CreatedAt, ReleasedAt: reservation.ReleasedAt, ReleaseReason: reservation.ReleaseReason,
	}
}

// SummarizeReservations maps SummarizeReservation over a slice.
func SummarizeReservations(reservations []Reservation) []ReservationSummary {
	summaries := make([]ReservationSummary, len(reservations))
	for index, reservation := range reservations {
		summaries[index] = SummarizeReservation(reservation)
	}
	return summaries
}

// ListResourceReservationsInput filters and paginates the reservation list
// across both active and released reservations (ISSUE-180:
// list_resource_reservations). All filters are optional and combine with AND.
type ListResourceReservationsInput struct {
	IssueID   *string
	AttemptID *string
	Kind      *ResourceKind
	// Active selects lifecycle state: nil returns both active and released
	// reservations; a non-nil value restricts to just that state.
	Active *bool
	Limit  int
	Cursor string
}

func (input ListResourceReservationsInput) Validate() (ListResourceReservationsInput, error) {
	if input.Limit < 0 || input.Limit > MaxReservationHistoryLimit {
		return ListResourceReservationsInput{}, validationError("limit", "OUT_OF_RANGE",
			fmt.Sprintf("must be 0 (default) or between 1 and %d", MaxReservationHistoryLimit))
	}
	issueID, err := normalizeOptionalSearchIssueID("issue_id", input.IssueID)
	if err != nil {
		return ListResourceReservationsInput{}, err
	}
	var attemptID *string
	if input.AttemptID != nil {
		parsed, parseErr := ulid.ParseStrict(*input.AttemptID)
		if parseErr != nil || len(*input.AttemptID) != 26 || parsed.String() != *input.AttemptID {
			return ListResourceReservationsInput{}, validationError("attempt_id", "INVALID_ULID", "must be a canonical ULID")
		}
		value := *input.AttemptID
		attemptID = &value
	}
	if input.Kind != nil && !input.Kind.Valid() {
		return ListResourceReservationsInput{}, invalidEnum("kind", string(*input.Kind))
	}
	if input.Cursor != "" {
		if err := ValidateText("cursor", input.Cursor, 4096); err != nil {
			return ListResourceReservationsInput{}, err
		}
	}
	limit := input.Limit
	if limit == 0 {
		limit = DefaultReservationHistoryLimit
	}
	return ListResourceReservationsInput{
		IssueID: issueID, AttemptID: attemptID, Kind: input.Kind, Active: input.Active, Limit: limit, Cursor: input.Cursor,
	}, nil
}

// ReservationList is one paginated page of ListResourceReservationsInput.
type ReservationList struct {
	Items      []Reservation
	NextCursor *string
	HasMore    bool
}

// CloneReservationList returns a defensive copy of list.
func CloneReservationList(list ReservationList) ReservationList {
	clone := ReservationList{HasMore: list.HasMore}
	if list.Items != nil {
		clone.Items = make([]Reservation, len(list.Items))
		for index, item := range list.Items {
			clone.Items[index] = CloneReservation(item)
		}
	}
	if list.NextCursor != nil {
		cursor := *list.NextCursor
		clone.NextCursor = &cursor
	}
	return clone
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
