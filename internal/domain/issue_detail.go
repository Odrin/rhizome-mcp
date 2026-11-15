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

// AssembleIssueDetail derives RootIssueProjection, latest activity highlights,
// and redacted reservation summaries from the loaded issue detail sections.
func AssembleIssueDetail(issue Issue, graph GraphResult, activity IssueActivity, reservations []Reservation, hasMoreReservations bool, gates WorkContextGateSummary) (IssueDetail, error) {
	var rootProjection *IssueProjection
	if graph.RootIssueID != nil {
		for _, node := range graph.Nodes {
			if node.ID == *graph.RootIssueID {
				projection := node
				rootProjection = &projection
				break
			}
		}
	}
	if rootProjection == nil {
		effectiveStatus, err := EffectiveStatusFor(issue.Status, false)
		if err != nil {
			return IssueDetail{}, err
		}
		rootProjection = &IssueProjection{Issue: issue, EffectiveStatus: effectiveStatus}
	}

	var latestAttempt *WorkAttempt
	var openReview *ReviewRequest
	var latestDecision *Decision
	for _, item := range activity.Items {
		switch {
		case item.Attempt != nil && latestAttempt == nil:
			latestAttempt = item.Attempt
		case item.Review != nil && openReview == nil && item.Review.Status == ReviewRequestStatusOpen:
			openReview = item.Review
		case item.Decision != nil && latestDecision == nil:
			latestDecision = item.Decision
		}
	}

	return IssueDetail{
		Issue:               issue,
		RootIssueProjection: rootProjection,
		Graph:               graph,
		Activity:            activity,
		LatestAttempt:       latestAttempt,
		OpenReview:          openReview,
		LatestDecision:      latestDecision,
		Reservations:        SummarizeReservations(reservations),
		HasMoreReservations: hasMoreReservations,
		Gates:               gates,
	}, nil
}
