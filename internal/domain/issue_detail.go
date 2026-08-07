package domain

// IssueDetail is a bounded, documented projection for one served issue page.
type IssueDetail struct {
	Issue                Issue
	RootIssueProjection  *IssueProjection
	Graph                GraphResult
	Activity             IssueActivity
	LatestAttempt        *WorkAttempt
	OpenReview           *ReviewRequest
	LatestDecision       *Decision
}
