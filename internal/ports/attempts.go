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

type SaveAttemptNoteCommand struct {
	NoteID     string
	AttemptID  string
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
	TokenHash  []byte
	Input      domain.FinishAttemptInput
	OccurredAt time.Time
}

type FinishAttemptResult struct {
	Attempt       domain.WorkAttempt
	Issue         domain.Issue
	Warnings      []string
	LatestEventID int64
}

// AttemptRepository executes all attempt lifecycle mutations atomically.
type AttemptRepository interface {
	ClaimIssue(context.Context, ClaimIssueCommand) (ClaimIssueResult, error)
	RenewAttempt(context.Context, RenewAttemptCommand) (RenewAttemptResult, error)
	SaveAttemptNote(context.Context, SaveAttemptNoteCommand) (SaveAttemptNoteResult, error)
	FinishAttempt(context.Context, FinishAttemptCommand) (FinishAttemptResult, error)
}
