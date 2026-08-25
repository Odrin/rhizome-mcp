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
	// Gates is the issue's compact workflow-gate summary -- the same
	// projection get_work_context carries -- so a human inspecting the
	// issue page sees exactly what an agent sees in context (ISSUE-175
	// AC2). Always populated; a project with no matching policies reports
	// requirement_count 0 and an empty unmet list.
	Gates WorkContextGateSummary
}
