package domain

import (
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

const (
	// DefaultLeaseSeconds is used when a lease duration is omitted.
	DefaultLeaseSeconds = 900
	// MinLeaseSeconds and MaxLeaseSeconds bound short-lived attempt leases.
	MinLeaseSeconds = 60
	MaxLeaseSeconds = 3600
	// MaxLeaseTokenRunes bounds an opaque token supplied to renewal.
	MaxLeaseTokenRunes = 512
	// MaxAttemptNoteNextSteps bounds the compact recovery steps stored with a note.
	MaxAttemptNoteNextSteps = 20
	// MaxAttemptNoteNextStepRunes bounds each compact recovery step.
	MaxAttemptNoteNextStepRunes = 1_000
)

type AttemptKind string

const (
	AttemptKindWork   AttemptKind = "work"
	AttemptKindReview AttemptKind = "review"
)

func (value AttemptKind) Valid() bool { return value == AttemptKindWork || value == AttemptKindReview }

type AttemptStatus string

const (
	AttemptStatusActive      AttemptStatus = "active"
	AttemptStatusCompleted   AttemptStatus = "completed"
	AttemptStatusFailed      AttemptStatus = "failed"
	AttemptStatusInterrupted AttemptStatus = "interrupted"
	AttemptStatusExpired     AttemptStatus = "expired"
	AttemptStatusCancelled   AttemptStatus = "cancelled"
)

func (value AttemptStatus) Valid() bool {
	switch value {
	case AttemptStatusActive, AttemptStatusCompleted, AttemptStatusFailed, AttemptStatusInterrupted, AttemptStatusExpired, AttemptStatusCancelled:
		return true
	default:
		return false
	}
}

// WorkAttempt is the durable attempt record. It intentionally excludes the raw
// lease token; only its hash is persisted.
type WorkAttempt struct {
	ID                    string
	IssueID               string
	SessionID             *string
	AgentLabel            *string
	Kind                  AttemptKind
	Status                AttemptStatus
	IssueVersionAtStart   int64
	ContextEventIDAtStart int64
	LeaseExpiresAt        time.Time
	StartedAt             time.Time
	LastHeartbeatAt       time.Time
	FinishedAt            *time.Time
}

type AttemptNoteKind string

const (
	AttemptNoteKindProgress   AttemptNoteKind = "progress"
	AttemptNoteKindFinding    AttemptNoteKind = "finding"
	AttemptNoteKindWarning    AttemptNoteKind = "warning"
	AttemptNoteKindCheckpoint AttemptNoteKind = "checkpoint"
)

func (value AttemptNoteKind) Valid() bool {
	switch value {
	case AttemptNoteKindProgress, AttemptNoteKindFinding, AttemptNoteKindWarning, AttemptNoteKindCheckpoint:
		return true
	default:
		return false
	}
}

// AttemptNote is the durable, append-only recovery note associated with one attempt.
type AttemptNote struct {
	ID        string
	AttemptID string
	Kind      AttemptNoteKind
	Content   string
	NextSteps []string
	Important bool
	CreatedAt time.Time
}

type ClaimIssueInput struct {
	IssueID      string
	LeaseSeconds *int
}

func (input ClaimIssueInput) Validate() (ClaimIssueInput, error) {
	if _, err := ParseIssueIdentifier(input.IssueID); err != nil {
		return ClaimIssueInput{}, err
	}
	lease, err := validateLeaseSeconds(input.LeaseSeconds)
	if err != nil {
		return ClaimIssueInput{}, err
	}
	return ClaimIssueInput{IssueID: input.IssueID, LeaseSeconds: lease}, nil
}

type RenewAttemptInput struct {
	AttemptID    string
	LeaseToken   string
	LeaseSeconds *int
}

func (input RenewAttemptInput) Validate() (RenewAttemptInput, error) {
	if _, err := ulid.ParseStrict(input.AttemptID); err != nil || len(input.AttemptID) != 26 {
		return RenewAttemptInput{}, validationError("attempt_id", "INVALID_ULID", "must be a canonical ULID")
	}
	if strings.TrimSpace(input.LeaseToken) == "" {
		return RenewAttemptInput{}, validationError("lease_token", "REQUIRED", "is required")
	}
	if err := ValidateText("lease_token", input.LeaseToken, MaxLeaseTokenRunes); err != nil {
		return RenewAttemptInput{}, err
	}
	lease, err := validateLeaseSeconds(input.LeaseSeconds)
	if err != nil {
		return RenewAttemptInput{}, err
	}
	return RenewAttemptInput{AttemptID: input.AttemptID, LeaseToken: input.LeaseToken, LeaseSeconds: lease}, nil
}

type SaveAttemptNoteInput struct {
	AttemptID  string
	LeaseToken string
	Kind       AttemptNoteKind
	Content    string
	NextSteps  []string
	Important  bool
}

func (input SaveAttemptNoteInput) Validate() (SaveAttemptNoteInput, error) {
	attemptID, err := ulid.ParseStrict(input.AttemptID)
	if err != nil || len(input.AttemptID) != 26 || attemptID.String() != input.AttemptID {
		return SaveAttemptNoteInput{}, validationError("attempt_id", "INVALID_ULID", "must be a canonical ULID")
	}
	if strings.TrimSpace(input.LeaseToken) == "" {
		return SaveAttemptNoteInput{}, validationError("lease_token", "REQUIRED", "is required")
	}
	if err := ValidateText("lease_token", input.LeaseToken, MaxLeaseTokenRunes); err != nil {
		return SaveAttemptNoteInput{}, err
	}
	if !input.Kind.Valid() {
		return SaveAttemptNoteInput{}, validationError("kind", "INVALID_ENUM", "must be progress, finding, warning, or checkpoint")
	}
	if strings.TrimSpace(input.Content) == "" {
		return SaveAttemptNoteInput{}, validationError("content", "REQUIRED", "is required")
	}
	if err := ValidateText("content", input.Content, MaxAttemptNoteRunes); err != nil {
		return SaveAttemptNoteInput{}, err
	}
	nextSteps, err := CopyBounded("next_steps", input.NextSteps, MaxAttemptNoteNextSteps)
	if err != nil {
		return SaveAttemptNoteInput{}, err
	}
	for _, nextStep := range nextSteps {
		field := "next_steps"
		if strings.TrimSpace(nextStep) == "" {
			return SaveAttemptNoteInput{}, validationError(field, "REQUIRED", "items must be nonblank")
		}
		if err := ValidateText(field, nextStep, MaxAttemptNoteNextStepRunes); err != nil {
			return SaveAttemptNoteInput{}, err
		}
	}
	return SaveAttemptNoteInput{
		AttemptID: input.AttemptID, LeaseToken: input.LeaseToken, Kind: input.Kind, Content: input.Content,
		NextSteps: nextSteps, Important: input.Important,
	}, nil
}

func validateLeaseSeconds(value *int) (*int, error) {
	seconds := DefaultLeaseSeconds
	if value != nil {
		seconds = *value
	}
	if seconds < MinLeaseSeconds || seconds > MaxLeaseSeconds {
		return nil, validationError("lease_seconds", "OUT_OF_RANGE", "must be between 60 and 3600")
	}
	return &seconds, nil
}
