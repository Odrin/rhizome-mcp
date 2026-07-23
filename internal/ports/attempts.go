package ports

import (
	"context"
	"time"

	"rhizome-mcp/internal/domain"
)

type ClaimIssueCommand struct {
	Identifier     domain.IssueIdentifier
	AttemptID      string
	SessionID      *string
	TokenHash      []byte
	LeaseToken     string
	LeaseDuration  time.Duration
	OccurredAt     time.Time
	IdempotencyKey string
	RequestHash    []byte
}

type ClaimIssueResult struct {
	Issue      domain.Issue
	Attempt    domain.WorkAttempt
	LeaseToken string
}

type RenewAttemptCommand struct {
	AttemptID     string
	SessionID     *string
	TokenHash     []byte
	LeaseDuration time.Duration
	OccurredAt    time.Time
}

type RenewAttemptResult struct {
	LeaseExpiresAt time.Time
	ServerTime     time.Time
}

type SaveAttemptNoteCommand struct {
	NoteID         string
	AttemptID      string
	SessionID      *string
	TokenHash      []byte
	Kind           domain.AttemptNoteKind
	Content        string
	NextSteps      []string
	Important      bool
	Artifacts      []domain.Artifact
	OccurredAt     time.Time
	IdempotencyKey string
	RequestHash    []byte
}

type SaveAttemptNoteResult struct {
	Note      domain.AttemptNote
	Artifacts []domain.Artifact
}

type FinishAttemptCommand struct {
	AttemptID      string
	SessionID      *string
	TokenHash      []byte
	Input          domain.FinishAttemptInput
	Artifacts      []domain.Artifact
	IdempotencyKey string
	RequestHash    []byte
	OccurredAt     time.Time
}

type FinishAttemptResult struct {
	Attempt       domain.WorkAttempt
	Issue         domain.Issue
	Warnings      []string
	LatestEventID int64
	Artifacts     []domain.Artifact
}

type ForceReleaseAttemptCommand struct {
	AttemptID  string
	OccurredAt time.Time
}

type ForceReleaseAttemptResult struct {
	Attempt       domain.WorkAttempt
	LatestEventID int64
}

type ExpireAttemptsCommand struct {
	OccurredAt time.Time
}

type ExpireAttemptsResult struct {
	ExpiredAttemptCount int
}

// AttemptRepository executes all attempt lifecycle mutations atomically.
type AttemptRepository interface {
	ClaimIssue(context.Context, ClaimIssueCommand) (ClaimIssueResult, error)
	LookupClaimIssue(context.Context, string, []byte) (ClaimIssueResult, bool, error)
	RenewAttempt(context.Context, RenewAttemptCommand) (RenewAttemptResult, error)
	SaveAttemptNote(context.Context, SaveAttemptNoteCommand) (SaveAttemptNoteResult, error)
	LookupSaveAttemptNote(context.Context, string, []byte) (SaveAttemptNoteResult, bool, error)
	LookupFinishedAttempt(context.Context, string, []byte) (FinishAttemptResult, bool, error)
	FinishAttempt(context.Context, FinishAttemptCommand) (FinishAttemptResult, error)
	ForceReleaseAttempt(context.Context, ForceReleaseAttemptCommand) (ForceReleaseAttemptResult, error)
	ExpireAttempts(context.Context, ExpireAttemptsCommand) (ExpireAttemptsResult, error)
}
