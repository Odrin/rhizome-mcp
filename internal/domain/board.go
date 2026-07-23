package domain

import "time"

// EffectiveStatusCount is one bounded aggregate count of issues in a single
// effective status. There is one row per possible effective status, so the
// result set size is fixed regardless of backlog size.
type EffectiveStatusCount struct {
	EffectiveStatus EffectiveStatus `json:"effective_status"`
	Count           int64           `json:"count"`
}

// ActiveAttemptSummary is one bounded, project-wide active (leased) attempt
// projection used for read-only status displays.
type ActiveAttemptSummary struct {
	AttemptID      string      `json:"attempt_id"`
	IssueID        string      `json:"issue_id"`
	IssueDisplayID string      `json:"issue_display_id"`
	IssueTitle     string      `json:"issue_title"`
	Kind           AttemptKind `json:"kind"`
	SessionID      *string     `json:"session_id,omitempty"`
	SessionLabel   *string     `json:"session_label,omitempty"`
	StartedAt      time.Time   `json:"started_at"`
	LeaseExpiresAt time.Time   `json:"lease_expires_at"`
}

// BoardResult is the bounded read-only project status board aggregate: issue
// counts by effective status, currently leased attempts, blocked issues, open
// review requests, and the planning graph. Every field is already bounded by
// the collaborating services and repositories that produced it.
type BoardResult struct {
	GeneratedAt    time.Time              `json:"generated_at"`
	StatusCounts   []EffectiveStatusCount `json:"status_counts"`
	ActiveAttempts []ActiveAttemptSummary `json:"active_attempts"`
	BlockedIssues  []IssueProjection      `json:"blocked_issues"`
	ReviewRequests []ReviewRequest        `json:"review_requests"`
	PlanningGraph  GraphResult            `json:"planning_graph"`
}
