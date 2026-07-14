package ports

import (
	"context"
	"time"

	"rhizome-mcp/internal/domain"
)

type ClaimIssueCommand struct {
	Identifier    domain.IssueIdentifier
	AttemptID     string
	TokenHash     []byte
	LeaseDuration time.Duration
	OccurredAt    time.Time
}

type ClaimIssueResult struct {
	Issue   domain.Issue
	Attempt domain.WorkAttempt
}

type RenewAttemptCommand struct {
	AttemptID     string
	TokenHash     []byte
	LeaseDuration time.Duration
	OccurredAt    time.Time
}

type RenewAttemptResult struct {
	LeaseExpiresAt time.Time
	ServerTime     time.Time
}

// AttemptRepository executes all attempt lifecycle mutations atomically.
type AttemptRepository interface {
	ClaimIssue(context.Context, ClaimIssueCommand) (ClaimIssueResult, error)
	RenewAttempt(context.Context, RenewAttemptCommand) (RenewAttemptResult, error)
}
