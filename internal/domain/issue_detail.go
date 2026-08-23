package domain

// IssueDetail is a bounded, documented projection for one served issue page.
//
// Reservations is the issue's own reservations, current (active) and
// historical (released), newest first, bounded by
// DefaultReservationHistoryLimit (ISSUE-181: "issue detail shows current
// and historical rows"). ReservationSummary, not Reservation, because this
// struct is marshaled directly as the served issue-detail JSON API's body
// with no further per-field conversion -- Reservation's ComparisonValue
// must never reach that response.
type IssueDetail struct {
	Issue               Issue
	RootIssueProjection *IssueProjection
	Graph               GraphResult
	Activity            IssueActivity
	LatestAttempt       *WorkAttempt
	OpenReview          *ReviewRequest
	LatestDecision      *Decision
	Reservations        []ReservationSummary
	HasMoreReservations bool
}
