package ports

import (
	"context"
	"time"

	"rhizome-mcp/internal/domain"
)

type ClaimIssueCommand struct {
	Identifier    domain.IssueIdentifier
	AttemptID     string
	SessionID     *string
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
	NoteID     string
	AttemptID  string
	SessionID  *string
	TokenHash  []byte
	Kind       domain.AttemptNoteKind
	Content    string
	NextSteps  []string
	Important  bool
	Artifacts  []domain.Artifact
	OccurredAt time.Time
}

type SaveAttemptNoteResult struct {
	Note      domain.AttemptNote
	Artifacts []domain.Artifact
}

type FinishAttemptCommand struct {
	AttemptID  string
	SessionID  *string
	TokenHash  []byte
	Input      domain.FinishAttemptInput
	Artifacts  []domain.Artifact
	OccurredAt time.Time
}

type FinishAttemptResult struct {
	Attempt       domain.WorkAttempt
	Issue         domain.Issue
	Warnings      []string
	LatestEventID int64
	Artifacts     []domain.Artifact
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
	RenewAttempt(context.Context, RenewAttemptCommand) (RenewAttemptResult, error)
	SaveAttemptNote(context.Context, SaveAttemptNoteCommand) (SaveAttemptNoteResult, error)
	FinishAttempt(context.Context, FinishAttemptCommand) (FinishAttemptResult, error)
	ExpireAttempts(context.Context, ExpireAttemptsCommand) (ExpireAttemptsResult, error)
}
